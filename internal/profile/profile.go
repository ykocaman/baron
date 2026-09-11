// Package profile detects a project's gate profile — the ordered
// format/lint/test/build steps `baron work gate` runs — from the files in
// its working tree. Each supported language lives in its own
// profile_<lang>.go file and registers a detect/build pair in the registry
// below; adding or dropping a language is a one-line change there; Detect
// and ByName never need to change.
package profile

import (
	"os"
	"os/exec"
	"path/filepath"
)

// GateStep represents a single validation step.
type GateStep struct {
	Name    string   // e.g., "format", "lint", "test"
	Command string   // e.g., "gofmt", "go"
	Args    []string // e.g., ["-l", "."] or ["test", "-count=1", "./..."]
	Timeout int      // seconds, 0 = use gate default
}

// stepAlt is one candidate for a step slot: the GateSteps it contributes if
// when reports true (nil when = always matches, used for a slot's final
// unconditional fallback or a step no language variant ever skips). Most
// alternatives contribute exactly one GateStep (build with step); a few
// contribute more than one as an atomic unit (e.g. react-ts's vite build,
// which also needs a preceding typecheck pass vite itself doesn't do).
type stepAlt struct {
	steps []GateStep
	when  func() bool
}

// step builds a single-GateStep stepAlt — the common case.
func step(name, command string, args []string, when func() bool) stepAlt {
	return stepAlt{steps: []GateStep{{Name: name, Command: command, Args: args}}, when: when}
}

// buildSteps evaluates each ordered slot — a group of mutually exclusive
// alternatives for one pipeline stage — and appends the first alternative
// whose condition holds, skipping the slot entirely if none match. This is
// what makes a language profile close to pure data: adding a step is a new
// stepAlt in the right slot, nothing else to wire up.
func buildSteps(slots [][]stepAlt) []GateStep {
	var steps []GateStep
	for _, slot := range slots {
		for _, alt := range slot {
			if alt.when == nil || alt.when() {
				steps = append(steps, alt.steps...)
				break
			}
		}
	}
	return steps
}

// Profile represents a gate profile for a project type.
type Profile struct {
	Name  string // e.g., "go", "react-ts", "python", "rust", "php"
	Steps []GateStep
}

// language pairs one built-in profile's detection check with its builder.
type language struct {
	name   string
	detect func(dir string) bool
	build  func(dir string) Profile
}

// registry is the ordered list of built-in languages. Detect and ByName
// walk it top to bottom; the first matching (or named) entry wins. Order
// matters when a directory could match more than one detector (e.g. a Go
// service that vendors a package.json-based devtool) — earlier entries take
// priority. Supporting a new language is one more entry here plus its own
// profile_<lang>.go; dropping one is deleting the entry.
var registry = []language{
	{name: "go", detect: func(dir string) bool { return exists(filepath.Join(dir, "go.mod")) }, build: Go},
	{name: "react-ts", detect: func(dir string) bool { return exists(filepath.Join(dir, packageJSON)) }, build: ReactTS},
	{name: "rust", detect: detectRust, build: Rust},
	{name: "php", detect: detectPHP, build: PHP},
	{name: "python", detect: detectPython, build: Python},
}

// Detect returns the appropriate profile for a project directory, trying
// each registered language in order; a directory matching none of them
// defaults to the Go profile.
func Detect(dir string) Profile {
	for _, l := range registry {
		if l.detect(dir) {
			return l.build(dir)
		}
	}
	return Go(dir)
}

// BuildAll returns every built-in language's profile for dir, one per
// registered language, in registry order — e.g. for exporting a starter
// profile file per language without duplicating the registry elsewhere.
func BuildAll(dir string) []Profile {
	profiles := make([]Profile, len(registry))
	for i, l := range registry {
		profiles[i] = l.build(dir)
	}
	return profiles
}

// ByName returns the named built-in profile for dir. An unrecognized name
// (no config override exists for it either) falls back to Detect, so an
// unknown or misspelled profile name still gets a working pipeline instead
// of silently defaulting to Go regardless of what's actually in dir.
func ByName(name, dir string) Profile {
	for _, l := range registry {
		if l.name == name {
			return l.build(dir)
		}
	}
	return Detect(dir)
}

// exists reports whether path exists on disk.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasGlob reports whether any file matches pattern in dir.
func hasGlob(dir, pattern string) bool {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	return err == nil && len(matches) > 0
}

// onPath reports whether name is resolvable on PATH.
func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
