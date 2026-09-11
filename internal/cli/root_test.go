package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tui"
)

// TestRunRequiresTTY: baron has no headless/scriptable mode any more (see
// baron-09q) — a non-interactive invocation must fail loudly rather than
// silently doing nothing or hanging waiting for a terminal that isn't there.
func TestRunRequiresTTY(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	if code := a.run("test", "abc123", "now"); code != ExitError {
		t.Fatalf("exit = %d, want %d", code, ExitError)
	}
	if !strings.Contains(stderr(t, a), "interactive terminal") {
		t.Fatalf("stderr = %q, want an interactive-terminal message", stderr(t, a))
	}
}

// TestRunTTYLaunchesTUI: bare `baron` in a TTY must draw the TUI directly
// (the cockpit rework: no tmux sidecar, no re-entrant second copy to stand
// in for).
func TestRunTTYLaunchesTUI(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	a.inTTY = func() bool { return true }
	a.in = strings.NewReader("n\n")
	launched := false
	a.tuiRun = func(context.Context, tui.Deps, io.Reader, io.Writer) error {
		launched = true
		return nil
	}
	if code := a.run("test", "abc123", "now"); code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, ExitOK, stderr(t, a))
	}
	if !launched {
		t.Fatal("bare `baron` in a TTY must launch the TUI")
	}
}

// TestRunTTYAutoInits: bare `baron` in a TTY against a project with no
// .beads workspace yet must initialize it automatically before drawing the
// TUI — no separate init step required first.
func TestRunTTYAutoInits(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	a.inTTY = func() bool { return true }
	a.in = strings.NewReader("n\n")
	launched := false
	a.tuiRun = func(context.Context, tui.Deps, io.Reader, io.Writer) error {
		launched = true
		return nil
	}
	if code := a.run("test", "abc123", "now"); code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, ExitOK, stderr(t, a))
	}
	if !hasCall(fr, "init", "--quiet") {
		t.Fatalf("calls = %v, want bd init --quiet (auto-init before the TUI launches)", fr.calls)
	}
	if !launched {
		t.Fatal("TUI was not launched after auto-init")
	}
}

// TestRunTTYAutoInitAsksAutoMergeForReal: the one first-run question BARON
// always asks for real (never auto-approved) is the auto-merge policy —
// answering "y" on the huh confirm must actually enable it in
// .baron/config.toml.
func TestRunTTYAutoInitAsksAutoMergeForReal(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	a.inTTY = func() bool { return true }
	a.in = strings.NewReader("y\n")
	a.tuiRun = func(context.Context, tui.Deps, io.Reader, io.Writer) error { return nil }
	if code := a.run("test", "abc123", "now"); code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, ExitOK, stderr(t, a))
	}
	cfg, err := store.LoadConfig(a.dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Merge.Auto.Enabled {
		t.Error("merge.auto.enabled = false, want the real answer (\"y\") to have been honored")
	}
}

// TestRunTTYAutoInitDeclinedAutoMergeStaysOff: answering "n" (or letting it
// default) must leave auto-merge off — no surprise policy change from a
// prompt someone declined.
func TestRunTTYAutoInitDeclinedAutoMergeStaysOff(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	a.inTTY = func() bool { return true }
	a.in = strings.NewReader("n\n")
	a.tuiRun = func(context.Context, tui.Deps, io.Reader, io.Writer) error { return nil }
	if code := a.run("test", "abc123", "now"); code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, ExitOK, stderr(t, a))
	}
	cfg, err := store.LoadConfig(a.dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Merge.Auto.Enabled {
		t.Error("merge.auto.enabled = true, want it left off after declining")
	}
}
