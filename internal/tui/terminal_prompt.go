package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	tea "charm.land/bubbletea/v2"
)

// msgAgentChatNotWiredUp is shown both as the spawn error and as the Prompt
// Mode empty state when the host app hasn't supplied the agent-CLI deps.
const msgAgentChatNotWiredUp = "agent chat isn't wired up"

func spawnPromptAgentTerminal(ctx context.Context, deps Deps, agentID string, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		if cols <= 0 {
			cols = 80
		}
		if rows <= 0 {
			rows = 24
		}
		if deps.PromptAgentEnsure == nil || deps.AgentHost == nil {
			return promptAgentSpawnedMsg{agentID: agentID, err: errors.New(msgAgentChatNotWiredUp)}
		}
		window, _, err := deps.PromptAgentEnsure(agentID)
		if err != nil {
			return promptAgentSpawnedMsg{agentID: agentID, err: err}
		}
		argv, err := deps.AgentHost.AttachArgv(ctx, window)
		if err != nil {
			return promptAgentSpawnedMsg{agentID: agentID, err: err}
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		f, emu, err := startPtyEmulator(cmd, cols, rows)
		if err != nil {
			return promptAgentSpawnedMsg{agentID: agentID, err: err}
		}
		t := &agentTerminal{
			brn: agentID, kind: kindAgent, cmd: cmd, pty: f, emu: emu,
			exitCh: make(chan struct{}), tmuxWindow: window,
			cols: cols, rows: rows, startedAt: time.Now(),
		}
		go t.pump()
		go t.pumpReplies()
		return promptAgentSpawnedMsg{agentID: agentID, t: t}
	}
}

// activePromptAgentID returns the agent ID the left pane's active tab
// points at, "" if there are none.
func (m Model) activePromptAgentID() string {
	if m.promptAgentTab < 0 || m.promptAgentTab >= len(m.promptAgentIDs) {
		return ""
	}
	return m.promptAgentIDs[m.promptAgentTab]
}

// focusPromptAgent drives the left pane's 't'/enter: focuses the active
// tab's already-running session, or spawns it first (lazily — only the
// tab actually visited ever starts a tmux window, mirroring Board Mode's
// own lazy-spawn-on-tab-select pattern for its Agent tab).
func (m Model) focusPromptAgent() (tea.Model, tea.Cmd) {
	id := m.activePromptAgentID()
	if id == "" {
		return m, nil
	}
	if t := m.promptSessions[id]; t != nil {
		if !t.done {
			m.promptTermFocus = true
			t.focus = true
		}
		return m, nil
	}
	if m.deps.PromptAgentEnsure == nil {
		return m, m.notify("agent chat isn't wired up")
	}
	cols, rows := m.promptTermDims()
	return m, spawnPromptAgentTerminal(m.ctx, m.deps, id, cols, rows)
}

// handlePromptAgentSpawned registers a freshly spawned left-pane session
// and, if its tab is still the one on screen (a slow spawn racing a tab
// switch is possible), focuses it immediately — the whole point of
// pressing 't' was to start chatting, not to land back on an unfocused pane.
func (m Model) handlePromptAgentSpawned(msg promptAgentSpawnedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("agent chat %s: %w", msg.agentID, msg.err))
	}
	if m.promptSessions == nil {
		m.promptSessions = map[string]*agentTerminal{}
	}
	m.promptSessions[msg.agentID] = msg.t
	if msg.agentID == m.activePromptAgentID() {
		m.promptTermFocus = true
		msg.t.focus = true
	}
	m.resizePromptSessions()
	return m, m.promptTermTick()
}

// handlePromptAgentExit marks a left-pane session done on process exit —
// checkPromptTmuxDone's tmux-backed detection path.
func (m Model) handlePromptAgentExit(msg promptAgentExitMsg) (tea.Model, tea.Cmd) {
	if t := m.promptSessions[msg.agentID]; t != nil {
		t.done = true
		t.exitErr = msg.err
	}
	return m, nil
}

// handlePromptTermTick drives the left pane's frame re-render while any
// session is live — checkPromptTmuxDone first (a tmux-backed session may
// have exited since the last tick), then re-arms via promptTermTick.
func (m Model) handlePromptTermTick() (tea.Model, tea.Cmd) {
	m.promptTermTickScheduled = false
	cmds := m.checkPromptTmuxDone()
	if cmd := m.promptTermTick(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// checkPromptTmuxDone scans every live left-pane session for
// domain.DoneSentinel, same pattern checkTmuxDone uses for Board Mode's own
// sessions, just over m.promptSessions.
func (m Model) checkPromptTmuxDone() []tea.Cmd {
	var cmds []tea.Cmd
	for id, t := range m.promptSessions {
		if t == nil || t.done || t.tmuxWindow == "" {
			continue
		}
		found, exitErr := t.doneResult()
		if !found {
			continue
		}
		t.done = true
		agentID := id
		cmds = append(cmds, func() tea.Msg { return promptAgentExitMsg{agentID: agentID, err: exitErr} })
	}
	return cmds
}

// promptTermTick arms the left pane's frame-tick chain — a fully separate
// chain from tickScheduled/termTick (see promptTermTickScheduled's doc
// comment), same tickScheduled-guard shape as termTick itself.
func (m *Model) promptTermTick() tea.Cmd {
	if m.promptTermTickScheduled {
		return nil
	}
	for _, t := range m.promptSessions {
		if t != nil && !t.done {
			m.promptTermTickScheduled = true
			return tea.Tick(termFrameInterval, func(time.Time) tea.Msg { return promptTermTickMsg{} })
		}
	}
	return nil
}

// promptTermDims returns the left pane's inner size in cells — matches
// viewPromptLeft's own innerW/bodyRows math (border+padding eaten from the
// box's total width, same convention termDims uses for Board Mode's pane).
func (m Model) promptTermDims() (cols, rows int) {
	cols, rows = 80, 24
	if m.width > 0 {
		left, _ := promptPaneWidths(m.width)
		cols = max(20, left-4)
	}
	if m.height > 0 {
		rows = max(4, m.splitBoxInnerRows()-3)
	}
	return cols, rows
}

// resizePromptSessions fits every live left-pane session's pty/emulator to
// the pane — same shape resizeSessions uses for Board Mode's own sessions.
func (m Model) resizePromptSessions() {
	cols, rows := m.promptTermDims()
	for _, t := range m.promptSessions {
		if t != nil {
			t.resize(cols, rows)
		}
	}
}
