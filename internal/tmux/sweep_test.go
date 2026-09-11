package tmux

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// sweepRunner answers list-sessions with a fixed roster and records every
// kill-session it is asked to perform.
type sweepRunner struct {
	sessions string
	listErr  error
	listRes  tool.Result
	killed   []string
}

func (r *sweepRunner) Run(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
	switch args[0] {
	case "list-sessions":
		if r.listErr != nil {
			return r.listRes, r.listErr
		}
		return tool.Result{Stdout: r.sessions}, nil
	case "kill-session":
		r.killed = append(r.killed, args[2])
		return tool.Result{}, nil
	}
	return tool.Result{}, nil
}

// TestSweepViewersRemovesOnlyDeadOwners is the regression test for the viewer
// leak that filled a real machine with 280 orphaned sessions: a viewer whose
// BARON process is gone must be reaped, and one whose process is still running
// must be left strictly alone — it is another live TUI's window into the same
// session, and killing it would blank that instance's terminal pane.
func TestSweepViewersRemovesOnlyDeadOwners(t *testing.T) {
	r := &sweepRunner{sessions: "baron\n" +
		ViewerPrefix + "baron-abc-111\n" + // dead owner: sweep
		ViewerPrefix + "baron-abc-222\n" + // live owner: keep
		ViewerPrefix + "baron-4al-1-333\n" + // dead owner, dashed window: sweep
		"some-unrelated-session\n"}
	c := &Client{Runner: r, Session: "baron"}

	alive := func(pid int) bool { return pid == 222 }
	n, err := c.SweepViewers(context.Background(), alive)
	if err != nil {
		t.Fatalf("SweepViewers: %v", err)
	}
	if n != 2 {
		t.Errorf("swept %d viewers, want 2", n)
	}
	want := []string{ViewerPrefix + "baron-abc-111", ViewerPrefix + "baron-4al-1-333"}
	if !slices.Equal(r.killed, want) {
		t.Errorf("killed %v, want %v", r.killed, want)
	}
}

// TestSweepViewersIgnoresNonViewerSessions: the shared agent session itself
// must never be a sweep target, whatever else is on the server.
func TestSweepViewersIgnoresNonViewerSessions(t *testing.T) {
	r := &sweepRunner{sessions: "baron\nwork\nbaron-view-malformed\n" + ViewerPrefix + "w-notapid\n"}
	c := &Client{Runner: r, Session: "baron"}

	n, err := c.SweepViewers(context.Background(), func(int) bool { return false })
	if err != nil {
		t.Fatalf("SweepViewers: %v", err)
	}
	if n != 0 || len(r.killed) != 0 {
		t.Errorf("swept %d (%v), want none — no session here has a pid suffix", n, r.killed)
	}
}

// TestSweepViewersNoServerIsNotAnError: sweeping at startup happens before
// anything guarantees a tmux server exists, so "no server running" is the
// normal empty case and must not fail the TUI's launch.
func TestSweepViewersNoServerIsNotAnError(t *testing.T) {
	r := &sweepRunner{
		listErr: errors.New("exit status 1"),
		listRes: tool.Result{Stderr: "no server running on /tmp/tmux-501/default"},
	}
	c := &Client{Runner: r, Session: "baron"}

	n, err := c.SweepViewers(context.Background(), func(int) bool { return false })
	if err != nil {
		t.Fatalf("SweepViewers with no server: %v", err)
	}
	if n != 0 {
		t.Errorf("swept %d, want 0", n)
	}
}

func TestViewerPID(t *testing.T) {
	cases := []struct {
		name string
		pid  int
		ok   bool
	}{
		{ViewerPrefix + "baron-abc-4242", 4242, true},
		{ViewerPrefix + "baron-4al-1-7", 7, true},
		{ViewerPrefix + "baron-abc-", 0, false},
		{ViewerPrefix + "baron-abc-x9", 0, false},
		{ViewerPrefix + "baron-abc-0", 0, false},
		{"noseparators", 0, false},
	}
	for _, c := range cases {
		pid, ok := viewerPID(c.name)
		if ok != c.ok || pid != c.pid {
			t.Errorf("viewerPID(%q) = %d,%v; want %d,%v", c.name, pid, ok, c.pid, c.ok)
		}
	}
}

// viewerRunner scripts has-session / select-window outcomes so EnsureViewer's
// stale-viewer recovery can be driven without a tmux server.
type viewerRunner struct {
	exists       bool // has-session succeeds
	selectFailsN int  // this many select-window calls fail before one succeeds
	calls        []string
}

func (r *viewerRunner) Run(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
	r.calls = append(r.calls, args[0])
	switch args[0] {
	case "has-session":
		if r.exists {
			return tool.Result{}, nil
		}
		return tool.Result{Stderr: "can't find session"}, errors.New("exit status 1")
	case "select-window":
		if r.selectFailsN > 0 {
			r.selectFailsN--
			return tool.Result{Stderr: "can't find window"}, errors.New("exit status 1")
		}
		return tool.Result{}, nil
	}
	return tool.Result{}, nil
}

// TestEnsureViewerRebuildsAStaleViewer: a viewer session left behind by a
// process that died mid-run stays discoverable by has-session but is grouped
// with a session that no longer exists, so select-window fails. Reusing it is
// what makes the breakage permanent — has-session keeps succeeding, and every
// later attach to that window fails the same way with no way out.
func TestEnsureViewerRebuildsAStaleViewer(t *testing.T) {
	r := &viewerRunner{exists: true, selectFailsN: 1}
	c := &Client{Runner: r, Session: "baron"}

	if err := c.EnsureViewer(context.Background(), ViewerPrefix+"w-1", "w"); err != nil {
		t.Fatalf("EnsureViewer did not recover from a stale viewer: %v", err)
	}
	if !slices.Contains(r.calls, "kill-session") {
		t.Errorf("stale viewer was never torn down; calls: %v", r.calls)
	}
	if !slices.Contains(r.calls, "new-session") {
		t.Errorf("stale viewer was never rebuilt; calls: %v", r.calls)
	}
}

// TestEnsureViewerReusesAWorkingViewer: recovery must not fire on the happy
// path — rebuilding a healthy viewer would tear down the pty another attach is
// already streaming through.
func TestEnsureViewerReusesAWorkingViewer(t *testing.T) {
	r := &viewerRunner{exists: true}
	c := &Client{Runner: r, Session: "baron"}

	if err := c.EnsureViewer(context.Background(), ViewerPrefix+"w-1", "w"); err != nil {
		t.Fatalf("EnsureViewer: %v", err)
	}
	if slices.Contains(r.calls, "kill-session") {
		t.Errorf("a healthy viewer was rebuilt anyway; calls: %v", r.calls)
	}
}

// TestEnsureViewerMissingWindowStillFails: a genuinely absent window must be
// reported, not papered over by an endless rebuild loop.
func TestEnsureViewerMissingWindowStillFails(t *testing.T) {
	r := &viewerRunner{exists: false, selectFailsN: 5}
	c := &Client{Runner: r, Session: "baron"}

	if err := c.EnsureViewer(context.Background(), ViewerPrefix+"w-1", "nope"); err == nil {
		t.Error("EnsureViewer reported success for a window that does not exist")
	}
}
