package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
	"github.com/baron-cli/baron/internal/tui"
)

// mustLoad reads a golden fixture from test/fixtures/bd-json at package load.
func mustLoad(name string) string {
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "bd-json", name))
	if err != nil {
		panic(err)
	}
	return string(data)
}

var (
	fixtureList  = mustLoad("list-non-envelope.json")
	fixtureReady = mustLoad("list-envelope.json")
)

// listOut serves the list fixture for `bd list --json`, the ready fixture
// for `bd ready --json`, and empty output for `bd comments --json`, matching
// the store's command surface.
func listOut(args []string) string {
	if len(args) > 0 && args[0] == "comments" {
		return "[]"
	}
	if len(args) > 0 && args[0] == "ready" {
		return fixtureReady
	}
	return fixtureList
}

// fakeRunner records bd invocations and serves canned stdout.
type fakeRunner struct {
	calls [][]string
	out   func(args []string) string
	err   string
	// run, when set, fully overrides command execution (name-aware); for
	// tests that must fail or script specific subprocesses.
	run func(name string, args []string) (tool.Result, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
	f.calls = append(f.calls, args)
	if f.run != nil {
		return f.run(name, args)
	}
	if f.err != "" {
		return tool.Result{Stderr: f.err}, errors.New("exit status 1")
	}
	stdout := ""
	switch {
	case name == "git" && len(args) > 0 && args[0] == "rev-parse":
		stdout = "true\n"
	case len(args) > 0 && args[0] == "--version":
		stdout = depVersion(name)
	case f.out != nil:
		stdout = f.out(args)
	case name == "bd" && len(args) >= 2 && args[0] == "list" && args[1] == "--all":
		stdout = "[]\n"
	}
	return tool.Result{Stdout: stdout}, nil
}

// depVersion serves a healthy version for the doctor's --version probes so
// init's mandatory-dependency gate passes in the fake environment.
func depVersion(name string) string {
	switch name {
	case "git":
		return "git version 2.50.1\n"
	case "bd":
		return "bd version 1.1.2\n"
	case "gitleaks":
		return "gitleaks 8.30.1\n"
	case "hunk":
		return "hunk 0.17.0\n"
	}
	return ""
}

// newTestApp wires an app with an isolated directory and buffers.
func newTestApp(t *testing.T, runner tool.Runner) *app {
	t.Helper()
	// Agent discovery and the model catalog are machine-wide caches
	// (~/.cache/baron). Point them at a temp dir so a test run neither
	// reads the developer's real catalog nor writes over it.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// store.LoadConfig/UserConfigDir and Init's profile export resolve
	// ~/.config/baron (os.UserHomeDir reads $HOME on Unix) — sandbox it too,
	// so Init's exportBuiltinProfiles never writes real starter profiles
	// into the developer's actual ~/.config/baron/profiles.
	t.Setenv("HOME", t.TempDir())
	var out, errOut bytes.Buffer
	a := &app{
		out:    &out,
		errOut: &errOut,
		in:     strings.NewReader(""),
		dir:    t.TempDir(),
		runner: runner,
		loadConfig: func(string) (*store.Config, error) {
			return store.DefaultConfig(), nil
		},
		// No personas by default — a test run must never read or (via
		// persona.LoadAll's first-run seeding) write the developer's real
		// ~/.config/baron/personas. Tests exercising the trigger engine
		// override this field directly.
		loadPersonas: func() ([]persona.Persona, error) { return nil, nil },
		savePersona:  func(persona.Persona) error { return nil },
		// A real tea.Program blocks reading a's input reader until it gets a
		// quit message, which an empty test buffer never sends. Tests that
		// care whether the TUI was launched can override this field.
		tuiRun: func(context.Context, tui.Deps, io.Reader, io.Writer) error {
			return nil
		},
	}
	a.beads = store.NewBeadStore(runner, a.dir)
	a.agents = agent.NewRegistry()
	a.gate = domain.NewGateRunner(runner, store.DefaultConfig().Gate.Timeout)
	a.worktrees = domain.NewWorktreeManager(runner, a.dir, filepath.Join(a.dir, ".baron", "worktrees"))
	a.backend = domain.NewBackend(runner)
	a.audit = store.NewAuditStore(filepath.Join(a.dir, store.DefaultConfig().Audit.Path))
	a.runs = store.NewRunStore(filepath.Join(a.dir, store.DefaultConfig().Cost.RunsPath))
	a.inTTY = func() bool { return false }
	return a
}

// stdout returns the app's captured stdout; newTestApp always wires buffers.
func stdout(t *testing.T, a *app) string {
	t.Helper()
	buf, ok := a.out.(*bytes.Buffer)
	if !ok {
		t.Fatalf("a.out is %T, want *bytes.Buffer", a.out)
	}
	return buf.String()
}

func stderr(t *testing.T, a *app) string {
	t.Helper()
	buf, ok := a.errOut.(*bytes.Buffer)
	if !ok {
		t.Fatalf("a.errOut is %T, want *bytes.Buffer", a.errOut)
	}
	return buf.String()
}

// auditEvents returns the events written by the app's audit store.
func auditEvents(t *testing.T, a *app) []store.AuditEvent {
	t.Helper()
	if a.audit == nil {
		t.Fatal("app has no audit store")
	}
	events, err := a.audit.All()
	if err != nil {
		t.Fatalf("audit all: %v", err)
	}
	return events
}

// isSilentError reports whether err is a silentError carrying ExitMerge —
// the typed-method equivalent of checking a cobra-mapped process exit code.
func isSilentError(err error) bool {
	var se silentError
	return errors.As(err, &se) && se.code == ExitMerge
}

// runBRN runs the fixed test bead "baron-a1b2c3" through the full StartRun
// pipeline and maps the returned error to the same exit-code contract the
// old cobra `run <bead>` command mapped it to — the typed-method equivalent
// of exitCode(t, a, "run", brn).
func runBRN(t *testing.T, a *app) int {
	t.Helper()
	return mapExitCode(a.StartRun(context.Background(), domain.BRN("baron-a1b2c3")))
}

// runResumeBRN is runBRN's --resume equivalent.
func runResumeBRN(t *testing.T, a *app, brn string) int {
	t.Helper()
	return mapExitCode(a.ResumeRun(context.Background(), domain.BRN(brn)))
}

// runIdleResumeBRN is runResumeBRN's idle-triggered equivalent — see
// IdleResumeRun.
func runIdleResumeBRN(t *testing.T, a *app, brn string) int {
	t.Helper()
	return mapExitCode(a.IdleResumeRun(context.Background(), domain.BRN(brn)))
}

// closeBead closes brn via the typed CloseBead method, confirm always
// approved — the typed-method equivalent of `work close --yes <brn>`.
func closeBead(t *testing.T, a *app, brn string) error {
	t.Helper()
	_, _, err := a.CloseBead(context.Background(), domain.BRN(brn), alwaysApprove)
	return err
}

// closeBeadCode is closeBead mapped to the old exit-code contract.
func closeBeadCode(t *testing.T, a *app, brn string) int {
	t.Helper()
	return mapExitCode(closeBead(t, a, brn))
}

// mapExitCode maps a typed method's returned error to the exit-code
// contract the old cobra commands mapped it to.
func mapExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var se silentError
	if errors.As(err, &se) {
		return se.code
	}
	return ExitError
}

