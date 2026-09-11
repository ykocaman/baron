package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m Model) allSessions() []*agentTerminal {
	all := make([]*agentTerminal, 0, len(m.sessions)+len(m.diffSessions))
	for _, t := range m.sessions {
		all = append(all, t)
	}
	for _, t := range m.diffSessions {
		all = append(all, t)
	}
	return all
}

// sessionsFor returns the live-session registry for kind — m.sessions (the
// Agent tab) or m.diffSessions (the Diff tab) — so kind-generic code
// doesn't need to branch on kind itself; the returned map is the live one
// (a reference), safe to both read and write through.
func (m Model) sessionsFor(kind termKind) map[string]*agentTerminal {
	if kind == kindDiff {
		return m.diffSessions
	}
	return m.sessions
}

// currentKind reports which kind the active detail tab hosts — kindDiff for
// the Diff tab, kindAgent for every other tab (including ones with no
// terminal at all, where the distinction is moot).
func (m Model) currentKind() termKind {
	if m.detailTab == 2 {
		return kindDiff
	}
	return kindAgent
}

// tabIndexFor returns the detail-tab index a kind's session lives in —
// 1 for the Agent tab, 2 for the Diff tab — the inverse of currentKind.
func tabIndexFor(kind termKind) int {
	if kind == kindDiff {
		return 2
	}
	return 1
}

// termTick arms the frame tick while any embedded session (either kind) is
// still live; once every session has exited the tick stops and the tabs
// freeze on their final frames.
//
// At most one tick chain may exist at a time — see Model.tickScheduled for
// why a second one is unrecoverable. Callers therefore get a nil cmd (a
// no-op) whenever a chain is already running, which makes termTick safe to
// call from anywhere that merely wants to be sure the frame is animating
// (afterSelect, tabCmd, embeddedSpawnCmd) without having to know whether
// something else already started it. The tickMsg handler is the one caller
// that must always re-arm, and it does so by clearing tickScheduled first.
func (m *Model) termTick() tea.Cmd {
	if m.tickScheduled {
		return nil
	}
	for _, t := range m.allSessions() {
		if t != nil && !t.done {
			m.tickScheduled = true
			return tea.Tick(termFrameInterval, func(time.Time) tea.Msg { return tickMsg{} })
		}
	}
	return nil
}

// termDims returns the embedded terminal's size in cells: the detail pane's
// inner dimensions, or the whole screen while zoomed. Unknown sizes fall
// back to 80x24 (tests).
func (m Model) termDims() (cols, rows int) {
	cols, rows = 80, 24
	if m.width > 0 {
		if m.termZoom {
			cols = max(20, m.width)
		} else {
			// Match viewSplitDetailPane's innerW exactly (border(2) +
			// padding(2) eaten from the box's total width) — sizing the pty
			// wider than what actually gets rendered silently clips the
			// emulator's trailing columns.
			_, right := splitPaneWidths(m.width)
			cols = max(20, right-4)
		}
	}
	if m.height > 0 {
		if m.termZoom {
			rows = max(4, m.height)
		} else {
			rows = max(4, m.detailPaneHeight())
		}
	}
	return cols, rows
}

// resizeSessions fits every live session's pty and emulator to the pane.
// Focus no longer changes the budget — the old focus banner that ate a row
// from it is gone (see viewEmbeddedTerminal), replaced by the toast notice
// (focusNoticeText) — so every session sizes the same regardless of focus.
func (m Model) resizeSessions() {
	cols, rows := m.termDims()
	for _, t := range m.allSessions() {
		if t != nil {
			t.resize(cols, rows)
		}
	}
}

// sessionFor returns the selected bead's terminal of kind, nil when it has
// none.
func (m Model) sessionFor(kind termKind) *agentTerminal {
	if m.detail.BRN == "" {
		return nil
	}
	return m.sessionsFor(kind)[string(m.detail.BRN)]
}

// paneCopyText returns the selected bead's kind pane as plain text for the
// 'y' clipboard shortcut: a live session's current frame (ANSI stripped,
// same conversion finalLines uses for persistence — never raw SGR codes
// pasted into whatever the user pastes into), or a finished session's
// persisted summary when nothing is running. label names what got copied
// for the confirmation toast. ok is false when there's genuinely nothing to
// copy yet (bead has no session and no persisted summary either).
func (m Model) paneCopyText(kind termKind) (text, label string, ok bool) {
	brn := string(m.detail.BRN)
	if brn == "" {
		return "", "", false
	}
	label = "terminal output"
	if kind == kindDiff {
		label = "diff"
	}
	if t := m.sessionsFor(kind)[brn]; t != nil && !t.done {
		return strings.Join(t.finalLines(), "\n"), label, true
	}
	if p := m.panesFor(kind)[brn]; p != nil && len(p.summary) > 0 {
		return strings.Join(p.summary, "\n"), label, true
	}
	return "", "", false
}

// embeddedSpawnCmd starts (or re-shows) the bead's terminal of kind: the
// matching detail tab is focused, and a re-shown live session restores its
// focus/zoom state instead of duplicating the process. The spawn reads the
// bead straight out of m.beads (no fresh store fetch — see spawnAgentTerminal
// and spawnDiffTerminal's own doc comments), so callers right after an
// assign must wait for a beadsLoadedMsg to land first (see
// pendingRunAfterAssign) rather than call this straight from commandRanMsg's
// handler — m.beads there still predates the assign.
func (m *Model) embeddedSpawnCmd(brn string, kind termKind) tea.Cmd {
	m.detailTab = tabIndexFor(kind)
	m.liveBRN = brn
	cmds := []tea.Cmd{loadBeads(m.ctx, m.deps)}
	if t := m.sessionsFor(kind)[brn]; t != nil && !t.done {
		m.termFocus, m.termZoom = t.focus, t.zoom
	} else if !m.spawning[spawnKey(brn, kind)] {
		m.spawning[spawnKey(brn, kind)] = true
		cols, rows := m.termDims()
		if kind == kindDiff {
			cmds = append(cmds, spawnDiffTerminal(m.ctx, m.deps, m.beads, brn, cols, rows))
		} else {
			cmds = append(cmds, spawnAgentTerminal(m.ctx, m.deps, m.beads, brn, cols, rows, ""))
		}
	}
	cmds = append(cmds, m.termTick())
	return tea.Batch(cmds...)
}

// spawnKey identifies one (bead, kind) session slot, for Model.spawning.
func spawnKey(brn string, kind termKind) string {
	return brn + "\x00" + string(kind)
}

// paneHistoryLines is how far back a capture reaches. tmux's own default
// history-limit is 2000 lines, so asking for more just returns what exists;
// this is a bound on how much text is copied per refresh, not a promise.
const paneHistoryLines = 2000

// paneHistoryMaxAge is how long a capture is served before a scroll refreshes
// it. Short enough that scrolling back into a still-running agent's output
// picks up what it has printed since, long enough that holding a scroll key
// does not shell out to tmux on every repeat.
const paneHistoryMaxAge = 2 * time.Second

// paneHistoryMsg carries a captured pane scrollback back to the update loop.
