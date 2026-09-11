package tui

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/domain"
)

// termFrameInterval is the Agent-tab re-render cadence while a session is
// active (~24fps, smooth enough for an interactive agent TUI).
const termFrameInterval = 42 * time.Millisecond

// clampUint16 clamps a terminal dimension (cols/rows, always small and
// non-negative in practice) into uint16's range before handing it to the
// pty.Winsize syscall struct, which only has room for uint16.
func clampUint16(v int) uint16 {
	if v < 0 {
		return 0
	}
	if v > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(v)
}

// AgentHost hosts a bead's interactive agent process independently of
// BARON's own lifetime — implemented today by internal/cli wiring a tmux
// window in a shared session (see internal/tmux), so the agent keeps
// running even if BARON exits or crashes and a headless `baron run` can
// find and finish the exact same run the TUI started. A future Docker or
// Kubernetes backend can satisfy the same interface without any change
// to terminal.go. Deps.AgentHost == nil falls back to spawning the agent
// directly in a bare pty (no persistence across a BARON restart) — e.g.
// tmux isn't installed, or config tui.tmux = "never".
type AgentHost interface {
	// EnsureRunning starts window running argv in dir unless a process is
	// already running under that name (idempotent — a reconnect must
	// never kill an agent still in flight). started reports whether this
	// call actually launched a fresh process, so the caller knows
	// whether it's safe to type the bead's prompt.
	EnsureRunning(ctx context.Context, window string, argv []string, dir string) (started bool, err error)
	// Alive reports whether window's process is still running,
	// independent of any attached viewer — used right after startup to
	// tell a bead that's still actually working (reattach) from one
	// that's genuinely stuck (needs `run --resume`).
	Alive(ctx context.Context, window string) (bool, error)
	// AttachArgv returns the local command (its own pty) whose stdio
	// streams window's live content — spawnAgentTerminal's pty runs this
	// instead of the raw agent command.
	AttachArgv(ctx context.Context, window string) ([]string, error)
	// Detach tears down BARON's own viewer of window (the tmux side
	// effect of a local pty closing) without touching window's process —
	// for a session ending without the user asking to stop the agent.
	Detach(ctx context.Context, window string) error
	// Kill ends window's process outright — an explicit user "stop", not
	// a mere detach (see killSession).
	Kill(ctx context.Context, window string) error
	// CapturePane returns the last lines of window's scrollback as plain
	// text, oldest first. This is the host's own record of what the process
	// has printed, which for a program that writes to the terminal normally
	// (rather than painting its own full screen) is the only complete
	// history anywhere: BARON's pty is an attach client that only ever sees
	// redraws of the current pane. Empty output is not an error — a pane
	// that has scrolled nothing has nothing to give.
	CapturePane(ctx context.Context, window string, lines int) (string, error)
}

// termKind distinguishes what an embedded terminal hosts: the assigned
// coding agent (kindAgent, the original and only kind) or a live `hunk
// diff` review session (kindDiff). A bead can have one live session of each
// kind at once — see Model.sessions/diffSessions — sharing the same
// spawn/render/focus/zoom machinery below, since the only real differences
// are argv/dir resolution, whether AgentHost/tmux persistence applies, and
// whether a prompt gets typed in after spawn.
type termKind string

const (
	kindAgent termKind = "agent"
	kindDiff  termKind = "diff"
)

