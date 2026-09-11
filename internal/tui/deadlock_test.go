package tui

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// TestEmulatorNeverDeadlocksTheUI is the regression test for the hard freeze
// hit while navigating between beads with the Diff tab open: BARON stopped
// responding entirely, and a goroutine dump showed the UI parked in
// SafeEmulator.Render on the read lock.
//
// The chain: killing the previous bead's diff session closes its pty, which
// used to end the reply drain (an io.Copy that died on the write error). The
// child's next terminal query then had no reader, so Emulator.Write parked
// writing the reply into its internal pipe — holding the emulator's write lock
// forever. Render waits on that lock, and View calls Render, so the whole TUI
// hung on one undrained colour query.
//
// The invariant: a query arriving after the pty is gone must never stall a
// write, and Render must stay callable.
func TestEmulatorNeverDeadlocksTheUI(t *testing.T) {
	if raceEnabled {
		// vt.SafeEmulator.Close() promotes the embedded *vt.Emulator's own
		// Close() unlocked, while SafeEmulator.Read() takes its lock — a real
		// data race inside charm.land/x/vt itself (not in this test, and not
		// in anything BARON's own code changed), between this test's own
		// Cleanup and the pumpReplies goroutine it starts below. Confirmed
		// reproducible on demand (2 of 3 -race runs) against
		// v0.0.0-20260813141921-f091cedeaf78 — skip pending an upstream fix
		// rather than leave every -race run of this package permanently red.
		t.Skip("known data race in charm.land/x/vt's SafeEmulator.Close() vs Read() — see comment")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	term := &agentTerminal{brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(80, 24), pty: w}
	go term.pumpReplies()
	t.Cleanup(func() { _ = term.emu.Close() })

	// The pty goes away, exactly as it does when a bead switch kills the
	// session, and nothing is reading the other end any more either.
	_ = w.Close()
	_ = r.Close()

	// Now the child sends terminal queries — enough to fill any internal
	// buffer — the way every TUI does on startup.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			// OSC 10/11 (foreground/background colour) and DA1: all of these
			// make the emulator produce a reply.
			term.feed([]byte("\x1b]10;?\x07\x1b]11;?\x07\x1b[c"))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writing to the emulator parked with a dead pty — the write lock is held forever and the UI cannot render")
	}

	// And the UI must still be able to draw.
	rendered := make(chan struct{})
	go func() {
		defer close(rendered)
		_ = term.emu.Render()
	}()
	select {
	case <-rendered:
	case <-time.After(5 * time.Second):
		t.Fatal("Render blocked — this is the hang the user saw as a completely unresponsive TUI")
	}
}

// TestPumpRepliesSurvivesPtyWriteFailure: the drain must keep going when the
// pty write fails, not treat it as a reason to stop. Stopping is what left the
// reply pipe unread.
func TestPumpRepliesSurvivesPtyWriteFailure(t *testing.T) {
	if raceEnabled {
		// Same pre-existing charm.land/x/vt race as TestEmulatorNeverDeadlocksTheUI
		// — see that test's comment.
		t.Skip("known data race in charm.land/x/vt's SafeEmulator.Close() vs Read() — see TestEmulatorNeverDeadlocksTheUI")
	}
	r, w, _ := os.Pipe()
	_ = r.Close() // every write to w now fails
	term := &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(80, 24), pty: w}
	go term.pumpReplies()
	t.Cleanup(func() { _ = term.emu.Close(); _ = w.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			term.feed([]byte("\x1b]11;?\x07"))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain stopped after a failed pty write, so replies backed up and blocked the emulator")
	}
}

// TestKillEndsTheChildProcess is the regression test for the process leak
// behind the same report: navigating between beads with the Diff tab open
// kills the previous bead's session on every move, and closing the pty alone
// did not end the child. Measured with hunk, which was still running six
// seconds after its pty went away — one leaked process per bead visited, 79 of
// them on a real machine.
func TestKillEndsTheChildProcess(t *testing.T) {
	// A child that ignores its pty going away, like hunk does.
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "trap '' HUP; while :; do sleep 0.2; done")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 40, Rows: 10})
	if err != nil {
		t.Skipf("cannot start a pty here: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = f.Close() })

	term := &agentTerminal{brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(40, 10), pty: f, cmd: cmd}
	term.kill()
	go func() {
		if err := cmd.Wait(); err != nil {
			t.Logf("child process exited: %v", err)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the child outlived kill() — every bead switch would leak one of these")
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// TestExplicitCloseKeepsItsOwnNotice: closing a session with 'x' confirms
// "closed diff view for X". The exit that follows must not then replace that
// with "diff view for X exited" — the user is told the consequence of their
// own action instead of the action, and the confirmation they were waiting for
// disappears. This surfaced once kill() actually ended the child promptly.
func TestExplicitCloseKeepsItsOwnNotice(t *testing.T) {
	m := scrollBaseModel(t, 2, nil)
	term, _ := pipedSession(t, &m, kindDiff, "")
	term.userKilled = true

	next, cmd := m.Update(agentExitMsg{brn: "baron-a", kind: kindDiff})
	m = asModel(next)
	if cmd != nil {
		if _, ok := cmd().(statusExpiredMsg); !ok {
			// Any other message would be a fresh notice overwriting the one
			// the confirmation set.
			t.Errorf("a user-requested exit produced %T, want no new notice", cmd())
		}
	}
	if strings.Contains(m.statusMsg, "exited") {
		t.Errorf("statusMsg = %q — the exit notice replaced the close confirmation", m.statusMsg)
	}
}

// TestUnrequestedExitStillReportsItself: the quiet path above must not silence
// a session that died on its own, which is exactly when the user needs telling.
func TestUnrequestedExitStillReportsItself(t *testing.T) {
	m := scrollBaseModel(t, 2, nil)
	pipedSession(t, &m, kindDiff, "")

	next, _ := m.Update(agentExitMsg{brn: "baron-a", kind: kindDiff})
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "exited") {
		t.Errorf("statusMsg = %q, want the unexpected exit reported", m.statusMsg)
	}
}

// TestQuitEndsLocalSessions: a local session has no host to outlive BARON in,
// so quitting without ending it just orphans the process. The Diff tab's pager
// ignores the hangup a closing pty sends, which is how 79 stray hunk processes
// accumulated on a real machine — one per bead whose diff had been opened.
func TestQuitEndsLocalSessions(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "trap '' HUP; while :; do sleep 0.2; done")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 40, Rows: 10})
	if err != nil {
		t.Skipf("cannot start a pty here: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = f.Close() })

	m := scrollBaseModel(t, 2, nil)
	m.diffSessions["baron-a"] = &agentTerminal{
		brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(40, 10), pty: f, cmd: cmd,
	}

	m.quitCmd()
	go func() {
		if err := cmd.Wait(); err != nil {
			t.Logf("child process exited: %v", err)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("quitting left a local session running — it is orphaned for good")
}
