package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// TestRunStartsEmbeddedTerminal: 'r' on an assigned bead must start the
// embedded agent terminal — the right pane's Agent tab is focused, and the
// returned batch carries the spawn (not a legacy `run` command or a tmux
// capture). A failed spawn surfaces as a status notice, never a crash.
func TestRunStartsEmbeddedTerminal(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen, Assignee: "opencode"}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("r"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want to stay on the dashboard", m.screen)
	}
	if m.detailTab != 1 {
		t.Fatalf("detailTab = %d, want 1 (right pane Agent tab focused)", m.detailTab)
	}
	if m.liveBRN != "baron-a" {
		t.Fatalf("liveBRN = %q, want baron-a", m.liveBRN)
	}
	if cmd == nil {
		t.Fatal("expected the spawn batch after r")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", cmd())
	}
	var gotSpawn bool
	for _, c := range batch {
		switch msg := c().(type) {
		case agentSpawnedMsg:
			gotSpawn = true
		case commandRanMsg:
			t.Fatalf("legacy run command fired: %v", msg)
		}
	}
	if !gotSpawn {
		t.Fatal("batch = no spawn, want the embedded terminal spawn")
	}

	// A failed spawn (no worktree/store in the test env) is a friendly
	// notice, not a raw error.
	next, _ = m.Update(agentSpawnedMsg{brn: "baron-a", err: errors.New("worktree for baron-a: boom")})
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "agent failed to start") {
		t.Errorf("statusMsg = %q, want the friendly failed-to-start notice", m.statusMsg)
	}
}

// TestRunNotifiesSpawnSuccess: when the embedded spawn resolves cleanly a
// confirmation notice shows ("it ran"); the failure notice above already
// carries the reason when it didn't.
func TestRunNotifiesSpawnSuccess(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen, Assignee: "opencode"}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, _ = m.Update(agentSpawnedMsg{brn: "baron-a", kind: kindAgent, t: &agentTerminal{brn: "baron-a"}})
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "agent for baron-a is running") {
		t.Errorf("statusMsg = %q, want the running notice", m.statusMsg)
	}
}

// TestRunOnLiveSessionNotifiesAlreadyRunning: 'r' on a bead whose agent is
// already live re-shows the terminal and says so instead of spawning a
// second process.
func TestRunOnLiveSessionNotifiesAlreadyRunning(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen, Assignee: "opencode"}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", emu: vt.NewSafeEmulator(10, 5)}
	next, cmd := m.Update(key("r"))
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "already running") {
		t.Errorf("statusMsg = %q, want the already-running notice", m.statusMsg)
	}
	if cmd == nil {
		t.Fatal("expected the re-show batch")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", cmd())
	}
	for _, c := range batch {
		if _, ok := c().(agentSpawnedMsg); ok {
			t.Fatal("batch spawned a second agent, want a re-show only")
		}
	}
}

// TestBeadStatusChangeNotifies: a beads reload that sees a bead move through
// the run pipeline raises a watch notice even when no key was pressed; the
// first load has no snapshot and stays quiet.
func TestBeadStatusChangeNotifies(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusWorking}}})
	m = asModel(next)
	if m.statusMsg != "" {
		t.Fatalf("statusMsg = %q, want none on the first load", m.statusMsg)
	}

	next, _ = m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusMergable}}})
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "baron-a → mergable") {
		t.Errorf("statusMsg = %q, want the status-change notice", m.statusMsg)
	}

	// Same status again: the notice must not re-fire.
	next, _ = m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusMergable}}})
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "baron-a → mergable") {
		t.Errorf("statusMsg = %q, want unchanged after a no-op reload", m.statusMsg)
	}

	// A user-driven status (closed) is outside the run pipeline: no notice.
	next, _ = m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusClosed}}})
	m = asModel(next)
	if strings.Contains(m.statusMsg, "closed") {
		t.Errorf("statusMsg = %q, want no notice for a user-driven status", m.statusMsg)
	}
}

