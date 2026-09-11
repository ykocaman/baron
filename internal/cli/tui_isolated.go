package cli

import (
	"bytes"
	"strings"
)

func (a *app) newIsolatedApp(out, errOut *bytes.Buffer) *app {
	return &app{
		out:          out,
		errOut:       errOut,
		in:           a.in,
		dir:          a.dir,
		runner:       a.runner,
		beads:        a.beads,
		agents:       a.agents,
		gate:         a.gate,
		worktrees:    a.worktrees,
		backend:      a.backend,
		audit:        a.audit,
		runs:         a.runs,
		loadConfig:   a.loadConfig,
		loadPersonas: a.loadPersonas,
		savePersona:  a.savePersona,
		inTTY:        func() bool { return false },
		brnPrefix:    a.brnPrefix,
		noColor:      a.noColor,
	}
}

// isolatedApp is newIsolatedApp with its stdout/stderr buffers thrown away —
// for a typed call whose return value already carries everything the caller
// needs (a *Result struct), where a.warn's side effect (real os.Stderr, see
// newApp's call site) is the only thing worth isolating against, not the
// text itself. See isolatedCall for the sibling that captures and returns
// the text.
func (a *app) isolatedApp() *app {
	return a.newIsolatedApp(&bytes.Buffer{}, &bytes.Buffer{})
}

// isolatedCall runs fn against a fresh isolated sub-app (see
// newIsolatedApp), combining whatever it wrote to stdout/stderr into one
// string — for a typed call into pipeline code (merge, resume) that still
// prints through a.out/a.warn, so that output is captured instead of
// leaking into the TUI's own rendering.
func (a *app) isolatedCall(fn func(sub *app) error) (string, error) {
	var out, errOut bytes.Buffer
	err := fn(a.newIsolatedApp(&out, &errOut))
	output := out.String()
	if errOut.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += strings.TrimSpace(errOut.String())
	}
	return output, err
}
