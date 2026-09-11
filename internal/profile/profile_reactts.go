package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// packageJSON is the Node manifest filename shared across React-TS
// detection and build.
const packageJSON = "package.json"

// ReactTS returns the React-TS gate profile for dir, detecting the
// package manager, linter, test runner, and bundler from project files.
func ReactTS(dir string) Profile {
	p := Profile{Name: "react-ts"}

	p.Steps = append(p.Steps, installSteps(dir)...)
	p.Steps = append(p.Steps, GateStep{Name: "format", Command: "prettier", Args: []string{"--check", "."}})
	p.Steps = append(p.Steps, buildSteps([][]stepAlt{
		{
			step("lint", "oxlint", nil, func() bool { return hasLocalBinary(dir, "oxlint") || onPath("oxlint") }),
			step("lint", "eslint", []string{"."}, func() bool { return hasGlob(dir, ".eslintrc*") || hasGlob(dir, "eslint.config.*") }),
		},
	})...)

	// typecheck: -b for composite projects, --noEmit otherwise — the args
	// depend on tsconfig's content, not just whether a file exists, so this
	// stays a plain value computation rather than a stepAlt slot.
	tscArgs := []string{"--noEmit"}
	if tsconfigComposite(dir) {
		tscArgs = []string{"-b"}
	}
	p.Steps = append(p.Steps, GateStep{Name: "typecheck", Command: "tsc", Args: tscArgs})

	p.Steps = append(p.Steps, buildSteps([][]stepAlt{
		{
			step("test", "vitest", []string{"run"}, func() bool { return detectTestRunner(dir) == "vitest" }),
			step("test", "jest", nil, func() bool { return detectTestRunner(dir) == "jest" }),
		},
		{
			step("build", "next", []string{"build"}, func() bool { return hasGlob(dir, "next.config.*") }),
			{
				// `vite build` alone doesn't typecheck, so this alternative
				// is the one case where a single slot contributes two steps.
				steps: []GateStep{
					{Name: "build-typecheck", Command: "tsc", Args: []string{"-b"}},
					{Name: "build", Command: "vite", Args: []string{"build"}},
				},
				when: func() bool { return hasGlob(dir, "vite.config.*") },
			},
		},
	})...)

	return p
}

// installSteps returns react-ts's install step for dir. The package manager
// itself doubles as the command name (npm/yarn/bun get their own canonical
// frozen-install flag; anything else falls back to --frozen-lockfile), so
// unlike the other steps this can't be expressed as a fixed set of named
// stepAlts — the command is the detected value, not a constant.
func installSteps(dir string) []GateStep {
	switch pm, _ := detectPackageManager(dir); pm {
	case "":
		return nil
	case "npm":
		return []GateStep{{Name: "install", Command: "npm", Args: []string{"ci"}}}
	case "bun":
		return []GateStep{{Name: "install", Command: "bun", Args: []string{"ci"}}}
	case "yarn":
		return []GateStep{{Name: "install", Command: "yarn", Args: []string{"install", "--immutable"}}}
	default:
		return []GateStep{{Name: "install", Command: pm, Args: []string{"install", "--frozen-lockfile"}}}
	}
}

// detectPackageManager returns the package manager and its lockfile for
// dir. Lockfiles are checked in order: bun, pnpm, yarn, npm; the first
// match wins (a conflict — several lockfiles present — resolves silently in
// that order rather than printing a warning: package.go has no app instance
// to route a warning through, and a raw log.Printf here used to write
// straight to real os.Stderr underneath the TUI's alt-screen, corrupting the
// display exactly like the a.warn calls documented in cli/tui.go's Deps
// wiring — see isolatedApp). With no lockfile, falls back to package.json's
// "packageManager" field, then "devEngines.packageManager"; if neither
// resolves it, returns ("", "") — no install step is added and no lockfile
// is silently written.
func detectPackageManager(dir string) (string, string) {
	candidates := []struct {
		files []string
		pm    string
	}{
		{[]string{"bun.lock", "bun.lockb"}, "bun"},
		{[]string{"pnpm-lock.yaml"}, "pnpm"},
		{[]string{"yarn.lock"}, "yarn"},
		{[]string{"package-lock.json"}, "npm"},
	}
	for _, c := range candidates {
		for _, f := range c.files {
			if exists(filepath.Join(dir, f)) {
				return c.pm, f
			}
		}
	}
	return manifestPackageManager(dir), ""
}

// manifestPackageManager reads package.json's "packageManager" field (e.g.
// "pnpm@8.6.0"), falling back to "devEngines.packageManager.name"; "" if
// neither is set.
func manifestPackageManager(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, packageJSON))
	if err != nil {
		return ""
	}
	var pkg struct {
		PackageManager string `json:"packageManager"`
		DevEngines     struct {
			PackageManager struct {
				Name string `json:"name"`
			} `json:"packageManager"`
		} `json:"devEngines"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	if pkg.PackageManager != "" {
		name, _, _ := strings.Cut(pkg.PackageManager, "@")
		return name
	}
	return pkg.DevEngines.PackageManager.Name
}

// detectTestRunner returns "vitest", "jest", or "" based on package.json
// dependencies and devDependencies.
func detectTestRunner(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, packageJSON))
	if err != nil {
		return ""
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	switch {
	case hasDep(pkg.Dependencies, "vitest") || hasDep(pkg.DevDependencies, "vitest"):
		return "vitest"
	case hasDep(pkg.Dependencies, "jest") || hasDep(pkg.DevDependencies, "jest"):
		return "jest"
	}
	return ""
}

// hasDep reports whether deps contains name.
func hasDep(deps map[string]string, name string) bool {
	_, ok := deps[name]
	return ok
}

// tsconfigComposite reports whether tsconfig.json sets "composite": true.
// Non-JSON or comment-bearing tsconfigs count as non-composite.
func tsconfigComposite(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "tsconfig.json"))
	if err != nil {
		return false
	}
	var ts struct {
		Composite *bool `json:"composite"`
	}
	if err := json.Unmarshal(data, &ts); err != nil {
		return false
	}
	return ts.Composite != nil && *ts.Composite
}

// hasLocalBinary reports whether name exists in the project's node_modules/.bin.
func hasLocalBinary(dir, name string) bool {
	return exists(filepath.Join(dir, "node_modules", ".bin", name))
}
