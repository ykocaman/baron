// Package cli is BARON's composition root: it builds the app (stores,
// config, agent registry, gate runner) and wires it into the TUI. There is
// no command-line interface here any more (see baron-09q) — the package
// name is a historical leftover from when it was one.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
	"github.com/baron-cli/baron/internal/tui"
)

// Process exit codes. 0-2 are live; 3-6 are reserved for Slices 2/3.
const (
	ExitOK       = 0 // success
	ExitError    = 1 // general error: config, missing tool, command failure
	ExitUsage    = 2 // usage error: invalid arguments
	ExitRetry    = 3 // gate left: work entered the retry flow (Slice 2)
	ExitGateFail = 4 // gate failed: work parked in the human queue (Slice 2/3)
	ExitMerge    = 5 // merge rejected (Slice 3)
	ExitHuman    = 6 // secret found: merge blocked (Slice 3)
)

// usageError marks an error as a usage error, exiting with ExitUsage.
type usageError struct{ err error }

func (u usageError) Error() string { return u.err.Error() }
func (u usageError) Unwrap() error { return u.err }

// silentError exits with code without printing anything; the command already
// produced its full output.
type silentError struct{ code int }

func (silentError) Error() string { return "" }

// usagef builds a usage error.
func usagef(format string, args ...any) error {
	return usageError{err: fmt.Errorf(format, args...)}
}

// app carries the dependencies shared by every command.
type app struct {
	out    io.Writer
	errOut io.Writer
	in     io.Reader
	dir    string

	runner    tool.Runner
	beads     *store.BeadStore
	agents    *agent.Registry
	gate      *domain.GateRunner
	worktrees *domain.WorktreeManager
	backend   *domain.Backend
	audit     *store.AuditStore
	runs      *store.RunStore

	loadConfig func(dir string) (*store.Config, error)
	// loadPersonas resolves Crew Mode's persona set (docs/PRD/crew-mode.md)
	// — a field, not a direct persona.Dir/persona.LoadAll call, for the
	// same reason loadConfig is: so tests don't read the developer's real
	// ~/.config/baron/personas.
	loadPersonas func() ([]persona.Persona, error)
	// savePersona is loadPersonas' write-side counterpart (UpdatePersonaByID,
	// Crew Mode's 'e' edit form) — same test-isolation reasoning.
	savePersona func(persona.Persona) error
	inTTY       func() bool
	// tuiRun launches the TUI; a field (not a direct tui.Run call) so tests
	// can stub it out. A real tea.Program blocks reading its input reader
	// until a quit message arrives — an empty test io.Reader never sends
	// one, so calling the real tui.Run against fake test buffers hangs.
	tuiRun func(ctx context.Context, deps tui.Deps, in io.Reader, out io.Writer) error

	// brnPrefix is the configurable BRN prefix (general.brn_prefix, default
	// empty); with an empty prefix bare bd issue IDs are the references.
	brnPrefix string

	json    bool
	quiet   bool
	yes     bool
	noColor bool
}

// newApp builds the app bound to the process environment.
func newApp(out, errOut io.Writer, runner tool.Runner) *app {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	a := &app{
		out:        out,
		errOut:     errOut,
		in:         os.Stdin,
		dir:        dir,
		runner:     runner,
		loadConfig: store.LoadConfig,
		loadPersonas: func() ([]persona.Persona, error) {
			return loadPersonasFromDisk(dir)
		},
		savePersona: savePersonaToDisk,
		tuiRun:      tui.Run,
	}
	a.beads = store.NewBeadStore(runner, dir)
	cfg, cfgErr := a.loadConfig(dir)
	if cfgErr != nil {
		// Falls back to defaults rather than aborting startup entirely — a
		// malformed config.toml must not brick the whole tool — but silently
		// doing so left a user with a broken config.toml staring at
		// confusing downstream symptoms (wrong base branch, wrong gate
		// timeout) with no indication why. warn is safe to call this early:
		// a.quiet is always its zero value (false) here, nothing in the
		// production Run path ever sets it.
		cfg = store.DefaultConfig()
		a.warn("config: %v (using defaults)", cfgErr)
	} else {
		a.brnPrefix = cfg.General.BRNPrefix
	}
	a.audit = store.NewAuditStore(filepath.Join(dir, cfg.Audit.Path))
	a.runs = store.NewRunStore(filepath.Join(dir, cfg.Cost.RunsPath))
	a.beads.SetBRNPrefix(a.brnPrefix)
	a.agents = agent.NewRegistry()
	// cfg.Gate.Timeout is minutes; GateRunner's internal unit is seconds,
	// matching profile.GateStep.Timeout's existing semantics.
	a.gate = domain.NewGateRunner(runner, cfg.Gate.Timeout*60, cfg.Gate.FailFast)
	a.worktrees = domain.NewWorktreeManager(runner, dir, filepath.Join(dir, ".baron", "worktrees"))
	// New bead branches fork from the local base branch, which `baron merge`
	// keeps current on every merge — no remote, no push.
	a.worktrees.BaseBranch = cfg.General.BaseBranch
	a.backend = domain.NewBackend(runner)
	a.inTTY = func() bool {
		// Interactive means BOTH ends are terminals: with stdin on a TTY but
		// stdout piped (baron > file, baron | wrapper), the TUI would launch
		// yet lipgloss's renderer (bound to os.Stdout) would degrade to
		// plain ASCII and silently strip every color.
		tt := func(f *os.File) bool {
			fi, err := f.Stat()
			if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
				return false
			}
			// /dev/null is a char device but never interactive.
			nullInfo, statErr := os.Stat(os.DevNull)
			return statErr != nil || !os.SameFile(fi, nullInfo)
		}
		in, okIn := a.in.(*os.File)
		out, okOut := a.out.(*os.File)
		if !okIn || !okOut {
			return false
		}
		return tt(in) && tt(out)
	}
	return a
}

