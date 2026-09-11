package cli

import (
	"reflect"
	"strings"

	"github.com/baron-cli/baron/internal/profile"
	"github.com/baron-cli/baron/internal/store"
)

// isDefaultProfile reports whether p equals the shipped default for name.
// DefaultConfig ships placeholder go/react-ts entries (command strings with
// no args and no tidy step) that would shadow the richer built-in model
// profiles; only genuinely user-defined entries override the built-ins.
func isDefaultProfile(name string, p store.Profile) bool {
	return reflect.DeepEqual(p, store.DefaultConfig().Profiles[name])
}

// profileByName returns the named profile for dir. A [profiles.<name>]
// section in cfg wins over the built-in profiles; unknown names fall back
// to the detected profile.
func profileByName(cfg *store.Config, name, dir string) profile.Profile {
	if cfg != nil {
		if p, ok := cfg.Profiles[name]; ok && !isDefaultProfile(name, p) {
			return profileFromStore(name, p, dir)
		}
	}
	return profile.ByName(name, dir)
}

// gateStepOverrideOrder is profileFromStore's fixed field-to-step-name
// mapping, in a stable order so a profile built from config is
// deterministic (map iteration isn't) whether a field overrides an
// existing auto-detected step or gets appended as a new one.
var gateStepOverrideOrder = []string{"format", "lint", "typecheck", "test", "build"}

// profileFromStore converts a [profiles.<name>] section to a runnable gate
// profile, layered onto dir's auto-detection (profile.Detect) rather than
// replacing it outright — so e.g. a project that only customizes `test`
// keeps the Go profile's "tidy" step and its auto-detected linter/build.
//
// p.Steps, if set, is the escape hatch instead: it replaces auto-detection
// and every flat field wholesale with a fully custom pipeline — the way to
// support a project type BARON has no built-in profile for without writing
// any Go code (see docs/PRD/harness-hardening.md §2, Katman 2).
//
// Otherwise each non-empty flat field (format/lint/typecheck/test/build)
// overrides the matching auto-detected step by name, split on whitespace
// into command and args; a field naming a step auto-detection didn't
// produce (e.g. typecheck for a Go project) is appended instead of
// dropped. package_manager still isn't consumed on its own — an install
// step is a side effect of auto-detection (react-ts's lockfile sniffing),
// not something a flat field maps onto.
func profileFromStore(name string, p store.Profile, dir string) profile.Profile {
	base := profile.Detect(dir)
	base.Name = name

	if len(p.Steps) > 0 {
		steps := make([]profile.GateStep, len(p.Steps))
		for i, s := range p.Steps {
			steps[i] = profile.GateStep{Name: s.Name, Command: s.Command, Args: s.Args, Timeout: s.Timeout}
		}
		base.Steps = steps
		return base
	}

	overrides := map[string]string{
		"format":    p.Formatter,
		"lint":      p.Linter,
		"typecheck": p.Typecheck,
		"test":      p.Test,
		"build":     p.Build,
	}
	applied := make(map[string]bool, len(overrides))
	for i, step := range base.Steps {
		cmd := overrides[step.Name]
		if strings.TrimSpace(cmd) == "" {
			continue
		}
		fields := strings.Fields(cmd)
		base.Steps[i] = profile.GateStep{Name: step.Name, Command: fields[0], Args: fields[1:]}
		applied[step.Name] = true
	}
	for _, stepName := range gateStepOverrideOrder {
		cmd := overrides[stepName]
		if strings.TrimSpace(cmd) == "" || applied[stepName] {
			continue
		}
		fields := strings.Fields(cmd)
		base.Steps = append(base.Steps, profile.GateStep{Name: stepName, Command: fields[0], Args: fields[1:]})
	}
	return base
}