// TestRunCompletionKeepsRunSummary: once an embedded session exits, the tick
// stops re-arming (no frame pump for a dead terminal) and reselecting the
// bead does not restart it. The final emulator frame stays visible.
func TestRunCompletionKeepsRunSummary(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen, Assignee: "opencode"}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	// A live session keeps the frame tick alive.
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", emu: vt.NewSafeEmulator(10, 5)}
	m.detailTab = 1
	m.detail = beads[0]
	next, cmd := m.Update(tickMsg{})
	m = asModel(next)
	if cmd == nil {
		t.Fatal("tick while a session is live must re-arm")
	}
	if _, ok := cmd().(tickMsg); !ok {
		t.Fatalf("tick cmd() = %T, want tickMsg", cmd())
	}

	// The session exits: the tick must stop.
	m.sessions["baron-a"].done = true
	next, cmd = m.Update(tickMsg{})
	m = asModel(next)
	if cmd != nil {
		t.Fatalf("tick after completion returned a cmd %v, want none", cmd)
	}
	next, cmd = m.Update(agentExitMsg{brn: "baron-a", kind: kindAgent})
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "exited") {
		t.Errorf("statusMsg = %q, want the exited notice", m.statusMsg)
	}
	if cmd == nil {
		t.Fatal("agentExitMsg must return a cmd batch (persist + resume)")
	}
}

// TestAgentExitRunsResume: the embedded terminal never runs a gate itself —
// the bead is still "working" the instant its process exits regardless of
// what happened inside the session, so agentExitMsg must fire `run --resume
// <brn>` (the post-agent ask/validate/gate/ready-for-merge pipeline, sans relaunch) to
// actually resolve it out of "working".
func TestAgentExitRunsResume(t *testing.T) {
	resumed := make(chan string, 1)
	d := testDeps()
	d.Resume = func(brn string) (string, error) {
		resumed <- brn
		return "", nil
	}
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", emu: vt.NewSafeEmulator(10, 5), done: true}

	_, cmd := m.Update(agentExitMsg{brn: "baron-a"})
	if cmd == nil {
		t.Fatal("agentExitMsg must return a cmd batch")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", cmd())
	}
	// Fire concurrently and wait on the channel: the batch also includes
	// statusDismissCmd (a 4s tea.Tick) alongside the resume command this
	// test cares about, and waiting on every command serially would ride
	// out that whole timer. A channel (not a plain var polled from the main
	// goroutine) is what actually makes the wait race-free — the write
	// happens on whichever goroutine runs the resume cmd.
	for _, c := range batch {
		go c()
	}
	select {
	case brn := <-resumed:
		if brn != "baron-a" {
			t.Errorf("Resume called with %q, want \"baron-a\"", brn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Resume was never called")
	}
}

// TestReconcileMsgMovedTriggersReloadAndToast: a Reconcile pass that
// actually changed a bead's status must both surface a toast and reload
// the board, so it doesn't keep showing stale data until some unrelated
// reload happens to come along.
func TestReconcileMsgMovedTriggersReloadAndToast(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, cmd := m.Update(reconcileMsg{summary: ReconcileSummary{Actions: []ReconcileActionSummary{
		{BRN: "baron-a", From: domain.BeadStateWorking, To: domain.BeadStateRetry, Reason: "agent process is gone"},
	}}})
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "reconciled 1 bead") {
		t.Errorf("statusMsg = %q, want it to mention the reconciled bead", m.statusMsg)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the notify+reload batch")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatalf("cmd() = %#v (%T), want a batch of at least 2 (notify dismiss + reload)", msg, msg)
	}
}

// TestReconcileMsgNeedsAttentionOnlyNoReload: a "retry" bead reported as
// needing a manual relaunch (gate.auto_retry disabled, nothing actually
// changed) gets a toast but no board reload — there's nothing new to show.
func TestReconcileMsgNeedsAttentionOnlyNoReload(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, cmd := m.Update(reconcileMsg{summary: ReconcileSummary{Actions: []ReconcileActionSummary{
		{BRN: "baron-a", From: domain.BeadStateRetry, To: domain.BeadStateRetry, Reason: "needs relaunch (gate.auto_retry is disabled)"},
	}}})
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "need attention") {
		t.Errorf("statusMsg = %q, want it to mention needing attention", m.statusMsg)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want at least the notify-dismiss command")
	}
	if _, isBatch := cmd().(tea.BatchMsg); isBatch {
		t.Error("cmd() is a batch, want a single notify command (no reload — nothing changed)")
	}
}

