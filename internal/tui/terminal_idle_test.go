package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// TestCheckTmuxDoneDetectsSentinelAndTriggersResume: a tmux-backed
// session's window stays up after the agent finishes (LaunchScript hands
// off to an interactive shell) — checkTmuxDone must notice
// domain.DoneSentinel in the pane and fire the same resume flow a raw-pty
// exit would, exactly once.
func TestCheckTmuxDoneDetectsSentinelAndTriggersResume(t *testing.T) {
	resumed := make(chan string, 1)
	d := testDeps()
	d.AgentHost = &fakeAgentHost{}
	d.Resume = func(brn string) (string, error) {
		resumed <- brn
		return "", nil
	}
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	term := &agentTerminal{brn: "baron-a", emu: vt.NewSafeEmulator(40, 5), tmuxWindow: "baron-a"}
	// Through feed, exactly as the pump delivers it: the sentinel is spotted
	// as the bytes stream past, not by re-rendering the screen each frame.
	term.feed([]byte("agent output\r\n" + domain.DoneSentinel + "=0\r\n"))
	m.sessions["baron-a"] = term

	next, cmd := m.Update(tickMsg{})
	m = asModel(next)
	if !term.done {
		t.Fatal("checkTmuxDone must mark the session done once the sentinel appears")
	}
	if cmd == nil {
		t.Fatal("tickMsg must return a cmd producing agentExitMsg")
	}
	drainUntilResume(t, m, cmd)
	// A channel (not a plain var polled from the main goroutine) makes this
	// wait race-free — the write happens on whichever goroutine runs the
	// resume cmd drainUntilResume fired.
	select {
	case brn := <-resumed:
		if brn != "baron-a" {
			t.Fatalf("Resume called with %q, want \"baron-a\"", brn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Resume was never called")
	}
}

// drainUntilResume feeds cmd's result back into m.Update the way the real
// bubbletea runtime would — unwrapping tea.BatchMsg recursively — until an
// agentExitMsg surfaces, then feeds that into Update too. agentExitMsg's
// own handler always returns a genuine multi-cmd tea.Batch (statusDismissCmd
// among them, a 4s tea.Tick), so those are fired concurrently rather than
// awaited serially. Fails the test if no agentExitMsg ever surfaces.
func drainUntilResume(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	var found bool
	for _, msg := range flattenBatch(cmd()) {
		exitMsg, ok := msg.(agentExitMsg)
		if !ok {
			continue
		}
		found = true
		next, cmd2 := m.Update(exitMsg)
		m = asModel(next)
		if batch, ok := cmd2().(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					go c()
				}
			}
		}
		break
	}
	if !found {
		t.Fatal("no agentExitMsg surfaced from cmd")
	}
	return m
}

// runIdleAgentsTick fires tickMsg{} and executes every returned cmd
// synchronously (checkIdleAgents' own cmds are runTypedAction closures —
// direct calls, not another message needing further routing back through
// Update the way agentExitMsg does), so a spy on deps.IdleResume observes
// the call by the time this returns.
func runIdleAgentsTick(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.Update(tickMsg{})
	m = asModel(next)
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
	return m
}

// TestCheckIdleAgentsFiresAfterThreshold: a kindAgent session with no new
// pty bytes for longer than deps.IdleThreshold, cursor not explicitly
// hidden, and not human-focused, is exactly what checkIdleAgents exists to
// catch: the first idle tick sends the "continue" nudge (no IdleResume
// call yet — the session gets a chance to recover on its own first), and
// only once it's STILL idle a full IdleThreshold after that nudge does
// deps.IdleResume actually fire. idleChecked then latches so it never
// fires twice for the same session.
func TestCheckIdleAgentsFiresAfterThreshold(t *testing.T) {
	var gotBRN string
	var calls int
	d := testDeps()
	d.IdleThreshold = time.Millisecond
	d.IdleResume = func(brn string) (string, error) {
		gotBRN = brn
		calls++
		return "", nil
	}
	m := New(context.Background(), d)

	term := &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(40, 5)}
	term.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	m.sessions["baron-a"] = term

	// First idle tick: nudges, does not resume yet.
	m = runIdleAgentsTick(t, m)
	if calls != 0 {
		t.Fatalf("IdleResume calls after the first idle tick = %d, want 0 — it must nudge first", calls)
	}
	if term.idleNudgedAt.Load() == 0 {
		t.Fatal("idleNudgedAt = 0, want set after the first idle tick's nudge")
	}
	if term.idleChecked {
		t.Fatal("idleChecked = true after only a nudge, want false")
	}

	time.Sleep(2 * time.Millisecond) // clear IdleThreshold since the nudge
	m = runIdleAgentsTick(t, m)
	if calls != 1 || gotBRN != "baron-a" {
		t.Fatalf("IdleResume calls = %d, brn = %q, want 1 call with baron-a once still idle past the nudge", calls, gotBRN)
	}
	if !term.idleChecked {
		t.Error("idleChecked = false, want true after firing")
	}

	// A further tick, still idle, must not fire again — the latch holds.
	runIdleAgentsTick(t, m)
	if calls != 1 {
		t.Errorf("IdleResume calls after a further tick = %d, want still 1 (latched)", calls)
	}
}

