package tui

import (
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"
)

func (m Model) checkTmuxDone() []tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.allSessions() {
		if t == nil || t.done || t.tmuxWindow == "" {
			continue
		}
		found, exitErr := t.doneResult()
		if !found {
			continue
		}
		t.done = true
		brn, kind := t.brn, t.kind
		cmds = append(cmds, func() tea.Msg { return agentExitMsg{brn: brn, kind: kind, err: exitErr} })
	}
	return cmds
}

// idleNudgeText is checkIdleAgents' one-shot "are you still there" — sent
// once, only after a session has already gone idle at its own prompt for a
// full IdleThreshold, never baked into a bead's opening prompt (the whole
// point: teaching every agent about "[ask]" on every single invocation
// permanently taxes prompt signal-to-noise for a case most beads never
// hit — see docs/PRD/agent-contract.md's idle-detection section — but a
// short, situational nudge sent only to a session that has *already*
// proven itself stuck costs nothing everywhere else). "DONE" is not
// parsed by anything — postAgentGate still decides purely off observable
// diff/commit state (see its own doc comment for why a typed claim is the
// weakest possible input to a transition that mutates real tracked work);
// it's here only so the agent has a natural, low-effort way to
// acknowledge instead of just resuming silently or ignoring the nudge.
const idleNudgeText = `continue. reply DONE if finished, or comment "[ask] ..." if you need something.`

