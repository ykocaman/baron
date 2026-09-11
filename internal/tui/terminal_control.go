package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func (m Model) detachAllCmd() tea.Cmd {
	host := m.deps.AgentHost
	windows := make([]string, 0, len(m.sessions)+len(m.diffSessions))
	for _, t := range m.allSessions() {
		if t == nil {
			continue
		}
		if t.tmuxWindow != "" {
			windows = append(windows, t.tmuxWindow)
			continue
		}
		// A local session has no host to outlive BARON in, so leaving it
		// running just orphans it. Closing the pty is not enough — hunk, the
		// Diff tab's pager, ignores the hangup (see kill) — which is how 79
		// stray hunk processes were found on a real machine, each holding a
		// worktree open. kill() is synchronous and only signals, so this is
		// safe to do here rather than in the cmd below.
		t.kill()
	}
	// Ending local sessions above happens whether or not there is a host —
	// an early return on a nil host would skip it entirely, which is exactly
	// the tmux-less configuration where nothing else cleans up.
	if host == nil || len(windows) == 0 {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		for _, w := range windows {
			_ = host.Detach(ctx, w)
		}
		return nil
	}
}

// quitCmd ends the program after detaching BARON's viewers. tea.Sequence, not
// tea.Batch: a batched cleanup races the quit and usually loses, which is how
// the viewer leak survived for as long as it did.
func (m Model) quitCmd() tea.Cmd {
	if cmd := m.detachAllCmd(); cmd != nil {
		return tea.Sequence(cmd, tea.Quit)
	}
	return tea.Quit
}

// killSession kills kind's running terminal for the selected bead and
// clears focus. For a tmux-backed agent session this also stops the
// underlying window's agent process outright (not just detaching BARON's
// own viewer) — 'x' means "stop the agent"; merely closing the local pty
// would leave it running unattended in its tmux window. A Diff session
// never has a tmuxWindow, so the same function closes it as a plain local
// pty with no extra step.
func (m *Model) killSession(kind termKind) tea.Cmd {
	t := m.sessionFor(kind)
	if t == nil {
		return nil
	}
	window := t.tmuxWindow
	// Mark it before killing: the exit that follows is one the user asked
	// for, and its own "… exited" notice would land a moment later and
	// replace the "stopped agent" / "closed diff view" confirmation they just
	// got — reporting the consequence instead of the action.
	t.userKilled = true
	t.kill()
	m.termFocus = false
	if window == "" || m.deps.AgentHost == nil {
		return nil
	}
	host, ctx := m.deps.AgentHost, m.ctx
	return func() tea.Msg {
		_ = host.Kill(ctx, window)
		return nil
	}
}

// finalLines returns t's emulator frame as plain text (ANSI stripped) for
// persistence, so the .summary log stays a readable transcript on disk
// (grep, cat, a text editor) instead of a file full of raw SGR codes.
func (t *agentTerminal) finalLines() []string {
	return strings.Split(strings.TrimRight(xansi.Strip(t.emu.Render()), "\n"), "\n")
}

// persistSessionCmd saves t's final frame as brn's run summary so the Agent
// tab still shows the agent's last screen after a baron restart — m.sessions
// lives only in memory, so this is the only thing that survives a process
// exit or a killed window.
func persistSessionCmd(deps Deps, brn string, t *agentTerminal) tea.Cmd {
	return func() tea.Msg {
		if deps.WriteRunSummary == nil || t == nil {
			return nil
		}
		_ = deps.WriteRunSummary(brn, t.finalLines())
		return nil
	}
}

// --- Prompt Mode's left pane (docs/PRD/crew-mode.md's v7 redesign) ---
//
// A tabbed, live tmux-attached chat session per active agent CLI, running
// bd-scoped in the main checkout at its current branch — not a bead
// worktree, not task-agent BEADS_DB isolation. Deliberately a FULLY
// separate registry (m.promptSessions, keyed by agent ID) and tick/resize
// chain from everything above: never touches m.sessions/m.diffSessions,
// termKind/currentKind/tabIndexFor (both hard-coded to exactly two kinds),
// or the tickScheduled/termTick chain. The low-level pty/emulator/keystroke
// plumbing above (startPtyEmulator, pump/pumpReplies, resize, kill,
// sendKeys) is fully generic and reused as-is.

// spawnPromptAgentTerminal starts agentID's interactive chat session for
// Prompt Mode's left pane. Unlike spawnAgentTerminal, this deliberately does
// NOT go through Deps.AgentHost.EnsureRunning: that interface's concrete
// implementation (internal/cli's tmuxAgentHost) unconditionally wraps argv
// in task-agent env (domain.TaskAgentEnv), which would silently block the
// real bd access this pane exists for. Deps.PromptAgentEnsure does its own
// tmux Spawn instead (mirroring internal/cli's launchPersona — repo root,
// no worktree, bd-scoped permission extras, no task-agent isolation),
// returning just the window name; AttachArgv (generic, no env-baking) gets
// the local pty-attach command from there, same as spawnAgentTerminal's
// AgentHost path.
