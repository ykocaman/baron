package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/hunk"
)

type paneHistoryMsg struct {
	brn   string
	kind  termKind
	lines []string
	err   error
}

// paneHistoryCmd fetches a tmux-backed session's real scrollback from the host.
//
// This is the only place the backlog of a normally-printing agent can come
// from. BARON's pty for such a session is a `tmux attach-session` client: it
// receives redraws of the *current* pane and nothing else, so its emulator
// accumulates at best the few lines that happened to scroll past while it was
// watching. tmux, which owns the pane, has the whole thing — verified against
// a real opencode run: the visible pane held 20 of 90 printed lines and
// capture-pane returned all 90.
//
// (An alt-screen application is the exception and does not come through here —
// see scrollSessionPane. Nothing keeps scrollback for the alternate screen,
// tmux included, because the application owns that screen and its history.)
func paneHistoryCmd(ctx context.Context, host AgentHost, brn string, kind termKind, window string) tea.Cmd {
	return func() tea.Msg {
		out, err := host.CapturePane(ctx, window, paneHistoryLines)
		if err != nil {
			return paneHistoryMsg{brn: brn, kind: kind, err: err}
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			lines = nil
		}
		return paneHistoryMsg{brn: brn, kind: kind, lines: lines}
	}
}

// needsHistory reports whether a scroll should refresh this session's captured
// scrollback: it has none yet, or what it has has aged out.
func (t *agentTerminal) needsHistory() bool {
	if t.capturing {
		return false
	}
	return t.history == nil || time.Since(t.capturedAt) > paneHistoryMaxAge
}

// hunkPollInterval is how often the live Diff session's review comments are
// re-read. Slow on purpose: this shells out to the hunk CLI, and a review note
// arriving a second or two later than it was typed costs nothing.
const hunkPollInterval = 2 * time.Second

// hunkCommentsMsg carries the result of one poll of brn's Hunk session.
type hunkCommentsMsg struct {
	brn      string
	comments []hunk.Comment
	err      error
}

// maybePollHunkCmd polls the live Diff session's review comments, at most once
// per hunkPollInterval, and only while there is a Diff session to poll.
//
// It rides the frame tick rather than arming a timer of its own. A second
// self-re-arming tea.Tick is precisely the bug that made the TUI unusable (see
// Model.tickScheduled), and there is no reason to risk repeating it for
// something that can be a rate-limited check on a clock that already runs.
func (m *Model) maybePollHunkCmd() tea.Cmd {
	if m.deps.HunkComments == nil || m.liveBRN == "" {
		return nil
	}
	t := m.diffSessions[m.liveBRN]
	if t == nil || t.done {
		return nil
	}
	if m.hunkPollInFlight || time.Since(m.lastHunkPoll) < hunkPollInterval {
		return nil
	}
	m.lastHunkPoll = time.Now()
	m.hunkPollInFlight = true
	repo := m.deps.Dir
	if m.deps.Worktrees != nil {
		repo = m.deps.Worktrees.Path(m.liveBRN)
	}
	brn, load := m.liveBRN, m.deps.HunkComments
	return func() tea.Msg {
		comments, err := load(repo)
		return hunkCommentsMsg{brn: brn, comments: comments, err: err}
	}
}

