package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/domain"
)

func (t *agentTerminal) pump() {
	buf := make([]byte, 4096)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.feed(buf[:n])
		}
		if err != nil {
			break
		}
	}
	t.exitErr = t.cmd.Wait()
	_ = t.pty.Close()
	// No more writes will reach the emulator, so close it: that is what ends
	// pumpReplies, which otherwise keeps draining for the emulator's whole
	// lifetime on purpose (see its doc comment).
	_ = t.emu.Close()
	close(t.exitCh)
}

// pumpReplies drains the emulator's automatic replies (the OSC color / DA /
// CPR answers a child's TUI expects back from "the terminal") and forwards
// them to the pty.
//
// Draining is not optional and must not stop while the emulator is alive.
// Emulator.Write parks writing a reply into its internal pipe until something
// reads it — and it holds the emulator's write lock the whole time. So a
// single undrained query freezes not just this session but the entire TUI:
// View calls Render, Render takes the read lock, and the read lock is behind
// the parked writer. Observed exactly that way, as a hard hang after a few
// bead switches with the Diff tab open — killing the previous bead's hunk
// closed its pty, an io.Copy-based drain died on the write error, and the
// next colour query the child sent parked forever with the lock held.
//
// Hence the explicit loop rather than io.Copy: a failed write to the pty is
// ignored and draining continues. Only the emulator closing (see pump) ends
// it, which is the one point after which no further reply can be produced.
func (t *agentTerminal) pumpReplies() {
	buf := make([]byte, 4096)
	for {
		n, err := t.emu.Read(buf)
		if n > 0 && t.pty != nil {
			_, _ = t.pty.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// promptQuietWindow is how long the agent's pty must be silent before its
// TUI is considered done drawing (and safe to type into). 400ms used to be
// enough margin in practice, then 700ms, but a real multi-phase startup (a
// splash frame, then a pause while the agent loads something, then the
// real input box) can produce a quiet gap of its own mid-startup — under
// system load that gap gets longer, and a too-short window falsely reads
// it as "done drawing," typing into a screen that isn't ready and
// silently dropping the prompt (see interactiveReadyDelay's doc comment
// for the observed case).
//
// Measured live (claude 2.1.224, real machine, ordinary load — the
// "silently dropped forever" bug this constant now guards against): the
// splash paints once around t=1.5s, then goes completely silent for
// 2.5s (a real network round trip — auth/org lookup, the weekly-limit
// banner, the update check — not a rendering pause), and only then does
// claude's loading spinner start repainting the whole screen every
// ~300-350ms for several more seconds before the input box is actually
// ready. A 700ms quiet window reads that 2.5s pre-spinner silence as
// "done drawing" and types straight into the still-loading splash; the
// spinner's very next repaint overwrites it, and the typed prompt never
// reaches an input box that did not exist yet — indistinguishable from
// "nothing happened" from the outside. 3s comfortably clears that
// measured 2.5s gap with margin for a slower network or a more loaded
// machine, while staying well under promptMaxWait's 25s backstop.
const promptQuietWindow = 3 * time.Second

// promptMaxWait caps how long typePrompt will wait for quiet before typing
// anyway — a safety net against an agent that never goes quiet (a blinking
// cursor, say), so a slow/unusual agent delays the prompt rather than
// dropping it forever. Widened alongside promptQuietWindow/
// interactiveReadyDelay for the same reason: a genuinely slow start under
// load should still get real quiet-detection instead of falling through to
// "type anyway" before the TUI has actually finished drawing.
const promptMaxWait = 25 * time.Second

// typePrompt delivers the bead's prompt to the interactive agent once its
// TUI has drawn, the way the old cockpit typed it with send-keys.
//
// The guard before the write is critical: if the agent exits during
// waitForQuiet the wrapper's "exec $SHELL" takes the pane, and any
// subsequent write lands on a shell prompt and runs as commands (observed
// as "zsh: parse error near ." when the bead prompt hit the shell).
// Two complementary checks cover both execution paths:
//   - exitCh closed ⇒ raw-pty child exited (EOF on the pty)
//   - doneFound set ⇒ tmux-backed agent echoed DoneSentinel
func (t *agentTerminal) typePrompt(launch domain.InteractiveLaunch) {
	if launch.PromptKeys == "" {
		return
	}
	t.waitForQuiet(launch.ReadyDelay, promptQuietWindow, promptMaxWait)
	// Did the agent exit while we were waiting? Raw-pty: exitCh is already
	// closed. Tmux-backed: scanForDone set doneFound. Either way, the
	// agent's shell wrapper has taken the pane — do NOT write there.
	select {
	case <-t.exitCh:
		return // raw-pty child already gone
	default:
	}
	t.doneMu.Lock()
	found := t.doneFound
	t.doneMu.Unlock()
	if found {
		return // tmux-backed agent already echoed DoneSentinel
	}
	// Wrap the prompt in bracketed paste mode (\x1b[200~ ... \x1b[201~) to prevent
	// multi-line prompts from triggering an early submission on the first newline.
	_, _ = t.pty.WriteString("\x1b[200~")
	_, _ = t.pty.WriteString(launch.PromptKeys)
	_, _ = t.pty.WriteString("\x1b[201~")
	// promptSubmitDelay: a long paste renders as a collapsed "[Pasted text
	// #1 +N lines]" placeholder rather than literal text, and that
	// collapse is not instantaneous — claude (and likely other TUIs built
	// the same way) processes the \x1b[201~ end-of-paste marker
	// asynchronously from the byte stream that delivered it. A \r written
	// in the same burst arrives before that transition finishes and is
	// silently swallowed: the placeholder sits there fully formed and
	// correct, submit never happens, and the bead parks forever with a
	// live, idle agent that received its task but never started it —
	// indistinguishable from typePrompt not having run at all. Measured
	// live (claude 2.1.224): back-to-back writes with no gap lost the \r
	// on every trial; a 100ms gap delivered it on every trial. 150ms
	// keeps a safety margin over that measured floor.
	time.Sleep(150 * time.Millisecond)
	_, _ = t.pty.WriteString("\r")
}

// waitForQuiet blocks until the pty has produced no output for quiet, or
// maxWait has elapsed since call time — whichever comes first. A fixed
// delay alone raced a slow-drawing agent TUI (system load, a cold-cache
// agent start): typing before its input box exists gets the keystrokes
// swallowed by the terminal, silently dropping the entire prompt. minDelay
// is a floor before the first sample, since "no output yet" and "drew
// instantly and has been idle since" look identical to lastActivity alone.
func (t *agentTerminal) waitForQuiet(minDelay, quiet, maxWait time.Duration) {
	deadline := time.Now().Add(maxWait)
	time.Sleep(minDelay)
	for time.Now().Before(deadline) {
		// Agent already gone — no point waiting for quiet output that will
		// never come, and the caller's guards need to fire promptly.
		select {
		case <-t.exitCh:
			return
		default:
		}
		t.doneMu.Lock()
		found := t.doneFound
		t.doneMu.Unlock()
		if found {
			return
		}
		if last := t.lastActivity.Load(); last != 0 && time.Since(time.Unix(0, last)) >= quiet {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitTermExit blocks until the session's pump reports its pty closing and
// relays that to the update loop — agentExitMsg (the agent process itself
// is done) for the raw-pty fallback, agentDetachedMsg (just BARON's own
// viewer went away; the tmux window and its agent may still be running)
// when tmux-backed. See checkTmuxDone for how a tmux-backed session's real
// agentExitMsg gets fired instead.
func waitTermExit(t *agentTerminal) tea.Cmd {
	return func() tea.Msg {
		<-t.exitCh
		if t.tmuxWindow != "" {
			return agentDetachedMsg{brn: t.brn, kind: t.kind}
		}
		return agentExitMsg{brn: t.brn, kind: t.kind, err: t.exitErr}
	}
}

// checkTmuxDone collects an agentExitMsg for each live tmux-backed session
// whose agent has finished — domain.DoneSentinel, the same completion marker
// baron run's headless tmux path polls for via capture-pane
// (internal/domain/tmux_launch.go). The window itself stays up (LaunchScript
// hands off to an interactive shell, exactly like the headless path leaves it
// for inspection); only t.done and the resulting resume are triggered here.
//
// The detection itself happens in the pump as bytes arrive (scanForDone) —
// this rides the frame tick only to turn an already-recorded result into a
// message on the update loop, so it costs a mutex-guarded bool per session
// rather than a full screen render per session per frame.
