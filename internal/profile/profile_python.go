package profile

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	pyprojectTOML   = "pyproject.toml"
	requirementsTXT = "requirements.txt"
)

// Python returns the Python gate profile for dir, detecting the installer,
// formatter/linter, and typechecker from project files. test always runs
// pytest — the de facto standard — the way Go's profile always runs `go
// test` regardless of what else it detects.
func Python(dir string) Profile {
	return Profile{
		Name: "python",
		Steps: buildSteps([][]stepAlt{
			// install: poetry.lock/Pipfile/uv.lock each name their own
			// manager unambiguously; a bare requirements.txt falls back to
			// pip. No manifest at all means no install step.
			{
				step("install", "poetry", []string{"install"}, func() bool { return exists(filepath.Join(dir, "poetry.lock")) }),
				step("install", "pipenv", []string{"install", "--dev", "--deploy"}, func() bool {
					return exists(filepath.Join(dir, "Pipfile.lock")) || exists(filepath.Join(dir, "Pipfile"))
				}),
				step("install", "uv", []string{"sync"}, func() bool { return exists(filepath.Join(dir, "uv.lock")) }),
				step("install", "pip", []string{"install", "-r", requirementsTXT}, func() bool {
					return exists(filepath.Join(dir, requirementsTXT))
				}),
			},
			// format/lint: ruff covers both if configured, else black
			// (format) and flake8 (lint) individually — ruff is a drop-in
			// replacement for both, so running all three would be
			// redundant.
			{
				step("format", "ruff", []string{"format", "--check", "."}, func() bool { return usesRuff(dir) }),
				step("format", "black", []string{"--check", "."}, func() bool {
					return exists(filepath.Join(dir, pyprojectTOML)) || exists(filepath.Join(dir, "setup.cfg"))
				}),
			},
			{
				step("lint", "ruff", []string{"check", "."}, func() bool { return usesRuff(dir) }),
				step("lint", "flake8", []string{"."}, func() bool {
					return exists(filepath.Join(dir, ".flake8")) || pyprojectHasSection(dir, "[tool.flake8]")
				}),
			},
			{step("typecheck", "mypy", []string{"."}, func() bool {
				return pyprojectHasSection(dir, "[tool.mypy]") || exists(filepath.Join(dir, "mypy.ini"))
			})},
			{step("test", "pytest", nil, nil)},
		}),
	}
}

// detectPython reports whether dir is a Python project.
func detectPython(dir string) bool {
	for _, f := range []string{pyprojectTOML, requirementsTXT, "setup.py", "setup.cfg", "Pipfile"} {
		if exists(filepath.Join(dir, f)) {
			return true
		}
	}
	return false
}

// usesRuff reports whether dir configures ruff, either via a dedicated
// config file or a [tool.ruff] section in pyproject.toml.
func usesRuff(dir string) bool {
	return exists(filepath.Join(dir, "ruff.toml")) ||
		exists(filepath.Join(dir, ".ruff.toml")) ||
		pyprojectHasSection(dir, "[tool.ruff")
}

// pyprojectHasSection reports whether dir's pyproject.toml contains a line
// starting with marker (a crude but dependency-free TOML section check).
func pyprojectHasSection(dir, marker string) bool {
	data, err := os.ReadFile(filepath.Join(dir, pyprojectTOML))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), marker) {
			return true
		}
	}
	return false
}