// Run is BARON's single entry point: no subcommands, no flags — TUI-only
// (see baron-09q). A fresh or incompletely-configured project is
// initialized automatically before the TUI opens (Init, alwaysApprove: the
// "initialize?" question is redundant when someone just ran baron; the
// auto-merge policy question is real and still asks for real, via
// confirmAutoMerge's own huh form, writing the answer to
// .baron/config.toml). baron requires a real interactive terminal — there
// is no headless/scriptable fallback any more.
func Run(version, commit, date string) int {
	a := newApp(os.Stdout, os.Stderr, tool.NewRunner())
	return a.run(version, commit, date)
}

// run is Run's app-level body, separated out so tests can drive it against
// a fake runner/buffers instead of the real process's stdio.
func (a *app) run(version, commit, date string) int {
	a.noColor = a.noColor || colorprofile.Env(os.Environ()) <= colorprofile.Ascii
	if !a.inTTY() {
		_, _ = fmt.Fprintln(a.errOut, "baron requires an interactive terminal")
		return ExitError
	}
	ctx := context.Background()
	if _, _, err := a.Init(ctx, alwaysApprove, a.confirmAutoMerge); err != nil {
		_, _ = fmt.Fprintln(a.errOut, "Error:", err)
		return ExitError
	}
	versionString := fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)
	if err := a.runTUI(ctx, versionString); err != nil {
		var se silentError
		if errors.As(err, &se) {
			return se.code
		}
		_, _ = fmt.Fprintln(a.errOut, "Error:", err)
		return ExitError
	}
	return ExitOK
}

// printJSON writes v to w as indented JSON followed by a newline.
func printJSON[T any](w io.Writer, v T) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

// outf prints to stdout unless quiet. JSON output ignores quiet: scripts
// combining --json --quiet still receive the data.
func (a *app) outf(format string, args ...any) {
	if a.quiet {
		return
	}
	_, _ = fmt.Fprintf(a.out, format, args...)
}

// warn writes a warning to stderr unless quiet.
func (a *app) warn(format string, args ...any) {
	if a.quiet {
		return
	}
	_, _ = fmt.Fprintf(a.errOut, "warning: "+format+"\n", args...)
}

// aborted reports a declined interactive confirmation to stderr.
func (a *app) aborted() {
	_, _ = fmt.Fprintln(a.errOut, "aborted")
}

// auditLog appends an event to the audit log attributed to the current OS
// user. Audit failures degrade to a warning: a broken audit trail must never
// block the command that produced it.
func (a *app) auditLog(action, target, detail string) {
	a.auditLogAs(store.Actor{Type: store.ActorUser, Name: currentUser()}, action, target, detail)
}

// auditLogAs is auditLog with an explicit actor, for callers that aren't a
// human at a keyboard — chiefly the reconciler (domain.ActorManager),
// which must be distinguishable from a human in the audit trail or a
// misbehaving background loop is undiagnosable.
func (a *app) auditLogAs(actor store.Actor, action, target, detail string) {
	if a.audit == nil {
		return
	}
	event := store.AuditEvent{
		Time:   time.Now().UTC(),
		Actor:  actor,
		Action: action,
		Target: target,
		Detail: detail,
	}
	if err := a.audit.Append(event); err != nil {
		a.warn("audit: %v", err)
	}
}

// currentUser returns the OS username for audit attribution.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return os.Getenv("USERNAME")
}
