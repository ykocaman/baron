package profile

import "testing"

func TestReactTSProfileSteps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
		"name": "web",
		"devDependencies": {"vitest": "^3.0.0", "typescript": "^5.0.0"}
	}`)
	writeFile(t, dir, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	writeFile(t, dir, "tsconfig.json", `{"compilerOptions": {"strict": true}}`)
	writeFile(t, dir, "vite.config.ts", "export default {}\n")
	// Local oxlint binary short-circuits before PATH, keeping this test deterministic.
	writeFile(t, dir, "node_modules/.bin/oxlint", "#!/bin/sh\n")

	p := ReactTS(dir)
	if p.Name != "react-ts" {
		t.Errorf("Name = %q, want %q", p.Name, "react-ts")
	}
	want := []struct{ name, cmd string }{
		{"install", "pnpm"},
		{"format", "prettier"},
		{"lint", "oxlint"},
		{"typecheck", "tsc"},
		{"test", "vitest"},
		{"build-typecheck", "tsc"},
		{"build", "vite"},
	}
	if len(p.Steps) != len(want) {
		t.Fatalf("len(Steps) = %d, want %d: %+v", len(p.Steps), len(want), p.Steps)
	}
	for i, w := range want {
		if p.Steps[i].Name != w.name || p.Steps[i].Command != w.cmd {
			t.Errorf("Steps[%d] = %s %s, want %s %s", i, p.Steps[i].Name, p.Steps[i].Command, w.name, w.cmd)
		}
	}
	if got := p.Steps[0].Args; len(got) != 2 || got[0] != "install" || got[1] != "--frozen-lockfile" {
		t.Errorf("install args = %v, want [install --frozen-lockfile]", got)
	}
	if got := p.Steps[3].Args; len(got) != 1 || got[0] != "--noEmit" {
		t.Errorf("typecheck args = %v, want [--noEmit]", got)
	}
	// row 6: `vite build` doesn't typecheck by itself.
	if got := p.Steps[5].Args; len(got) != 1 || got[0] != "-b" {
		t.Errorf("build-typecheck args = %v, want [-b]", got)
	}
	if got := p.Steps[6].Args; len(got) != 1 || got[0] != "build" {
		t.Errorf("build args = %v, want [build]", got)
	}
}

// TestReactTSProfileFrozenInstallCommands: — each
// package manager has its own canonical frozen-install flag.
func TestReactTSProfileFrozenInstallCommands(t *testing.T) {
	tests := []struct {
		lockfile string
		wantCmd  string
		wantArgs []string
	}{
		{"bun.lockb", "bun", []string{"ci"}},
		{"yarn.lock", "yarn", []string{"install", "--immutable"}},
		{"package-lock.json", "npm", []string{"ci"}},
		{"pnpm-lock.yaml", "pnpm", []string{"install", "--frozen-lockfile"}},
	}
	for _, tt := range tests {
		t.Run(tt.lockfile, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "package.json", `{"name": "web"}`)
			writeFile(t, dir, tt.lockfile, "")
			p := ReactTS(dir)
			install := p.Steps[0]
			if install.Command != tt.wantCmd {
				t.Fatalf("install command = %q, want %q", install.Command, tt.wantCmd)
			}
			if len(install.Args) != len(tt.wantArgs) {
				t.Fatalf("install args = %v, want %v", install.Args, tt.wantArgs)
			}
			for i, a := range tt.wantArgs {
				if install.Args[i] != a {
					t.Errorf("install args = %v, want %v", install.Args, tt.wantArgs)
				}
			}
		})
	}
}

func TestReactTSProfileSkipOptional(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name": "web"}`)

	p := ReactTS(dir)
	// No lockfile, no linter config, no test runner, no bundler: only
	// format and typecheck remain.
	if len(p.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2: %+v", len(p.Steps), p.Steps)
	}
	if p.Steps[0].Name != "format" || p.Steps[1].Name != "typecheck" {
		t.Errorf("steps = %s, %s, want format, typecheck", p.Steps[0].Name, p.Steps[1].Name)
	}
}

