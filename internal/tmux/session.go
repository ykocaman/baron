// Package tmux hosts BARON's agent CLI processes independently of BARON's
// own lifetime: each bead's agent runs in its own window of a shared tmux
// session (default "baron"), so it keeps running whether BARON exits
// cleanly, crashes, or is simply not running at all — a headless `baron
// run` and the interactive TUI can both find and attach to the exact same
// window. tmux is an optional dependency (config tui.tmux); missing tmux
// degrades to a direct subprocess with no such persistence
// (internal/domain/tmux_launch.go's non-tmux path, internal/tui's raw-pty
// fallback). EnsureViewer/AttachArgv/KillViewer back the TUI's own live
// rendering of a window without disturbing the window itself or any other
// viewer attached to it.
package tmux

import (
	"context"
	"fmt"
	"strings"

	"github.com/baron-cli/baron/internal/tool"
)

// Client manages the BARON tmux session. Runner is any tool.Runner; tests
// inject a stub that records argv.
type Client struct {
	Runner  tool.Runner
	Session string
}

// New returns a Client for the default "baron" session.
func New(runner tool.Runner) *Client {
	return &Client{Runner: runner, Session: "baron"}
}

// target returns the window target "session:window".
func (c *Client) target(window string) string {
	return c.Session + ":" + window
}

// tmuxOpts strips the caller's $TMUX (and other credential-bearing vars):
// baron may itself run inside a tmux session, and a nested tmux client would
// attach to that session instead of managing the "baron" one.
var tmuxOpts = tool.Options{CleanEnv: true}

const hasSessionCommand = "has-session"

// Ensure creates the session if it does not exist. A missing session is the
// normal signal (has-session exits non-zero); any other failure surfaces
// when new-session runs.
func (c *Client) Ensure(ctx context.Context) error {
	_, err := c.Runner.Run(ctx, "tmux", []string{hasSessionCommand, "-t", c.Session}, tmuxOpts)
	if err == nil {
		return nil
	}
	if _, err := c.Runner.Run(ctx, "tmux", []string{"new-session", "-d", "-s", c.Session}, tmuxOpts); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}
	// new-session leaves a default window at index 0. Don't kill it: it's
	// the session's only window, and killing the last window tears the whole
	// server down. Spawn's new-window takes the next free index instead.
	return nil
}

// Spawn kills any existing window named window (missing is fine) and creates
// a new one running cmd with args, starting in dir (the -c flag is omitted
// when dir is empty, letting tmux use the session's start directory).
func (c *Client) Spawn(ctx context.Context, window, cmd, dir string, args ...string) error {
	_ = c.KillWindow(ctx, window) // ponytail: tolerate not-found; new-window fails loudly if tmux is broken
	// Trailing colon forces a session target: a bare "-t baron" resolves to
	// the window named "baron" (the TUI dashboard, window 0) when present,
	// and new-window then fails with "create window failed: index 0 in use".
	argv := []string{"new-window", "-t", c.Session + ":", "-n", window}
	if dir != "" {
		argv = append(argv, "-c", dir)
	}
	argv = append(argv, "--", cmd)
	argv = append(argv, args...)
	res, err := c.Runner.Run(ctx, "tmux", argv, tmuxOpts)
	if err != nil {
		// Fold tmux's stderr in so a failed spawn says why (e.g. a bad -c
		// directory), not just "exit status 1".
		if stderr := strings.TrimSpace(res.Stderr); stderr != "" {
			return fmt.Errorf("tmux new-window: %s", stderr)
		}
		return fmt.Errorf("tmux new-window: %w", err)
	}
	return nil
}

// PipePane tees window's pane output to logPath for the lifetime of the
// pane, so the agent's output survives even after the window is reaped (the
// in-memory capture-pane scrollback goes with it, but this file doesn't).
// -o toggles piping on only if not already piping, so a second call for the
// same window is a safe no-op.
func (c *Client) PipePane(ctx context.Context, window, logPath string) error {
	shellCmd := "cat >> " + ShellQuote(logPath)
	if _, err := c.Runner.Run(ctx, "tmux", []string{"pipe-pane", "-o", "-t", c.target(window), shellCmd}, tmuxOpts); err != nil {
		return fmt.Errorf("tmux pipe-pane: %w", err)
	}
	return nil
}

