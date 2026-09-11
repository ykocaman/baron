package tui

import (
	"context"
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

func (f *fakeAgentHost) EnsureRunning(_ context.Context, window string, _ []string, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls = append(f.ensureCalls, window)
	return f.ensureStarted, f.ensureErr
}

func (f *fakeAgentHost) Alive(_ context.Context, window string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aliveChecked = append(f.aliveChecked, window)
	return f.aliveWindows[window], nil
}

func (f *fakeAgentHost) AttachArgv(_ context.Context, _ string) ([]string, error) {
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	if f.attachArgv != nil {
		return f.attachArgv, nil
	}
	return []string{"cat"}, nil
}

// CapturePane serves the scripted pane scrollback (see captured), recording
// each request so tests can assert on how often BARON re-reads it.
func (f *fakeAgentHost) CapturePane(_ context.Context, window string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captureCalls = append(f.captureCalls, window)
	return f.captured, f.captureErr
}

func (f *fakeAgentHost) Detach(_ context.Context, window string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detached = append(f.detached, window)
	return nil
}

func (f *fakeAgentHost) Kill(_ context.Context, window string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, window)
	return nil
}

// TestSelectingAnotherBeadKillsPreviousDiffSession is a regression test for
// a live incident: the Diff tab auto-spawning on open (tabCmd) combined
// with nothing ever cleaning up meant every bead a user so much as glanced
// at left a live hunk process running forever, accumulating without bound
// as they browsed — observed live as 230+ tmux sessions, 20+ live hunk
// processes, and the TUI's own render tick (which re-scans every
// registered session every ~42ms) pegging the CPU at 100%+, making list
// navigation itself feel frozen. Moving the list cursor to a different
// bead must kill the previously selected bead's diff session — its own
// tab reopening it fresh is cheap, unlike losing real agent work would be
// (which is why the Agent tab is deliberately NOT cleaned up this way).
func TestSelectingAnotherBeadKillsPreviousDiffSession(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	m.diffSessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindDiff, pty: w, emu: vt.NewSafeEmulator(10, 5)}

	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: 5}) // second row -> baron-b
	m = asModel(next)
	if m.detail.BRN != "baron-b" {
		t.Fatalf("detail = %+v, want baron-b selected", m.detail)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Error("baron-a's diff pty is still open after selecting a different bead, want it killed")
	}
}

// TestKillSessionKillsTmuxWindow: 'x' on the Terminal tab means "stop the
// agent" — for a tmux-backed session that must reach the underlying
// window's process, not just detach BARON's own local viewer pty.
func TestKillSessionKillsTmuxWindow(t *testing.T) {
	host := &fakeAgentHost{}
	d := testDeps()
	d.AgentHost = host
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.detail = beads[0]

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", pty: w, emu: vt.NewSafeEmulator(10, 5), tmuxWindow: "baron-a"}

	cmd := m.killSession(kindAgent)
	if cmd == nil {
		t.Fatal("killSession() = nil cmd, want the tmux Kill cmd")
	}
	cmd()
	if len(host.killed) != 1 || host.killed[0] != "baron-a" {
		t.Fatalf("host.killed = %v, want [baron-a]", host.killed)
	}
}

// TestKillSessionNoTmuxReturnsNilCmd: the raw-pty fallback (no AgentHost)
// has nothing to tell tmux — killSession must return a nil cmd, not panic
// on a nil AgentHost.
func TestKillSessionNoTmuxReturnsNilCmd(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.detail = beads[0]

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", pty: w, emu: vt.NewSafeEmulator(10, 5)}

	if cmd := m.killSession(kindAgent); cmd != nil {
		t.Fatalf("killSession() cmd = %v, want nil for a non-tmux session", cmd)
	}
}