// agentTerminal is one bead's embedded terminal session — the assigned
// coding agent (claude/opencode) or a `hunk diff` review, per kind — running
// on a pty, whose output a vt emulator renders for the right pane. The
// output pump fills the emulator off the update loop; the tick msg keeps
// the frame landing on screen.
type agentTerminal struct {
	brn     string
	kind    termKind
	cmd     *exec.Cmd
	pty     *os.File
	emu     *vt.SafeEmulator
	exitCh  chan struct{}
	focus   bool // 't': keystrokes forward to this terminal
	zoom    bool // 'z': fullscreen
	done    bool
	exitErr error
	// idleNudgedAt is when checkIdleAgents sent its one-shot "continue"
	// nudge (unix nanoseconds, 0 = not yet nudged) — see that function's
	// own doc comment for the two-step idle/nudge/resume sequence this
	// gates. A real reply bumps lastActivity, not this: the grace period
	// is measured from the nudge itself, not from whatever the agent says
	// back.
	idleNudgedAt atomic.Int64
	// idleChecked latches checkIdleAgents' own one-shot resume call — set
	// the moment it fires (success or error), never re-evaluated for this
	// *agentTerminal instance regardless of how long the session then sits
	// idle. A freshly (re)spawned session gets a brand new agentTerminal
	// (see spawnAgentTerminal), so a genuine restart naturally clears it —
	// this only guards against re-firing every tick against the same idle
	// stretch.
	idleChecked bool
	// lastActivity is the unix nanosecond timestamp of the last byte pump
	// copied from the pty, 0 meaning nothing has arrived yet — see
	// waitForQuiet, which uses this to type the bead's prompt once the
	// agent's own TUI has actually finished drawing instead of racing a
	// fixed delay.
	lastActivity atomic.Int64
	// tmuxWindow is the tmux window name hosting this session's agent
	// process when tmux-backed ("" for the raw-pty fallback). t.pty is
	// then a local *viewer* attach client, not the agent itself — the
	// agent survives this pty closing; see agentDetachedMsg vs
	// agentExitMsg.
	tmuxWindow string
	// cols/rows is the size resize() last actually applied — see resize's
	// doc comment for why a repeat at the same size must be skipped.
	cols, rows int
	// startedAt is when this session was spawned. For a Diff session it is
	// the cutoff that keeps ingestHunkComments from replaying review comments
	// that predate it.
	startedAt time.Time
	// history is the host's captured pane scrollback (oldest first), the real
	// backlog for a tmux-backed session — see paneHistoryCmd. Nil until the
	// first scroll asks for it; capturedAt dates it so it can be refreshed
	// rather than served stale forever.
	history    []string
	capturedAt time.Time
	capturing  bool
	// cursorHidden mirrors the child's DECTCEM state (CSI ?25l / ?25h), which
	// says whether it currently wants a caret drawn — a TUI hides the cursor
	// while it repaints and shows it when it is waiting for input. Tracked
	// from the byte stream here rather than read off the emulator, whose own
	// copy of this flag is not exported; two escape sequences are far less to
	// maintain than a fork of the emulator. See setCursorVisibility.
	cursorHidden atomic.Bool
	// userKilled records that this session's exit was explicitly requested
	// (see killSession), so the exit handler stays quiet about it.
	userKilled bool
	// historyUnavailable records that the last capture found nothing to page
	// through — the pane has not scrolled yet, or the child paints its own
	// screen and so keeps no backlog. Scrolling then goes to the child
	// instead. Deliberately NOT a latch: an agent that has printed nothing
	// yet will have printed plenty later, and a permanent flag would mean a
	// scroll attempted too early disabled scrolling for the rest of the
	// session. Every scroll re-checks, rate-limited by capturedAt.
	historyUnavailable bool
	// doneMu guards the pump-side DoneSentinel scan (scanForDone writes from
	// the pump goroutine; doneResult reads from the update loop).
	doneMu sync.Mutex
	// doneTail is a bounded rolling window of the most recent pty bytes,
	// scanned for domain.DoneSentinel as they arrive. Kept only until the
	// sentinel is found, then released.
	doneTail  []byte
	doneFound bool
	doneErr   error
}

// doneScanTail is how much recent pty output scanForDone keeps in order to
// spot the sentinel (which can straddle read boundaries) and to recover the
// agent's own error line preceding it. A few KB is far more than the tail of
// a shell echo needs while staying trivially cheap to rescan per chunk.
const doneScanTail = 8192

// scanForDone looks for domain.DoneSentinel in the session's output as it
// streams past, recording the exit result for checkTmuxDone to pick up.
//
// This runs on the pump goroutine, deliberately. checkTmuxDone used to do the
// detection itself on the update loop by rendering each live session's entire
// screen and ANSI-stripping the result — ~213µs per session, on every 42ms
// frame, forever, purely to grep for a string that had almost never arrived.
// With several sessions registered that alone was the whole frame budget, and
// it was work the single-threaded update loop had to finish before it could
// service a keystroke. The pump already sees every byte exactly once, so the
// same detection here costs a bounded memmem per read and blocks nothing.
//
// Only tmux-backed sessions echo the sentinel (it comes from
// domain.LaunchScript's wrapper); a raw-pty child's real exit is reported by
// the pump's own EOF instead, so there is nothing to scan for.
func (t *agentTerminal) scanForDone(chunk []byte) {
	if t.tmuxWindow == "" {
		return
	}
	t.doneMu.Lock()
	defer t.doneMu.Unlock()
	if t.doneFound {
		return
	}
	t.doneTail = append(t.doneTail, chunk...)
	if len(t.doneTail) > doneScanTail {
		t.doneTail = t.doneTail[len(t.doneTail)-doneScanTail:]
	}
	// Fast path: the sentinel is plain ASCII echoed by the shell, so a raw
	// byte search settles the overwhelmingly common "not there" case without
	// allocating. Only once it matches is the tail ANSI-stripped, so the
	// exit code and error line are read from clean text.
	if !bytes.Contains(t.doneTail, []byte(domain.DoneSentinel)) {
		return
	}
	content := xansi.Strip(string(t.doneTail))
	if !strings.Contains(content, domain.DoneSentinel) {
		return
	}
	t.doneFound = true
	t.doneTail = nil
	if code := domain.SentinelExitCode(content); code != 0 {
		reason := domain.AgentErrorLine(content)
		if reason != "" {
			reason = " — " + reason
		}
		t.doneErr = fmt.Errorf("exit code %d%s", code, reason)
	}
}