// ShellQuote wraps s in single quotes for safe use as one shell word,
// escaping any single quotes it contains.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// WindowExists reports whether window resolves in the session. A missing
// session or window is not an error (false, nil).
//
// has-session, not display-message: verified against real tmux (3.7b) that
// `display-message -p -t session:bogus-window` does NOT error when the
// session exists but the window doesn't — it silently falls back to
// reporting the session's *current* window instead, which would read a
// truly-gone window as still alive. has-session -t session:window does
// fail correctly ("can't find window: ...") when the window is missing.
func (c *Client) WindowExists(ctx context.Context, window string) (bool, error) {
	res, err := c.Runner.Run(ctx, "tmux", []string{hasSessionCommand, "-t", c.target(window)}, tmuxOpts)
	if err == nil {
		return true, nil
	}
	if isNotFound(res) {
		return false, nil
	}
	return false, fmt.Errorf("tmux has-session: %w", err)
}

// KillWindow kills window, tolerating a missing window.
func (c *Client) KillWindow(ctx context.Context, window string) error {
	res, err := c.Runner.Run(ctx, "tmux", []string{"kill-window", "-t", c.target(window)}, tmuxOpts)
	if err == nil || isNotFound(res) {
		return nil
	}
	return fmt.Errorf("tmux kill-window: %w", err)
}

// Attach attaches the current terminal to window (interactive; the TUI binds
// this to the `i` key).
//
// ponytail: runs through Runner for testability; TUI wiring must hand the
// terminal over (exec with os.Stdin/Stdout/Stderr passthrough, as
// tool.runInteractive does) for tmux attach to see a real TTY.
func (c *Client) Attach(ctx context.Context, window string) error {
	if _, err := c.Runner.Run(ctx, "tmux", []string{"attach", "-t", c.target(window)}, tmuxOpts); err != nil {
		return fmt.Errorf("tmux attach: %w", err)
	}
	return nil
}