// TestAgentDetachedMsgClearsSessionWithoutResume: only BARON's own viewer
// went away — the tmux window and its agent may still be running, so this
// must never trigger `run --resume` (that would race a possibly-still-live
// agent). It must still clean up the now-orphaned viewer session.
func TestAgentDetachedMsgClearsSessionWithoutResume(t *testing.T) {
	host := &fakeAgentHost{}
	resumeCalled := false
	d := testDeps()
	d.AgentHost = host
	d.Resume = func(brn string) (string, error) {
		resumeCalled = true
		return "", nil
	}
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", tmuxWindow: "baron-a", emu: vt.NewSafeEmulator(10, 5)}

	next, cmd := m.Update(agentDetachedMsg{brn: "baron-a"})
	m = asModel(next)
	if _, ok := m.sessions["baron-a"]; ok {
		t.Error("agentDetachedMsg must remove the session entry")
	}
	if cmd != nil {
		cmd()
	}
	if len(host.detached) != 1 || host.detached[0] != "baron-a" {
		t.Fatalf("host.detached = %v, want [baron-a]", host.detached)
	}
	if resumeCalled {
		t.Fatal("Resume was called, want it never called — a detach must not trigger --resume")
	}
}

// TestReconcileMsgGoneTriggersRetry: a "working" bead whose tmux window is
// genuinely gone has no agent running, so it must be transitioned working ->
// retry (re-runnable) via `work status`, never left stranded in "working".
func TestReconcileMsgGoneTriggersRetry(t *testing.T) {
	var gotBRN, gotTarget string
	d := testDeps()
	d.AgentHost = &fakeAgentHost{}
	d.ChangeStatus = func(brn, target string) (string, error) {
		gotBRN, gotTarget = brn, target
		return "", nil
	}
	m := New(context.Background(), d)

	_, cmd := m.Update(reconcileWorkingMsg{brn: "baron-a", alive: false})
	if cmd == nil {
		t.Fatal("reconcileWorkingMsg{alive:false} must return a cmd")
	}
	cmd()
	if gotBRN != "baron-a" || gotTarget != "retry" {
		t.Fatalf("ChangeStatus(brn, target) = (%q, %q), want (\"baron-a\", \"retry\")", gotBRN, gotTarget)
	}
}

// TestReconcileMsgAliveDoesNotTriggerResume: a "working" bead whose tmux
// window is still alive (just unattached — e.g. after a BARON restart)
// must be reattached, never resolved via --resume, since the agent may
// still be actively working.
func TestReconcileMsgAliveDoesNotTriggerResume(t *testing.T) {
	resumeCalled := false
	d := testDeps()
	d.AgentHost = &fakeAgentHost{}
	d.Resume = func(brn string) (string, error) {
		resumeCalled = true
		return "", nil
	}
	m := New(context.Background(), d)

	_, cmd := m.Update(reconcileWorkingMsg{brn: "baron-a", alive: true})
	if cmd == nil {
		t.Fatal("reconcileWorkingMsg{alive:true} must return a cmd (the reattach spawn)")
	}
	cmd() // spawnAgentTerminal's returned func — the bead lookup fails
	// (testBeadStore's bare "list" is empty) so the reattach itself errors,
	// but that's irrelevant here: the point is --resume must never fire.
	if resumeCalled {
		t.Fatal("Resume was called, want it never called on the alive/reattach path")
	}
}

// TestBeadsLoadedReconcilesWorkingBeadOnce: a "working" bead with no
// in-process session gets exactly one liveness check per process — a
// second reload for the same still-unresolved bead must not re-issue it.
func TestBeadsLoadedReconcilesWorkingBeadOnce(t *testing.T) {
	host := &fakeAgentHost{aliveWindows: map[string]bool{"baron-a": true}}
	d := testDeps()
	d.AgentHost = host
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking}}

	for range 2 {
		next, cmd := m.Update(beadsLoadedMsg{beads: beads})
		m = asModel(next)
		if cmd == nil {
			continue
		}
		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					c()
				}
			}
		} else {
			cmd()
		}
	}
	host.mu.Lock()
	n := len(host.aliveChecked)
	host.mu.Unlock()
	if n != 1 {
		t.Fatalf("Alive checked %d times across two reloads, want 1 (checked once per process)", n)
	}
}