// checkIdleAgents finds every bead-run session (kindAgent only — a live
// `hunk diff --watch` in kindDiff is idle by design, permanently, and would
// false-positive on every check) that has gone quiet at its own input
// prompt and, in two steps, gives it a chance to recover before treating
// the bead as stuck:
//
//  1. First time idle (idleNudgedAt still 0): type idleNudgeText into the
//     session (agentTerminal.steer, the same bracketed-paste mechanism
//     comment-delivery already uses) and record when. This does not touch
//     bead state at all — an agent that was genuinely just paused
//     mid-task, not stuck, gets a chance to keep going on its own, which
//     is strictly better for a bead that would otherwise sit in "working"
//     forever than either doing nothing or jumping straight to human_queue.
//  2. Still idle a full IdleThreshold after the nudge (a real reply from
//     the agent — even just acknowledging — bumps lastActivity and this
//     grace period restarts, exactly like the first-idle check): treat it
//     as genuinely stuck and fire deps.IdleResume — the same ask-check/
//     validate/gate pipeline agentExitMsg already runs on a real process
//     exit (deps.Resume/runResume), just retriggered by "went idle" instead
//     of "exited." If the nudge produced a real diff/commits in between,
//     this routes through the normal validating path exactly like any
//     other resume; if it produced nothing at all, postAgentGate's
//     parkIfNoChange lands it in human_queue instead of a silent no-op —
//     see that function's own doc comment for why the two triggers differ
//     there.
//
// A nudge is a real, if small, risk of its own: typed text is a weak
// signal to feed a stuck agent, and pushing it to "continue" without
// knowing *why* it stopped could as easily invite a wrong guess as recover
// real progress. It is deliberately not skipped anyway — the alternative,
// leaving a bead silently parked in "working" with nobody watching until a
// human happens to notice, has a worse failure mode (indefinitely stuck,
// discovered by nobody) than the nudge's (an agent that guesses wrong
// still has to pass the same gate every other bead does before anything
// merges — a bad guess is caught there, not smuggled through).
//
// "Idle" here is deliberately not content-diff (checkTmuxDone's sibling,
// domain/tmux_launch.go's SilentDeathMonitor, uses that for the headless
// path) — a busy agent CLI's own spinner/token-counter redraws mean
// captured pane bytes are never byte-identical for a genuinely busy
// session, and an idle-but-still-repainting one would never trip a
// diff-based check either, silently disabling this feature. Instead this
// reads two already-tracked, protocol-level signals: lastActivity (no new
// pty bytes at all — not even a repaint) and cursorHidden (DECTCEM; a TUI
// hides its cursor while repainting, shows it while it's actually waiting
// on input). The combination only ever *suppresses* a false idle
// detection, never causes one: cursorHidden explicitly true (mid-repaint
// or still thinking) always skips, matching "when unsure, don't act, since
// resuming a session that's still genuinely working risks nothing but a
// wasted check next tick, while acting too early on the wrong session
// state is the harder-to-undo mistake."
//
// t.focus (the human has 't'-focused this exact pane) also always skips —
// never second-guess or talk over someone who's actively there. t.done/
// t.doneFound skip too: a real exit is already resolving this bead through
// agentExitMsg, and this must never race that (see terminal.go's own
// DoneSentinel commentary on why exit detection is trusted over any
// staleness heuristic).
func (m Model) checkIdleAgents() []tea.Cmd {
	if m.deps.IdleResume == nil || m.deps.IdleThreshold <= 0 {
		return nil
	}
	var cmds []tea.Cmd
	for _, t := range m.allSessions() {
		if cmd := m.checkIdleAgent(t); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

func (m Model) checkIdleAgent(t *agentTerminal) tea.Cmd {
	if t == nil || t.kind != kindAgent || t.done || t.doneFound || t.idleChecked || t.focus || t.cursorHidden.Load() {
		return nil
	}
	last := t.lastActivity.Load()
	if last == 0 || time.Since(time.Unix(0, last)) < m.deps.IdleThreshold {
		return nil
	}
	nudgedAt := t.idleNudgedAt.Load()
	if nudgedAt == 0 {
		t.steer(idleNudgeText)
		t.idleNudgedAt.Store(time.Now().UnixNano())
		return nil
	}
	if time.Since(time.Unix(0, nudgedAt)) < m.deps.IdleThreshold {
		return nil
	}
	t.idleChecked = true
	brn := t.brn
	return runTypedAction(func() (string, error) { return m.deps.IdleResume(brn) })
}

// resize fits the session's pty and emulator to cols/rows — a no-op when
// that's already the size in effect. This isn't just an optimization: a
// repeated same-size resize right after spawn used to be harmless against
// a raw pty (an ignorable no-change SIGWINCH), but against a tmux-attach
// pty it re-triggers tmux's attach-driven window-resize machinery a second
// time — landing squarely inside typePrompt's critical window and
// corrupting/dropping the agent's still-empty input box before the prompt
// ever reaches it (observed live: the prompt sat, never submitted, while
// the agent's own placeholder was already showing). agentSpawnedMsg's
// resizeSessions() call is exactly this redundant case: the session was
// already started at this same cols/rows.
func (t *agentTerminal) resize(cols, rows int) {
	if cols <= 0 || rows <= 0 || t.pty == nil {
		return
	}
	if cols == t.cols && rows == t.rows {
		return
	}
	_ = pty.Setsize(t.pty, &pty.Winsize{Rows: clampUint16(rows), Cols: clampUint16(cols)})
	t.emu.Resize(cols, rows)
	t.cols, t.rows = cols, rows
}

// kill ends the session's local child: the pty master is closed (which
// unblocks the pump and reports the exit back to the update loop) and the
// child's process group is signalled.
//
// The signal is not belt-and-braces. Closing the pty alone leaves the child
// running whenever it does not treat the hangup as fatal — measured with hunk,
// which was still alive six seconds after its pty went away. Since a Diff
// session is killed on every bead switch, that leaked one hunk process per
// bead visited: 79 were found on a real machine, each holding a worktree open.
//
// The group, not just the process: pty.Start puts the child in its own
// session, so signalling -pid reaches anything it spawned. SIGTERM rather than
// SIGKILL so it can deregister from its own daemon on the way out; the group
// call falling back to the bare process covers the case where it never became
// a leader.
//
// For a tmux-backed session t.cmd is BARON's `tmux attach-session` client, not
// the agent — ending it detaches, which is exactly what the callers here mean.
// Stopping the agent itself is the host's job (see killSession).
func (t *agentTerminal) kill() {
	if t.pty != nil {
		_ = t.pty.Close()
	}
	if t.cmd == nil || t.cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-t.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		_ = t.cmd.Process.Signal(syscall.SIGTERM)
	}
}

// steer types text into a running agent's session and submits it — the
// live-agent half of comment delivery (see deliverCommentCmd).
//
// Bracketed paste, not a raw write: a comment is prose a human typed, so it
// routinely contains newlines, and an agent's input box submits on the first
// one. Without the brackets a three-line review note becomes three separate
// turns, the first of them a fragment. This is the same wrapping typePrompt
// uses for the opening prompt, and for the same reason.
//
// Unlike typePrompt there is no wait for the agent to be quiet first: the
// session is already up and its input box already exists, which is precisely
// what distinguishes steering from starting.
func (t *agentTerminal) steer(text string) {
	if t.pty == nil || text == "" {
		return
	}
	_, _ = t.pty.WriteString("\x1b[200~")
	_, _ = t.pty.WriteString(text)
	_, _ = t.pty.WriteString("\x1b[201~")
	_, _ = t.pty.WriteString("\r")
}

// forwardScroll asks the child to scroll itself, for a session whose history
// BARON cannot read.
//
// Page Up / Page Down, not a synthesised mouse wheel: they are what pagers and
// full-screen TUIs bind, they survive the tmux relay unchanged, and they need
// no knowledge of whether the child turned mouse reporting on. Verified
// against hunk's pager, the one that matters here — both keys move its
// content. (A wheel event was tried first and needed a patched vt to know
// whether the child would even receive it; the keys work without that.)
//
// Keyboard focus is deliberately not required: scrolling to read is not the
// same gesture as typing, and making the user press 't' first — then remember
// shift+esc to get back — to scroll a pane they are already looking at is the
// friction this removes.
func (t *agentTerminal) forwardScroll(dir tea.MouseButton, n int) {
	if t.pty == nil {
		return
	}
	seq := "\x1b[5~" // Page Up
	if dir == tea.MouseWheelDown {
		seq = "\x1b[6~"
	}
	for range n {
		_, _ = t.pty.WriteString(seq)
	}
}

// sendKeys forwards one keypress to the pty as the raw bytes the child's
// terminal expects.
func (t *agentTerminal) sendKeys(msg tea.KeyPressMsg) {
	b := termKeyBytes(msg)
	if len(b) == 0 || t.pty == nil {
		return
	}
	_, _ = t.pty.Write(b)
}

// termKeyBytes maps a bubbletea keypress to the terminal byte sequence.
// Printable text is UTF-8; control keys are their ANSI/control bytes. Claude
// and opencode need printable text, enter, esc, arrows and ctrl+c/d at
// minimum.
func termKeyBytes(msg tea.KeyPressMsg) []byte {
	key := msg.Key()
	if text := key.Text; text != "" {
		return []byte(text)
	}
	switch key.Code {
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyTab:
		if key.Mod&tea.ModShift != 0 {
			return []byte("\x1b[Z")
		}
		return []byte{'\t'}
	case tea.KeyEscape:
		return []byte{0x1b}
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	case tea.KeySpace:
		return []byte{' '}
	}
	// ctrl+a..ctrl+z report Code 'a'..'z' with ModCtrl; the control byte is
	// 0x01..0x1a.
	if key.Mod&tea.ModCtrl != 0 && key.Code >= 'a' && key.Code <= 'z' {
		return []byte{byte(key.Code - 'a' + 1)}
	}
	return nil
}

// allSessions returns every live session across both kinds — used by the
// kind-agnostic operations (frame tick, resize) that must cover whatever's
// actually running regardless of which tab is on screen.