// EnsureRunning starts window running argv (via LaunchScript's wrapper, so
// the pane survives the agent exiting — see domain.LaunchScript) in dir,
// unless a process is already running under that name: a reconnect (e.g.
// the TUI restarting) must never kill an agent still in flight. started
// reports whether this call actually launched a fresh process, so the
// caller knows whether it's safe to re-type the bead's prompt.
func (c *Client) EnsureRunning(ctx context.Context, window, script, dir string) (started bool, err error) {
	if err := c.Ensure(ctx); err != nil {
		return false, err
	}
	exists, err := c.WindowExists(ctx, window)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if err := c.Spawn(ctx, window, "sh", dir, "-c", script); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureViewer creates (or reconnects) a private viewer session grouped
// with c.Session and pinned to window, with its own status line and
// prefix key disabled — a transparent passthrough rather than another
// piece of interactive tmux chrome. Grouped sessions (tmux "session
// groups") share the target session's window list but track their own
// current window independently, so several viewers can each watch a
// different window in the same shared session at once without fighting
// over which one is "active" — a plain `tmux attach -t session:window`
// does not have this property once more than one client is attached.
func (c *Client) EnsureViewer(ctx context.Context, viewer, window string) error {
	exists := false
	if _, err := c.Runner.Run(ctx, "tmux", []string{hasSessionCommand, "-t", viewer}, tmuxOpts); err == nil {
		exists = true
	}
	if !exists {
		if err := c.createViewer(ctx, viewer); err != nil {
			return err
		}
	}
	if _, err := c.Runner.Run(ctx, "tmux", []string{"select-window", "-t", viewer + ":" + window}, tmuxOpts); err == nil {
		return nil
	}
	if !exists {
		// Freshly created and still cannot see the window: the window really
		// is missing, which is a genuine failure rather than stale state.
		return fmt.Errorf("tmux select-window (viewer %s): window %q not found in the session group", viewer, window)
	}
	// A viewer that exists but cannot select the window is stale — left
	// behind by a process that died mid-run, and grouped with a session that
	// is gone. Reusing it is what makes that permanent: has-session keeps
	// succeeding, so every later attach fails on select-window with no way
	// out. Rebuild it instead.
	if err := c.KillViewer(ctx, viewer); err != nil {
		return fmt.Errorf("tmux kill stale viewer %s: %w", viewer, err)
	}
	if err := c.createViewer(ctx, viewer); err != nil {
		return err
	}
	if _, err := c.Runner.Run(ctx, "tmux", []string{"select-window", "-t", viewer + ":" + window}, tmuxOpts); err != nil {
		return fmt.Errorf("tmux select-window (viewer): %w", err)
	}
	return nil
}

// createViewer makes the grouped, chrome-free viewer session — see
// EnsureViewer for why it is grouped rather than a plain attach.
func (c *Client) createViewer(ctx context.Context, viewer string) error {
	if _, err := c.Runner.Run(ctx, "tmux", []string{"new-session", "-d", "-t", c.Session, "-s", viewer}, tmuxOpts); err != nil {
		return fmt.Errorf("tmux new-session (viewer): %w", err)
	}
	if _, err := c.Runner.Run(ctx, "tmux", []string{"set-option", "-t", viewer, "status", "off"}, tmuxOpts); err != nil {
		return fmt.Errorf("tmux set-option status (viewer): %w", err)
	}
	if _, err := c.Runner.Run(ctx, "tmux", []string{"set-option", "-t", viewer, "prefix", "None"}, tmuxOpts); err != nil {
		return fmt.Errorf("tmux set-option prefix (viewer): %w", err)
	}
	return nil
}

// AttachArgv is the local command whose own pty streams viewer's pinned
// window live — what a caller spawns a pty around instead of the raw
// agent command.
func AttachArgv(viewer string) []string {
	return []string{"tmux", "attach-session", "-t", viewer}
}

// KillViewer tears down a viewer session (best-effort; a missing session
// is fine) without touching the window it was pinned to or its process.
func (c *Client) KillViewer(ctx context.Context, viewer string) error {
	res, err := c.Runner.Run(ctx, "tmux", []string{"kill-session", "-t", viewer}, tmuxOpts)
	if err == nil || isNotFound(res) {
		return nil
	}
	return fmt.Errorf("tmux kill-session (viewer): %w", err)
}

// ViewerPrefix is the name prefix every BARON viewer session carries. Callers
// derive names via domain.ViewerSessionName (internal/tmux cannot import
// domain — domain imports tmux); SweepViewers relies on that shape to find
// strays.
const ViewerPrefix = "baron-view-"

// SweepViewers kills every viewer session whose owning BARON process is gone,
// returning how many it removed. alive reports whether a pid is still running
// (injected so the sweep is testable without real processes).
//
// Viewer sessions are named with their creator's pid precisely so two BARON
// processes never collide — but that also means a viewer outlives the only
// process that knew to clean it up if that process dies without detaching.
// Crashes, SIGKILL and (until quitCmd existed) every ordinary quit all left
// one behind per attached bead. They are invisible in normal use and cost
// nothing individually, so they accumulate silently: 280 of them were found on
// a real machine, each grouped with the shared session and holding references
// to all of its windows. Sweeping at startup makes the leak self-healing
// rather than something the user has to know to clean up by hand.
//
// Never touches a viewer whose pid is still live: that is another BARON
// instance's window into the same session, and killing it would blank a
// running TUI's terminal pane.
func (c *Client) SweepViewers(ctx context.Context, alive func(pid int) bool) (int, error) {
	res, err := c.Runner.Run(ctx, "tmux", []string{"list-sessions", "-F", "#{session_name}"}, tmuxOpts)
	if err != nil {
		// No server running at all means nothing to sweep, not a failure.
		if isNotFound(res) || strings.Contains(res.Stderr, "no server running") {
			return 0, nil
		}
		return 0, fmt.Errorf("tmux list-sessions: %w", err)
	}
	killed := 0
	for name := range strings.SplitSeq(res.Stdout, "\n") {
		name = strings.TrimSpace(name)
		if !strings.HasPrefix(name, ViewerPrefix) {
			continue
		}
		pid, ok := viewerPID(name)
		if !ok || alive(pid) {
			continue
		}
		if err := c.KillViewer(ctx, name); err == nil {
			killed++
		}
	}
	return killed, nil
}

// viewerPID extracts the trailing pid from a viewer session name. Window names
// can themselves contain dashes, so only the final segment is considered, and
// it must be all digits — anything else is not a name this scheme produced and
// is left alone.
func viewerPID(name string) (int, bool) {
	idx := strings.LastIndex(name, "-")
	if idx < 0 || idx == len(name)-1 {
		return 0, false
	}
	pid := 0
	for _, r := range name[idx+1:] {
		if r < '0' || r > '9' {
			return 0, false
		}
		pid = pid*10 + int(r-'0')
	}
	if pid <= 0 {
		return 0, false
	}
	return pid, true
}

// isNotFound reports whether a failed tmux call means the target session or
// window does not exist — tmux's canonical "can't find session: ..." /
// "can't find window: ..." message on stderr. Matched on that fixed prefix,
// not a bare "can't find" substring: an unrelated stderr line that happens
// to contain the phrase (e.g. a broken tmux.conf's own directive error)
// must never be misread as "target missing" — WindowExists would then
// report false instead of erroring, and a caller like EnsureRunning would
// spawn a second agent process into what it wrongly believes is a fresh
// window.
func isNotFound(res tool.Result) bool {
	stderr := strings.TrimSpace(res.Stderr)
	return strings.HasPrefix(stderr, "can't find session:") || strings.HasPrefix(stderr, "can't find window:")
}