// TestBeadsLoadedReconcileNeverOverlapsItself: a Reconcile pass that hasn't
// completed yet must not be dispatched again even once reconcileCooldown has
// elapsed since it started — lastReconcile alone can't tell "still running"
// from "long done", since it's stamped at dispatch time. Without
// reconcileInFlight, a pass slower than the cooldown (plausible: it shells
// out to git/tmux/bd per bead) would let the next beadsLoadedMsg pile a
// second, fully concurrent pass on top of the first.
func TestBeadsLoadedReconcileNeverOverlapsItself(t *testing.T) {
	var calls int
	d := testDeps()
	d.Reconcile = func(context.Context, map[string]bool) (ReconcileSummary, error) {
		calls++
		return ReconcileSummary{}, nil
	}
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}

	next, cmd1 := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	if cmd1 == nil {
		t.Fatal("first beadsLoadedMsg: cmd = nil, want a reconcile dispatch")
	}
	if !m.reconcileInFlight {
		t.Fatal("reconcileInFlight = false after dispatch, want true")
	}
	// cmd1 is deliberately not executed yet, standing in for a pass that's
	// still running when the cooldown next elapses.
	m.lastReconcile = time.Now().Add(-2 * reconcileCooldown)

	next, cmd2 := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m = runBatch(t, m, cmd2)
	if calls != 0 {
		t.Fatalf("Reconcile calls = %d, want 0 — a second beadsLoadedMsg while the first pass is still in flight must not dispatch another", calls)
	}

	// The still-outstanding first dispatch, once it actually runs and its
	// result is routed back through Update, both proves the mechanism
	// really does invoke Reconcile (not just never) and releases the guard
	// for a later reload.
	m = runBatch(t, m, cmd1)
	if calls != 1 {
		t.Fatalf("Reconcile calls = %d, want 1 after the in-flight pass actually completes", calls)
	}
	if m.reconcileInFlight {
		t.Fatal("reconcileInFlight = true after reconcileMsg, want false")
	}
}

// runBatch executes cmd — and recursively any tea.BatchMsg it returns — and
// routes every resulting message back through m.Update, the way a real
// tea.Program would. A test that needs a dispatched command's actual
// consequences (a mock Deps.Reconcile's call counter, a model field a
// message handler sets) rather than just "a non-nil cmd was returned" needs
// this; calling a returned cmd's closure alone runs its side effects but
// never lets Update react to what it reports.
func runBatch(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if msg == nil {
		return m
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = runBatch(t, m, c)
		}
		return m
	}
	next, _ := m.Update(msg)
	return asModel(next)
}

// TestReconcileCmdBoundsContext: a hung deps.Reconcile call (a wedged
// git/tmux/bd subprocess) must not leave reconcileInFlight stuck true
// forever — reconcileCmd has to hand it a context with a deadline, not the
// caller's own (the whole session's) unbounded one, or nothing ever forces
// it to give up and report back. Checking Deadline() rather than actually
// waiting out reconcilePassTimeout keeps this test fast; a real hang and
// timeout is exercised by hand, not in CI.
func TestReconcileCmdBoundsContext(t *testing.T) {
	var gotDeadline bool
	cmd := reconcileCmd(context.Background(), Deps{
		Reconcile: func(ctx context.Context, skip map[string]bool) (ReconcileSummary, error) {
			_, gotDeadline = ctx.Deadline()
			return ReconcileSummary{}, nil
		},
	}, nil)
	cmd()
	if !gotDeadline {
		t.Fatal("deps.Reconcile's ctx had no deadline — a hung subprocess call could block reconcileInFlight forever")
	}
}