// hasCall reports whether any recorded invocation starts with want.
func hasCall(f *fakeRunner, want ...string) bool {
	for _, call := range f.calls {
		if len(call) >= len(want) {
			equal := true
			for i := range want {
				if call[i] != want[i] {
					equal = false
					break
				}
			}
			if equal {
				return true
			}
		}
	}
	return false
}

func TestInitAudits(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	if _, _, err := a.Init(context.Background(), alwaysApprove, a.confirmAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}
	events := auditEvents(t, a)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Action != "init" || e.Target != a.dir || e.Actor.Type != store.ActorUser {
		t.Fatalf("event = %+v, want user init on %s", e, a.dir)
	}
}

func TestAuditFailureWarnsButSucceeds(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	block := filepath.Join(a.dir, "block")
	if err := os.WriteFile(block, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.audit = store.NewAuditStore(filepath.Join(block, "events.jsonl"))
	if _, err := a.AddComment(context.Background(), "baron-a1b2c3", "note"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if !strings.Contains(stderr(t, a), "audit") {
		t.Fatalf("stderr = %q, want audit warning", stderr(t, a))
	}
}

// TestHeaderStatsCountsPanesAndPorcelain: headerStats counts deleted and
// untracked porcelain lines separately, counts only BARON-launched agent
// panes (sentinel-marked), and dedupes panes listed by grouped
// baron-view-* sessions.
func TestHeaderStatsCountsPanesAndPorcelain(t *testing.T) {
	a := newTestApp(t, &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		switch {
		case name == "git" && args[0] == "rev-parse":
			return tool.Result{Stdout: "main\n"}, nil
		case name == "git" && args[0] == "status":
			return tool.Result{Stdout: " M internal/a.go\n D internal/b.go\nD  internal/c.go\n?? newfile.go\n"}, nil
		case name == "git" && args[0] == "diff":
			return tool.Result{Stdout: " 2 files changed, 5 insertions(+), 1 deletion(-)\n"}, nil
		case name == "tmux":
			return tool.Result{Stdout: "%1\tsh -c \"'claude' '--model' 'haiku' '--effort' 'low'; ec=$?; echo baron-run-exit=$ec; exec ${SHELL:-/bin/sh}\"\tzsh\n%2\tsh -c \"'opencode' 'run' 'work'; ec=$?; echo baron-run-exit=$ec; exec ${SHELL:-/bin/sh}\"\tzsh\n%3\t\tzsh\n%1\tdup-of-pane-1\n"}, nil
		}
		return tool.Result{}, errors.New("unexpected: " + name + " " + strings.Join(args, " "))
	}})

	stats, err := a.headerStats(context.Background(), "")
	if err != nil {
		t.Fatalf("headerStats: %v", err)
	}
	if stats.Branch != "main" {
		t.Errorf("Branch = %q, want main", stats.Branch)
	}
	if stats.ChangedFiles != 4 {
		t.Errorf("ChangedFiles = %d, want 4", stats.ChangedFiles)
	}
	if stats.DeletedFiles != 2 {
		t.Errorf("DeletedFiles = %d, want 2", stats.DeletedFiles)
	}
	if stats.UntrackedFiles != 1 {
		t.Errorf("UntrackedFiles = %d, want 1", stats.UntrackedFiles)
	}
	if stats.DiffInserted != 5 || stats.DiffDeleted != 1 {
		t.Errorf("DiffInserted/DiffDeleted = %d/%d, want 5/1", stats.DiffInserted, stats.DiffDeleted)
	}
	if stats.Agents != 2 {
		t.Errorf("Agents = %d, want 2 (bare zsh pane %q excluded, duplicated pane %q deduped)", stats.Agents, "%3", "%1")
	}
}