// TestReconcileMsgErrShowsToast: a failed pass surfaces as the same red
// toast every other error path uses (m.notifyErr) — never a raw dump to the
// screen, and never silent either: a persistently broken git/tmux/bd call
// deserves visibility, rate-limited to once per reconcileCooldown (90s) by
// the pass itself, not once a frame.
func TestReconcileMsgErrShowsToast(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, cmd := m.Update(reconcileMsg{err: errors.New("boom")})
	m = asModel(next)
	if m.err == nil || !strings.Contains(m.err.Error(), "boom") {
		t.Errorf("err = %v, want it to mention the reconcile error (this is notifyErr, not notify)", m.err)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the notify-dismiss command")
	}
}

// TestReconcileMsgEmptyIsSilent: no actions produces no toast — there is
// nothing to say when a pass genuinely found nothing to reconcile.
func TestReconcileMsgEmptyIsSilent(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, cmd := m.Update(reconcileMsg{summary: ReconcileSummary{}})
	m = asModel(next)
	if m.statusMsg != "" || cmd != nil {
		t.Errorf("statusMsg = %q, cmd = %v, want both empty/nil when nothing needed reconciling", m.statusMsg, cmd)
	}
}

// TestBeadsLoadedRestoresRunSummary: on startup the persisted run summaries
// hydrate each bead's pane so the gate result survives a restart.
func TestBeadsLoadedRestoresRunSummary(t *testing.T) {
	d := testDeps()
	d.ReadRunSummary = func(brn string) ([]string, error) {
		if brn != "baron-a" {
			return nil, nil
		}
		return []string{"ran in 5s", "gate passed (956ms)"}, nil
	}
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	p := m.panes["baron-a"]
	if p == nil || !p.done {
		t.Fatalf("pane after restore = %+v, want done with the persisted summary", p)
	}
	if !slices.Contains(p.summary, "gate passed (956ms)") {
		t.Errorf("pane.summary = %v, want the restored gate result", p.summary)
	}
	m.width, m.height = 120, 36
	m.detailTab = 1
	if v := m.View().Content; !strings.Contains(v, "gate passed (956ms)") {
		t.Errorf("View() = %q, want the restored gate result visible", v)
	}
}

// TestAgentTabFollowsSelectionAcrossBeads: switching the list cursor to a
// different bead must update the Agent tab to that bead's own persisted
// summary — previously liveBRN (what the persisted-summary fallback
// actually reads, see view.go) was only set on initial load and on 'r',
// never on plain j/k/mouse selection, so the pane kept showing whichever
// bead was last run instead of the one now selected.
func TestAgentTabFollowsSelectionAcrossBeads(t *testing.T) {
	d := testDeps()
	d.ReadRunSummary = func(brn string) ([]string, error) {
		switch brn {
		case "baron-a":
			return []string{"alpha gate passed"}, nil
		case "baron-b":
			return []string{"beta gate failed"}, nil
		}
		return nil, nil
	}
	m := New(context.Background(), d)
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "Beta", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	m.detailTab = 1

	if v := m.View().Content; !strings.Contains(v, "alpha gate passed") {
		t.Fatalf("View() = %q, want baron-a's own summary while it's selected", v)
	}

	next, _ = m.Update(key("j"))
	m = asModel(next)
	if m.liveBRN != "baron-b" {
		t.Fatalf("liveBRN = %q, want baron-b after selection moved to it", m.liveBRN)
	}
	if v := m.View().Content; !strings.Contains(v, "beta gate failed") || strings.Contains(v, "alpha gate passed") {
		t.Errorf("View() = %q, want baron-b's own summary, not baron-a's stale one", v)
	}
}

// TestBeadsLoadedDoesNotHijackLivePane: a pane already created this session
// (a run in flight, polling the window) must never be replaced by the
// persisted summary on a later beads reload.
func TestBeadsLoadedDoesNotHijackLivePane(t *testing.T) {
	d := testDeps()
	d.ReadRunSummary = func(brn string) ([]string, error) {
		return []string{"stale summary"}, nil
	}
	m := New(context.Background(), d)
	m.panes["baron-a"] = &agentPane{}
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	if p := m.panes["baron-a"]; p.done || len(p.summary) != 0 {
		t.Errorf("pane = %+v, want the in-flight pane untouched by the restore", p)
	}
}
