package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/hunk"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tmux"
	"github.com/baron-cli/baron/internal/tool"
	"github.com/baron-cli/baron/internal/tui"
)

// theme fallback.
func (a *app) agentHostFor(ctx context.Context, cfg *store.Config) tui.AgentHost {
	if cfg == nil || !tmux.Usable(ctx, a.runner, cfg.TUI.Tmux) {
		return nil
	}
	tmx := tmux.New(a.runner)
	if cfg.TUI.TmuxSession != "" {
		tmx.Session = cfg.TUI.TmuxSession
	}
	// Reap viewer sessions left behind by BARON processes that are gone (a
	// crash, a SIGKILL, or any quit from before the shutdown detach existed).
	// Best-effort: a sweep that fails must never stop the TUI from starting.
	_, _ = tmx.SweepViewers(ctx, pidAlive)
	return &tmuxAgentHost{tmx: tmx}
}

// hunkDeps builds the two hunk-backed Deps fields together, from a single
// PATH probe: the steering flag (whether a spawned agent is told it can leave
// inline review notes) and the comment reader (whether BARON pulls those notes
// back out). They are gated as one because half of the loop is worse than
// neither — telling agents to post notes nobody collects, or polling for notes
// nothing was ever asked to write.
func (a *app) hunkDeps(ctx context.Context) (comments func(string) ([]hunk.Comment, error)) {
	if !hunk.Available(ctx, a.runner) {
		return nil
	}
	return func(repo string) ([]hunk.Comment, error) {
		// Bounded independently of ctx's own lifetime (the whole TUI
		// session): tui.Deps.HunkComments takes no context of its own, so
		// without this a hung `hunk` invocation (a lock file, a stuck
		// process) would never return, leaving Model.hunkPollInFlight
		// (internal/tui) stuck true and silently disabling comment polling
		// for the rest of the session — the same failure mode
		// reconcilePassTimeout guards against for Reconcile.
		ctx, cancel := context.WithTimeout(ctx, hunkCommentsTimeout)
		defer cancel()
		return hunk.Comments(ctx, a.runner, repo)
	}
}

// hunkCommentsTimeout bounds one hunk.Comments call — normally near-instant
// (a local CLI reading its own session file), so this is purely a backstop
// against a genuinely wedged process, not a budget a normal call approaches.
const hunkCommentsTimeout = 15 * time.Second

// tmuxAgentHost adapts *tmux.Client to tui.AgentHost — see that interface's
// doc comment for why it exists as an abstraction: a future Docker or
// Kubernetes backend can implement the same four methods without any
// change to internal/tui.
type tmuxAgentHost struct {
	tmx *tmux.Client
}

func (h *tmuxAgentHost) EnsureRunning(ctx context.Context, window string, argv []string, dir string) (bool, error) {
	if exists, _ := h.tmx.WindowExists(ctx, window); exists {
		res, err := h.tmx.Runner.Run(ctx, "tmux", []string{
			"capture-pane", "-p", "-t", h.tmx.Session + ":" + window, "-S", "-200",
		}, tool.Options{CleanEnv: true})
		if err == nil && strings.Contains(res.Stdout, domain.DoneSentinel) {
			_ = h.tmx.KillWindow(ctx, window)
		}
	}
	script := domain.LaunchScript(argv[0], argv[1:], domain.TaskAgentEnv(dir))
	return h.tmx.EnsureRunning(ctx, window, script, dir)
}

// aliveScanLines is how far back Alive reads for the wrapper's sentinel.
// The line is printed once, immediately after the agent exits, and the
// pane's shell prompt is all that follows it — a few hundred lines covers
// even a chatty shell banner without capturing the whole transcript.
const aliveScanLines = 200

// Alive reports whether window's *agent* is still running — not whether the
// window exists. Those are different questions here: the pane wrapper ends
// with `exec $SHELL` on purpose, so the window (and a live shell in it)
// outlives the agent by design, keeping its output readable. Treating window
// existence as liveness left every finished bead reading "working" forever —
// the TUI's startup reconcile saw "alive", reattached to a dead shell, and
// never resolved the bead's real state.
//
// The wrapper prints DoneSentinel ("baron-run-exit=N") exactly when the
// agent exits, so its presence in the pane is the signal. A capture that
// fails answers "alive": the cost of a false "dead" is resuming a bead whose
// agent is still working, which is far worse than a stale row.
func (h *tmuxAgentHost) Alive(ctx context.Context, window string) (bool, error) {
	exists, err := h.tmx.WindowExists(ctx, window)
	if err != nil || !exists {
		return false, err
	}
	content, err := h.CapturePane(ctx, window, aliveScanLines)
	if err == nil {
		return !strings.Contains(content, domain.DoneSentinel), nil
	}
	return true, nil
}

func (h *tmuxAgentHost) AttachArgv(ctx context.Context, window string) ([]string, error) {
	viewer := h.viewerName(window)
	if err := h.tmx.EnsureViewer(ctx, viewer, window); err != nil {
		return nil, err
	}
	return tmux.AttachArgv(viewer), nil
}

func (h *tmuxAgentHost) Detach(ctx context.Context, window string) error {
	return h.tmx.KillViewer(ctx, h.viewerName(window))
}

// CapturePane reads window's pane history out of tmux.
//
//   - -S -<lines> starts the capture that many lines back into the scrollback.
//   - -J rejoins lines tmux wrapped at the pane width, so a long line comes
//     back as one line rather than several — what makes the result stable
//     across a pane resize.
//   - -e keeps the text's colour and attributes. Without it capture-pane
//     returns plain text, so scrolling back turned the pane monochrome and
//     scrolling to the bottom brought the colour back — the live frame is the
//     emulator's own render, which never lost it.
func (h *tmuxAgentHost) CapturePane(ctx context.Context, window string, lines int) (string, error) {
	res, err := h.tmx.Runner.Run(ctx, "tmux", []string{
		"capture-pane", "-p", "-J", "-e", "-t", h.tmx.Session + ":" + window,
		"-S", "-" + strconv.Itoa(lines),
	}, tool.Options{CleanEnv: true})
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return res.Stdout, nil
}

func (h *tmuxAgentHost) Kill(ctx context.Context, window string) error {
	_ = h.tmx.KillViewer(ctx, h.viewerName(window))
	return h.tmx.KillWindow(ctx, window)
}

// viewerName derives a private, per-process viewer session name for
// window, so two concurrently-running BARON processes attaching to the
// same window (e.g. a restart racing the previous instance's shutdown)
// never collide.
func (h *tmuxAgentHost) viewerName(window string) string {
	return domain.ViewerSessionName(window, os.Getpid())
}

// headerStats returns the header bar's project metrics. brn, when non-empty
// and its worktree exists on disk, scopes the branch/changed-file/diff
// numbers to that bead's own worktree instead of the main checkout — so the
// header tracks whichever bead is selected. An empty brn (nothing selected)
// or a bead with no worktree yet (never assigned) falls back to the main
// checkout's own branch, which is ordinarily "main". ShortPath and the tmux
// pane/agent count always describe the main checkout, not the worktree.