// TestCheckIdleAgentsSkipsKindDiff: a live `hunk diff --watch` session is
// idle by design, permanently — it must never be mistaken for a stalled
// bead-run agent.
func TestCheckIdleAgentsSkipsKindDiff(t *testing.T) {
	var calls int
	d := testDeps()
	d.IdleThreshold = time.Millisecond
	d.IdleResume = func(brn string) (string, error) { calls++; return "", nil }
	m := New(context.Background(), d)

	term := &agentTerminal{brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(40, 5)}
	term.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	m.diffSessions["baron-a"] = term

	m = runIdleAgentsTick(t, m)
	time.Sleep(2 * time.Millisecond)
	runIdleAgentsTick(t, m) // would-be resume tick too, not just the nudge tick
	if calls != 0 {
		t.Errorf("IdleResume calls = %d, want 0 — kindDiff must never be treated as idle-stalled", calls)
	}
}

// TestCheckIdleAgentsSkipsCursorHidden: cursorHidden true means the child
// is mid-repaint or still thinking, not parked at its own input prompt —
// content-diff-style watchdogs would false-positive on a busy spinner, so
// this guard must suppress detection rather than allow it.
func TestCheckIdleAgentsSkipsCursorHidden(t *testing.T) {
	var calls int
	d := testDeps()
	d.IdleThreshold = time.Millisecond
	d.IdleResume = func(brn string) (string, error) { calls++; return "", nil }
	m := New(context.Background(), d)

	term := &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(40, 5)}
	term.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	term.cursorHidden.Store(true)
	m.sessions["baron-a"] = term

	m = runIdleAgentsTick(t, m)
	time.Sleep(2 * time.Millisecond)
	runIdleAgentsTick(t, m) // would-be resume tick too, not just the nudge tick
	if calls != 0 {
		t.Errorf("IdleResume calls = %d, want 0 — cursorHidden must suppress detection", calls)
	}
}

// TestCheckIdleAgentsSkipsFocused: never second-guess a session the human
// currently has 't'-focused — they're right there.
func TestCheckIdleAgentsSkipsFocused(t *testing.T) {
	var calls int
	d := testDeps()
	d.IdleThreshold = time.Millisecond
	d.IdleResume = func(brn string) (string, error) { calls++; return "", nil }
	m := New(context.Background(), d)

	term := &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(40, 5), focus: true}
	term.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	m.sessions["baron-a"] = term

	m = runIdleAgentsTick(t, m)
	time.Sleep(2 * time.Millisecond)
	runIdleAgentsTick(t, m) // would-be resume tick too, not just the nudge tick
	if calls != 0 {
		t.Errorf("IdleResume calls = %d, want 0 — a focused session must never be nudged", calls)
	}
	if term.idleNudgedAt.Load() != 0 {
		t.Error("idleNudgedAt != 0, want never nudged either — a focused session must never be typed into")
	}
}

// TestCheckIdleAgentsSkipsBelowThreshold: lastActivity within
// deps.IdleThreshold is still-working, not idle.
func TestCheckIdleAgentsSkipsBelowThreshold(t *testing.T) {
	var calls int
	d := testDeps()
	d.IdleThreshold = time.Hour
	d.IdleResume = func(brn string) (string, error) { calls++; return "", nil }
	m := New(context.Background(), d)

	term := &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(40, 5)}
	term.lastActivity.Store(time.Now().UnixNano())
	m.sessions["baron-a"] = term

	runIdleAgentsTick(t, m)
	if calls != 0 {
		t.Errorf("IdleResume calls = %d, want 0 — session is well within threshold", calls)
	}
}