// dirRecordingRunner is a tool.Runner that records the Dir each git/tmux
// call ran in, so tests can assert whether headerStats scoped to a bead's
// worktree or fell back to the main checkout.
type dirRecordingRunner struct {
	dirs []string
}

func (r *dirRecordingRunner) Run(_ context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	r.dirs = append(r.dirs, opts.Dir)
	switch {
	case name == "git" && len(args) > 0 && args[0] == "rev-parse":
		return tool.Result{Stdout: "feature/x\n"}, nil
	case name == "git" && len(args) > 0 && (args[0] == "status" || args[0] == "diff"):
		return tool.Result{}, nil
	case name == "tmux":
		return tool.Result{}, nil
	}
	return tool.Result{}, errors.New("unexpected: " + name + " " + strings.Join(args, " "))
}

// TestHeaderStatsScopesToSelectedBeadWorktree: a brn whose worktree exists
// on disk scopes the branch/status/diff git calls to that worktree, not the
// main checkout — the header can then show the selected bead's own branch
// instead of the project's. A brn with no worktree yet (never assigned)
// falls back to the main checkout, ordinarily "main".
func TestHeaderStatsScopesToSelectedBeadWorktree(t *testing.T) {
	runner := &dirRecordingRunner{}
	a := newTestApp(t, runner)

	// No worktree on disk for this bead yet: every git call must run in the
	// main checkout (a.dir), the "main fallback".
	if _, err := a.headerStats(context.Background(), "baron-1"); err != nil {
		t.Fatalf("headerStats: %v", err)
	}
	for _, d := range runner.dirs {
		if d != a.dir {
			t.Errorf("dir = %q, want the main checkout %q (no worktree yet)", d, a.dir)
		}
	}

	// Create the bead's worktree on disk; now the git calls must run there.
	runner.dirs = nil
	wtDir := a.worktrees.Path(a.idOf(domain.BRN("baron-1")))
	if err := os.MkdirAll(wtDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, err := a.headerStats(context.Background(), "baron-1"); err != nil {
		t.Fatalf("headerStats: %v", err)
	}
	sawWorktree := false
	for _, d := range runner.dirs {
		if d == wtDir {
			sawWorktree = true
		}
	}
	if !sawWorktree {
		t.Errorf("dirs = %v, want at least one git call scoped to the worktree %q", runner.dirs, wtDir)
	}
}
