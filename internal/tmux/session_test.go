package tmux

import (
	"context"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

func TestClient_Ensure_createsSessionWhenMissing(t *testing.T) {
	s := newStub(
		fail("can't find session: baron"),
		ok(""),
	)
	c := New(s)

	if err := c.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wantCalls(t, s.calls, [][]string{
		{"has-session", "-t", "baron"},
		{"new-session", "-d", "-s", "baron"},
	})
}

func TestClient_Ensure_noopWhenSessionExists(t *testing.T) {
	s := newStub(ok(""))
	c := New(s)

	if err := c.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wantCalls(t, s.calls, [][]string{
		{"has-session", "-t", "baron"},
	})
}

func TestClient_Ensure_propagatesNewSessionError(t *testing.T) {
	s := newStub(
		fail("can't find session: baron"),
		fail("server error"),
	)
	c := New(s)

	err := c.Ensure(context.Background())
	wantErrContains(t, err, "tmux new-session: exit status 1")
}

func TestClient_Spawn_killsExistingAndCreatesWindow(t *testing.T) {
	s := newStub(
		ok(""),
		ok(""),
	)
	c := New(s)

	if err := c.Spawn(context.Background(), "BRN-001", "claude", "/work", "--resume", "BRN-001"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	wantCalls(t, s.calls, [][]string{
		{"kill-window", "-t", "baron:BRN-001"},
		{"new-window", "-t", "baron:", "-n", "BRN-001", "-c", "/work", "--", "claude", "--resume", "BRN-001"},
	})
}

func TestClient_Spawn_toleratesMissingWindow(t *testing.T) {
	s := newStub(
		fail("can't find window: baron:BRN-001"),
		ok(""),
	)
	c := New(s)

	if err := c.Spawn(context.Background(), "BRN-001", "codex", "", "exec"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	wantCalls(t, s.calls, [][]string{
		{"kill-window", "-t", "baron:BRN-001"},
		{"new-window", "-t", "baron:", "-n", "BRN-001", "--", "codex", "exec"},
	})
}

func TestClient_Spawn_propagatesNewWindowError(t *testing.T) {
	s := newStub(
		ok(""),
		fail("server error"),
	)
	c := New(s)

	err := c.Spawn(context.Background(), "BRN-001", "claude", "")
	wantErrContains(t, err, "tmux new-window: server error")
}

func TestClient_KillWindow_toleratesNotFound(t *testing.T) {
	s := newStub(fail("can't find window: baron:BRN-001"))
	c := New(s)

	if err := c.KillWindow(context.Background(), "BRN-001"); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	wantCalls(t, s.calls, [][]string{
		{"kill-window", "-t", "baron:BRN-001"},
	})
}

func TestClient_KillWindow_propagatesRealError(t *testing.T) {
	s := newStub(fail("server error"))
	c := New(s)

	err := c.KillWindow(context.Background(), "BRN-001")
	wantErrContains(t, err, "tmux kill-window: exit status 1")
}

func TestClient_WindowExists_true(t *testing.T) {
	s := newStub(ok(""))
	c := New(s)

	got, err := c.WindowExists(context.Background(), "BRN-001")
	if err != nil {
		t.Fatalf("WindowExists: %v", err)
	}
	if !got {
		t.Fatal("WindowExists: expected true")
	}
	// has-session, not display-message: verified against real tmux that
	// display-message -t session:bogus-window silently falls back to the
	// session's current window instead of erroring — see WindowExists' doc
	// comment.
	wantCalls(t, s.calls, [][]string{
		{"has-session", "-t", "baron:BRN-001"},
	})
}

func TestClient_WindowExists_falseWhenMissing(t *testing.T) {
	s := newStub(fail("can't find window: baron:BRN-001"))
	c := New(s)

	got, err := c.WindowExists(context.Background(), "BRN-001")
	if err != nil {
		t.Fatalf("WindowExists: %v", err)
	}
	if got {
		t.Fatal("WindowExists: expected false")
	}
}

func TestClient_WindowExists_propagatesRealError(t *testing.T) {
	s := newStub(fail("server error"))
	c := New(s)

	_, err := c.WindowExists(context.Background(), "BRN-001")
	wantErrContains(t, err, "tmux has-session: exit status 1")
}

func TestClient_Attach_targetsWindow(t *testing.T) {
	s := newStub(ok(""))
	c := New(s)

	if err := c.Attach(context.Background(), "BRN-001"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	wantCalls(t, s.calls, [][]string{
		{"attach", "-t", "baron:BRN-001"},
	})
}

func TestClient_Attach_propagatesError(t *testing.T) {
	s := newStub(fail("server error"))
	c := New(s)

	err := c.Attach(context.Background(), "BRN-001")
	wantErrContains(t, err, "tmux attach: exit status 1")
}

func TestClient_EnsureRunning_spawnsWhenNoWindow(t *testing.T) {
	s := newStub(
		ok(""),                                   // has-session (baron)
		fail("can't find window: baron:BRN-001"), // has-session (WindowExists)
		ok(""),                                   // kill-window (Spawn's pre-kill)
		ok(""),                                   // new-window
	)
	c := New(s)

	started, err := c.EnsureRunning(context.Background(), "BRN-001", "claude 'hi'; exec $SHELL", "/work")
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Error("started = false, want true (fresh window)")
	}
	wantCalls(t, s.calls, [][]string{
		{"has-session", "-t", "baron"},
		{"has-session", "-t", "baron:BRN-001"},
		{"kill-window", "-t", "baron:BRN-001"},
		{"new-window", "-t", "baron:", "-n", "BRN-001", "-c", "/work", "--", "sh", "-c", "claude 'hi'; exec $SHELL"},
	})
}

func TestClient_EnsureRunning_noopWhenAlreadyRunning(t *testing.T) {
	s := newStub(
		ok(""), // has-session (baron)
		ok(""), // has-session (WindowExists) -> exists
	)
	c := New(s)

	started, err := c.EnsureRunning(context.Background(), "BRN-001", "claude 'hi'; exec $SHELL", "/work")
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if started {
		t.Error("started = true, want false (already running — must not kill/respawn)")
	}
	wantCalls(t, s.calls, [][]string{
		{"has-session", "-t", "baron"},
		{"has-session", "-t", "baron:BRN-001"},
	})
}

func TestClient_EnsureViewer_createsAndConfiguresWhenMissing(t *testing.T) {
	s := newStub(
		fail("can't find session: baron-view-w1"), // has-session
		ok(""), // new-session
		ok(""), // set-option status
		ok(""), // set-option prefix
		ok(""), // select-window
	)
	c := New(s)

	if err := c.EnsureViewer(context.Background(), "baron-view-w1", "BRN-001"); err != nil {
		t.Fatalf("EnsureViewer: %v", err)
	}
	wantCalls(t, s.calls, [][]string{
		{"has-session", "-t", "baron-view-w1"},
		{"new-session", "-d", "-t", "baron", "-s", "baron-view-w1"},
		{"set-option", "-t", "baron-view-w1", "status", "off"},
		{"set-option", "-t", "baron-view-w1", "prefix", "None"},
		{"select-window", "-t", "baron-view-w1:BRN-001"},
	})
}

func TestClient_EnsureViewer_reselectsWindowWhenAlreadyExists(t *testing.T) {
	s := newStub(
		ok(""), // has-session -> exists
		ok(""), // select-window
	)
	c := New(s)

	if err := c.EnsureViewer(context.Background(), "baron-view-w1", "BRN-002"); err != nil {
		t.Fatalf("EnsureViewer: %v", err)
	}
	wantCalls(t, s.calls, [][]string{
		{"has-session", "-t", "baron-view-w1"},
		{"select-window", "-t", "baron-view-w1:BRN-002"},
	})
}

func TestAttachArgv(t *testing.T) {
	got := AttachArgv("baron-view-w1")
	want := []string{"tmux", "attach-session", "-t", "baron-view-w1"}
	if len(got) != len(want) {
		t.Fatalf("AttachArgv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AttachArgv = %v, want %v", got, want)
		}
	}
}

func TestClient_KillViewer_toleratesNotFound(t *testing.T) {
	s := newStub(fail("can't find session: baron-view-w1"))
	c := New(s)

	if err := c.KillViewer(context.Background(), "baron-view-w1"); err != nil {
		t.Fatalf("KillViewer: %v", err)
	}
	wantCalls(t, s.calls, [][]string{
		{"kill-session", "-t", "baron-view-w1"},
	})
}

func TestClient_KillViewer_propagatesRealError(t *testing.T) {
	s := newStub(fail("server error"))
	c := New(s)

	err := c.KillViewer(context.Background(), "baron-view-w1")
	wantErrContains(t, err, "tmux kill-session (viewer): exit status 1")
}

var _ tool.Runner = (*stubRunner)(nil)