// TestCheckIdleAgentsDisabledByDefault: testDeps() leaves IdleResume nil —
// checkIdleAgents must no-op rather than panic on a nil call, and this also
// guards the zero-value Deps{} case generally (IdleThreshold defaults to 0).
func TestCheckIdleAgentsDisabledByDefault(t *testing.T) {
	d := testDeps()
	m := New(context.Background(), d)
	term := &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(40, 5)}
	term.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	m.sessions["baron-a"] = term

	runIdleAgentsTick(t, m) // must not panic
	if term.idleChecked {
		t.Error("idleChecked = true, want false — IdleResume is nil, nothing should have fired")
	}
}

// TestCheckIdleAgentsSkipsDoneSession: a session already resolved via a
// real process exit (done/doneFound) must never also be idle-resumed —
// that would race deps.Resume's own transition for the same bead.
func TestCheckIdleAgentsSkipsDoneSession(t *testing.T) {
	var calls int
	d := testDeps()
	d.IdleThreshold = time.Millisecond
	d.IdleResume = func(brn string) (string, error) { calls++; return "", nil }
	m := New(context.Background(), d)

	term := &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(40, 5), done: true}
	term.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	m.sessions["baron-a"] = term

	m = runIdleAgentsTick(t, m)
	time.Sleep(2 * time.Millisecond)
	runIdleAgentsTick(t, m) // would-be resume tick too, not just the nudge tick
	if calls != 0 {
		t.Errorf("IdleResume calls = %d, want 0 — a done session must never also be idle-resumed", calls)
	}
	if term.idleNudgedAt.Load() != 0 {
		t.Error("idleNudgedAt != 0, want never nudged either — a done session must never be typed into")
	}
}

// TestCheckIdleAgentsNudgeWritesToThePty: the nudge is a real steer() call,
// not just a state flag — verify the exact bracketed-paste bytes actually
// land on the session's pty, the same mechanism deliverCommentCmd already
// uses (steer's own doc comment explains the bracketing).
func TestCheckIdleAgentsNudgeWritesToThePty(t *testing.T) {
	d := testDeps()
	d.IdleThreshold = time.Millisecond
	d.IdleResume = func(brn string) (string, error) { return "", nil }
	m := New(context.Background(), d)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	term := &agentTerminal{brn: "baron-a", kind: kindAgent, pty: w, emu: vt.NewSafeEmulator(40, 5)}
	term.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	m.sessions["baron-a"] = term

	runIdleAgentsTick(t, m)

	buf := make([]byte, 4096)
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("read nudge from pty: %v", err)
	}
	sent := string(buf[:n])
	if !strings.Contains(sent, idleNudgeText) {
		t.Errorf("sent to pty = %q, want it to contain idleNudgeText %q", sent, idleNudgeText)
	}
	if !strings.Contains(sent, "\x1b[200~") || !strings.Contains(sent, "\x1b[201~") {
		t.Errorf("sent to pty = %q, want bracketed paste around it (steer's own convention)", sent)
	}
}

// TestCheckIdleAgentsRecoversOnReply: a real reply after the nudge (even
// just acknowledging) bumps lastActivity, and the still-idle grace period
// is measured from the nudge, not the original idle detection — so a
// session that responds must get a fresh full IdleThreshold before being
// treated as stuck again, not an already-expired one.
func TestCheckIdleAgentsRecoversOnReply(t *testing.T) {
	var calls int
	d := testDeps()
	d.IdleThreshold = 50 * time.Millisecond
	d.IdleResume = func(brn string) (string, error) { calls++; return "", nil }
	m := New(context.Background(), d)

	term := &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(40, 5)}
	term.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	m.sessions["baron-a"] = term

	m = runIdleAgentsTick(t, m) // nudges
	if term.idleNudgedAt.Load() == 0 {
		t.Fatal("idleNudgedAt = 0, want set after the nudge tick")
	}

	// The agent replies almost immediately — a real byte arrives, bumping
	// lastActivity well after idleNudgedAt.
	time.Sleep(5 * time.Millisecond)
	term.lastActivity.Store(time.Now().UnixNano())

	// Old code measuring the grace period from lastActivity's original
	// (hour-old) value would already call IdleResume here; this must not.
	runIdleAgentsTick(t, m)
	if calls != 0 {
		t.Fatalf("IdleResume calls = %d, want 0 — the reply must not be treated as still-idle", calls)
	}
}