// TestReactTSProfileESLintFlatConfig: row 3 — ESLint's
// modern flat config (eslint.config.*) is a lint signal too, not just the
// legacy .eslintrc*.
func TestReactTSProfileESLintFlatConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name": "web"}`)
	writeFile(t, dir, "eslint.config.js", "export default []\n")

	p := ReactTS(dir)
	var lint *GateStep
	for i := range p.Steps {
		if p.Steps[i].Name == "lint" {
			lint = &p.Steps[i]
		}
	}
	if lint == nil {
		t.Fatalf("no lint step found: %+v", p.Steps)
	}
	if lint.Command != "eslint" {
		t.Errorf("lint command = %q, want eslint", lint.Command)
	}
}

func TestDetectPackageManager(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pnpm-lock.yaml", "")
	pm, lockfile := detectPackageManager(dir)
	if pm != "pnpm" || lockfile != "pnpm-lock.yaml" {
		t.Errorf("detectPackageManager = (%q, %q), want (pnpm, pnpm-lock.yaml)", pm, lockfile)
	}
}

func TestDetectPackageManagerBun(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bun.lockb", "")
	pm, lockfile := detectPackageManager(dir)
	if pm != "bun" || lockfile != "bun.lockb" {
		t.Errorf("detectPackageManager = (%q, %q), want (bun, bun.lockb)", pm, lockfile)
	}
}

func TestDetectPackageManagerFirstMatchWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bun.lockb", "")
	writeFile(t, dir, "package-lock.json", "")
	pm, lockfile := detectPackageManager(dir)
	if pm != "bun" || lockfile != "bun.lockb" {
		t.Errorf("detectPackageManager = (%q, %q), want first match (bun, bun.lockb)", pm, lockfile)
	}
}

func TestDetectPackageManagerNone(t *testing.T) {
	pm, lockfile := detectPackageManager(t.TempDir())
	if pm != "" || lockfile != "" {
		t.Errorf("detectPackageManager = (%q, %q), want empty", pm, lockfile)
	}
}

// TestDetectPackageManagerBunTextLockfile: newer Bun versions write
// bun.lock (text), not just the legacy binary bun.lockb.
func TestDetectPackageManagerBunTextLockfile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bun.lock", "")
	pm, lockfile := detectPackageManager(dir)
	if pm != "bun" || lockfile != "bun.lock" {
		t.Errorf("detectPackageManager = (%q, %q), want (bun, bun.lock)", pm, lockfile)
	}
}

// TestDetectPackageManagerManifestFallback: row 5 — no
// lockfile falls back to package.json's "packageManager" field.
func TestDetectPackageManagerManifestFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"packageManager": "pnpm@8.6.0"}`)
	pm, lockfile := detectPackageManager(dir)
	if pm != "pnpm" || lockfile != "" {
		t.Errorf("detectPackageManager = (%q, %q), want (pnpm, \"\")", pm, lockfile)
	}
}

// TestDetectPackageManagerDevEnginesFallback: row 5 —
// with no lockfile and no "packageManager", falls back to
// devEngines.packageManager.name.
func TestDetectPackageManagerDevEnginesFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"devEngines": {"packageManager": {"name": "yarn"}}}`)
	pm, lockfile := detectPackageManager(dir)
	if pm != "yarn" || lockfile != "" {
		t.Errorf("detectPackageManager = (%q, %q), want (yarn, \"\")", pm, lockfile)
	}
}

// TestDetectPackageManagerLockfileWinsOverManifest: an actual lockfile is
// still the authoritative signal even if packageManager names something
// else (drift between the two shouldn't silently prefer the manifest).
func TestDetectPackageManagerLockfileWinsOverManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"packageManager": "yarn@3.2.0"}`)
	writeFile(t, dir, "pnpm-lock.yaml", "")
	pm, lockfile := detectPackageManager(dir)
	if pm != "pnpm" || lockfile != "pnpm-lock.yaml" {
		t.Errorf("detectPackageManager = (%q, %q), want the lockfile (pnpm, pnpm-lock.yaml)", pm, lockfile)
	}
}