// ingestHunkComments turns review comments left on the live diff into bead
// comments and delivers them to the agent — the round trip that makes the Diff
// tab part of the work loop instead of a read-only view.
//
// A note left on a diff line is a review instruction. Left in hunk it reaches
// nobody: the agent is not watching the review tool, and the bead's own record
// never learns of it. So each new one is recorded on the bead (durable, shows
// up in the Overview tab like any other comment) and handed to the agent
// (steering if it is running, a restart prompt if it stopped — see
// deliverCommentCmd).
//
// Only comments created after the diff session started are ingested, and each
// id is ingested once. Without the cutoff, opening the Diff tab against a
// pre-existing external hunk session would dump its entire comment history
// into the agent in one go.
func (m *Model) ingestHunkComments(msg hunkCommentsMsg) tea.Cmd {
	t := m.diffSessions[msg.brn]
	if t == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, c := range msg.comments {
		if c.ID == "" || m.hunkSeen[c.ID] {
			continue
		}
		if !c.CreatedAt.IsZero() && !t.startedAt.IsZero() && c.CreatedAt.Before(t.startedAt) {
			// Predates this session; record it as seen so it is skipped
			// from now on without ever being delivered.
			m.hunkSeen[c.ID] = true
			continue
		}
		m.hunkSeen[c.ID] = true
		text := "review note on " + c.Location() + ": " + c.Summary
		cmds = append(
			cmds,
			runTypedAction(func() (string, error) { return m.deps.Comment(msg.brn, text) }),
			m.deliverCommentCmd(msg.brn, text),
		)
	}
	if len(cmds) == 0 {
		return nil
	}
	cmds = append(cmds, loadComments(m.ctx, m.deps, msg.brn))
	return tea.Batch(cmds...)
}

// deliverCommentCmd gets text in front of the bead's agent, whatever state
// that agent is in, and reports which way it went.
//
// A comment on a bead under active work is not a filing-cabinet entry — it is
// something the reviewer wants the agent to act on. Storing it and leaving the
// agent none the wiser means the review only lands if a human notices and
// relays it by hand, which is exactly the gap this closes. There are two
// cases, and both have to work or the feature is a coin flip:
//
//   - The agent is running: type the comment into its session (steer). It
//     picks it up as its next turn.
//   - The agent has stopped, or was never started in this process: start it
//     with the comment as its opening prompt (spawnAgentTerminal's followUp),
//     so it resumes on the note rather than on a brief it already followed.
//
// The storing of the comment as a bead comment is the caller's business and
// happens regardless — delivery is best-effort on top of a durable record,
// never instead of one.
func (m *Model) deliverCommentCmd(brn, text string) tea.Cmd {
	if text == "" {
		return nil
	}
	t := m.sessions[brn]
	if t != nil && !t.done && t.pty != nil {
		t.steer(text)
		return m.notify("sent to the running agent for " + brn)
	}
	// No agent has run for this bead in this session, so there is nothing to
	// steer and nothing to resume: "stopped" means an agent that was working
	// and finished, not one that was never started. Starting an agent is what
	// 'r' is for, and doing it as a side effect of leaving a comment would
	// launch work the user did not ask for — on any bead they happened to
	// comment on. The comment is still recorded; it just has no one to notify.
	if t == nil {
		return nil
	}
	cols, rows := m.termDims()
	m.liveBRN = brn
	// Restarting an agent is a visible, consequential thing to have just
	// caused, so show it happening. Steering stays on the Overview tab (the
	// comment is the thing to look at there); a restart puts the Terminal tab
	// up, where the agent is coming back to life.
	m.detailTab = tabIndexFor(kindAgent)
	return tea.Batch(
		m.notify("agent for "+brn+" was not running — restarting it with your comment"),
		spawnAgentTerminal(m.ctx, m.deps, m.beads, brn, cols, rows, text),
	)
}

// detachAllCmd is the cleanup that must run before the process exits: local
// sessions are ended outright (nothing else will), and for tmux-backed ones
// only BARON's own viewer is torn down — the agent is meant to outlive BARON
// there, which is the entire point of hosting it in tmux.
//
// This is not an optimisation. Each attached session creates a private viewer
// session named baron-view-<window>-<pid> (see internal/cli's viewerName), and
// nothing ever removed them: quitting went straight to tea.Quit, so every
// BARON run leaked one viewer session per bead it had attached to, forever.
// Measured on a real machine after a few days of use: 280 orphaned viewer
// sessions, each grouped with the shared "baron" session and therefore each
// holding live references to all 53 of its windows. The tmux server keeps
// state for every one of them.
//
// Detach is best-effort per session and shells out, so the whole sweep runs in
// one cmd off the update loop rather than blocking the quit.