// TestBeadsLoadedReconcilesBlockedBeadToOpen: a "blocked" bead with no
// live blocks-dependency is a lie (nothing actually blocks it) — the same
// startup reconcile that pulls dead "working" beads to retry pulls this
// one back to open, exactly once per process.
func TestBeadsLoadedReconcilesBlockedBeadToOpen(t *testing.T) {
	var calls int
	var gotSkip map[string]bool
	d := testDeps()
	d.Reconcile = func(_ context.Context, skip map[string]bool) (ReconcileSummary, error) {
		calls++
		gotSkip = skip
		return ReconcileSummary{Actions: []ReconcileActionSummary{
			{BRN: "baron-a", From: domain.BeadStateBlocked, To: domain.BeadStateOpen, Reason: "dependency graph changed"},
		}}, nil
	}
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusBlocked}}

	for range 2 {
		next, cmd := m.Update(beadsLoadedMsg{beads: beads})
		m = asModel(next)
		if cmd == nil {
			continue
		}
		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					c()
				}
			}
		} else {
			cmd()
		}
	}
	// The second beadsLoadedMsg lands well inside reconcileCooldown, so it
	// must not re-invoke Reconcile — otherwise every reload (a mutation, or
	// the 10s header-stats tick) would hammer git/tmux/bd per bead.
	if calls != 1 {
		t.Fatalf("Reconcile calls = %d, want exactly 1 (rate-limited by reconcileCooldown)", calls)
	}
	if gotSkip == nil {
		t.Fatal("skip set = nil, want a (possibly empty) map")
	}
}

// TestBeadsLoadedReconcileSkipsWorkingAndLiveSessions: Reconcile must never
// be asked to act on a "working" bead (reconcileWorkingCmd owns that state
// end to end, including reattaching a live view — something Reconcile has
// no way to do) or a bead with a live in-process session.
func TestBeadsLoadedReconcileSkipsWorkingAndLiveSessions(t *testing.T) {
	var gotSkip map[string]bool
	d := testDeps()
	d.Reconcile = func(_ context.Context, skip map[string]bool) (ReconcileSummary, error) {
		gotSkip = skip
		return ReconcileSummary{}, nil
	}
	m := New(context.Background(), d)
	m.sessions["baron-live"] = &agentTerminal{}
	beads := []store.Bead{
		{BRN: "baron-working", Title: "W", Status: store.BeadStatusWorking},
		{BRN: "baron-live", Title: "L", Status: store.BeadStatusOpen},
		{BRN: "baron-other", Title: "O", Status: store.BeadStatusOpen},
	}
	_, cmd := m.Update(beadsLoadedMsg{beads: beads})
	if cmd != nil {
		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					c()
				}
			}
		} else {
			cmd()
		}
	}
	if !gotSkip["baron-working"] {
		t.Error("skip set does not include the working bead, want it excluded")
	}
	if !gotSkip["baron-live"] {
		t.Error("skip set does not include the bead with a live session, want it excluded")
	}
	if gotSkip["baron-other"] {
		t.Error("skip set includes an unrelated open bead, want it left for Reconcile to examine")
	}
}

// TestBeadsLoadedReconcileSkipsAutoOffBeads: a human opting a bead out via
// '-' (m.autoOffBRNs — see updateKey) must actually suppress it from
// Reconcile's pass, not just relabel the header — otherwise the "toggle
// auto-run for the selected bead" keybinding would be cosmetic, same as
// the dead m.autoOn field it replaced.
func TestBeadsLoadedReconcileSkipsAutoOffBeads(t *testing.T) {
	var gotSkip map[string]bool
	d := testDeps()
	d.Reconcile = func(_ context.Context, skip map[string]bool) (ReconcileSummary, error) {
		gotSkip = skip
		return ReconcileSummary{}, nil
	}
	m := New(context.Background(), d)
	m.autoOffBRNs = map[string]bool{"baron-off": true}
	beads := []store.Bead{
		{BRN: "baron-off", Title: "Off", Status: store.BeadStatusAssigned},
		{BRN: "baron-on", Title: "On", Status: store.BeadStatusAssigned},
	}
	_, cmd := m.Update(beadsLoadedMsg{beads: beads})
	if cmd != nil {
		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					c()
				}
			}
		} else {
			cmd()
		}
	}
	if !gotSkip["baron-off"] {
		t.Error("skip set does not include the opted-out bead, want it excluded from Reconcile")
	}
	if gotSkip["baron-on"] {
		t.Error("skip set includes a bead nobody opted out, want it left for Reconcile to examine")
	}
}