// feed is everything that happens when a chunk of the child's output arrives:
// the activity stamp waitForQuiet reads, the completion scan, and the write
// into the emulator. The pump is its only production caller; tests use it so
// they drive a session the same way real output does, rather than reaching
// past it into the emulator (which is how a test kept passing while the
// detection it covered had moved elsewhere).
func (t *agentTerminal) feed(chunk []byte) {
	t.lastActivity.Store(time.Now().UnixNano())
	t.scanForDone(chunk)
	t.trackCursorVisibility(chunk)
	_, _ = t.emu.Write(chunk)
}

// cursorModeShow / cursorModeHide are DECTCEM, the sequences a child sends to
// show and hide the text cursor.
var (
	cursorModeShow = []byte("\x1b[?25h")
	cursorModeHide = []byte("\x1b[?25l")
)

// trackCursorVisibility follows the child's show/hide-cursor sequences as they
// stream past, so the caret BARON draws (see caretOverlay) matches what the
// child actually wants on screen.
//
// Last one in the chunk wins, which is the same answer a terminal would reach.
// A sequence split across two reads is missed; the child sends these on every
// repaint, so the state corrects itself on the next one rather than sticking.
func (t *agentTerminal) trackCursorVisibility(chunk []byte) {
	show := bytes.LastIndex(chunk, cursorModeShow)
	hide := bytes.LastIndex(chunk, cursorModeHide)
	switch {
	case hide > show:
		t.cursorHidden.Store(true)
	case show > hide:
		t.cursorHidden.Store(false)
	}
}

// doneResult reports whether the sentinel has been seen, and with what exit
// result.
func (t *agentTerminal) doneResult() (bool, error) {
	t.doneMu.Lock()
	defer t.doneMu.Unlock()
	return t.doneFound, t.doneErr
}

// agentSpawnedMsg reports that brn's embedded terminal is up (carrying the
// session for the update loop to register), or why it failed. kind is set
// even on failure (t nil), so the handler can pick the right map/notice
// text without inspecting t.
type agentSpawnedMsg struct {
	brn  string
	kind termKind
	t    *agentTerminal
	err  error
}

// agentExitMsg reports that brn's process itself has finished — a raw-pty
// child exiting, or (tmux-backed) domain.DoneSentinel appearing in the
// pane. The emulator keeps its final frame so the tab still shows the
// session's last screen. kind tells the handler whether the bead's
// run-pipeline resume/persist logic applies (agent only — see update.go).
type agentExitMsg struct {
	brn  string
	kind termKind
	err  error
}

// agentDetachedMsg reports that brn's *local viewer* went away (killSession,
// or the attach client dying) while tmux-backed — the window and its agent
// process are untouched and may still be running; this only clears BARON's
// own view of it, so 't' correctly reports "no session" until the bead is
// reconciled (see the beadsLoadedMsg reconciliation in update.go).
type agentDetachedMsg struct {
	brn  string
	kind termKind
}

// reconcileWorkingMsg reports whether brn's tmux window is still alive —
// see reconcileWorkingCmd.
type reconcileWorkingMsg struct {
	brn   string
	alive bool
	err   error
}

// reconcileWorkingCmd checks brn's tmux window liveness. Used once per bead
// per process right after beads load, for any "working" bead with no
// in-process session — either a fresh TUI launch, a restart, or a prior
// crash: alive means the agent is still going, just unattached (reattach
// it); gone means it's genuinely stuck — the bead transitions working to
// retry so a human or `baron run` can relaunch it.
