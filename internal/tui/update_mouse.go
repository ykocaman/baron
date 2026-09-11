package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.width <= 0 {
		return m, nil
	}
	if m.screen == screenCrew {
		return m.updatePromptMouse(msg)
	}
	if m.screen != screenDashboard {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		return m.handleDashboardWheel(msg)
	case tea.MouseClickMsg:
		return m.handleDashboardClick(msg)
	}
	return m, nil
}

// handleDashboardWheel handles a mouse-wheel event on the split-pane
// dashboard: the shell output panel (when visible), then the list pane or
// the detail pane depending on which side of the split the cursor is over.
func (m Model) handleDashboardWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	leftW, _ := splitPaneWidths(m.width)
	mouse := msg.Mouse()
	// Scroll the shell output panel when it's visible and the mouse is
	// in the lower third of the screen (where the panel lives).
	if len(m.shellOutput) > 0 && mouse.Y > m.height*2/3 {
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.scrollShellOutput(1)
		case tea.MouseWheelDown:
			m.scrollShellOutput(-1)
		}
		return m, nil
	}
	if mouse.X < leftW {
		return m.handleListPaneWheel(mouse)
	}
	return m.handleDetailPaneWheel(mouse)
}

// handleListPaneWheel scrolls the dashboard's left-hand bead list by 3 rows.
func (m Model) handleListPaneWheel(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	rows := m.currentRows()
	switch mouse.Button {
	case tea.MouseWheelUp:
		target := max(0, m.listCursor-3)
		if target == m.listCursor {
			return m, nil
		}
		m.listCursor = target
		m.selectListRow()
		return m, m.afterSelect()
	case tea.MouseWheelDown:
		target := min(len(rows)-1, m.listCursor+3)
		if target == m.listCursor {
			return m, nil
		}
		m.listCursor = target
		m.selectListRow()
		return m, m.afterSelect()
	}
	return m, nil
}

// handleDetailPaneWheel scrolls the dashboard's right-hand detail pane — the
// live agent/diff session pane on the Terminal/Diff tabs, or the plain
// detail viewport otherwise.
func (m Model) handleDetailPaneWheel(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if m.detailTab == 1 || m.detailTab == 2 {
		// Built before the return: a return statement's operands are
		// evaluated left to right, so `return m, m.scroll...()` copies m
		// before the call and discards whatever it sets — the notice
		// among it.
		cmd := m.scrollSessionPane(mouse.Button, 1)
		return m, cmd
	}
	m.syncDetailVP()
	switch mouse.Button {
	case tea.MouseWheelUp:
		m.detailVP.ScrollUp(1)
	case tea.MouseWheelDown:
		m.detailVP.ScrollDown(1)
	}
	return m, nil
}

// handleDashboardClick handles a mouse-click event on the split-pane
// dashboard, region by region from top to bottom: the left pane's tab bar,
// its sub-tab bar, the right pane's detail-tab bar, and finally the left
// pane's bead list rows.
func (m Model) handleDashboardClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	leftW, _ := splitPaneWidths(m.width)
	// MouseClickMsg is the press event; a release adds nothing here.
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return m, nil
	}

	// ── Left pane: tab bar row (Y=1, Y=2) ────────────────────────────
	// Expand hitbox to include the top border (Y=1) just in case the terminal
	// reports coordinates slightly off or the user clicks the edge.
	if (mouse.Y == 1 || mouse.Y == 2) && mouse.X < leftW {
		return m.handleLeftTabBarClick(mouse.X)
	}

	// ── Left pane: sub-tab bar row (Y=3) ─────────────────────────────
	if mouse.Y == 3 && mouse.X < leftW {
		return m.handleLeftSubTabBarClick(mouse.X)
	}

	// ── Right pane: detail-tab bar row (Y=3, Y=4) ────────────────────
	// Expand hitbox to include the status line (Y=3) or tabs (Y=4).
	if (mouse.Y == 3 || mouse.Y == 4) && mouse.X > leftW {
		return m.handleRightTabBarClick(mouse.X, leftW)
	}

	// ── Left pane: bead list rows (Y ≥ 4) ────────────────────────────
	if mouse.X >= leftW || mouse.Y < 4 {
		return m, nil
	}
	return m.handleListRowClick(mouse.Y)
}

// handleLeftTabBarClick handles a click on the dashboard's left-pane tab bar
// (updateMouse's Y=1/Y=2 region).
func (m Model) handleLeftTabBarClick(x int) (tea.Model, tea.Cmd) {
	if idx := m.leftTabClickIdx(x); idx >= 0 {
		m.listTab = idx
		m.listSubTab = -1
		m.listCursor, m.listScroll = 0, 0
		m.selectListRow()
	}
	return m, m.afterSelect()
}

// handleLeftSubTabBarClick handles a click on the dashboard's left-pane
// sub-tab bar (updateMouse's Y=3 region); clicking the active sub-tab
// toggles it off.
func (m Model) handleLeftSubTabBarClick(x int) (tea.Model, tea.Cmd) {
	if idx := m.subTabClickIdx(x); idx >= 0 {
		if m.listSubTab == idx {
			m.listSubTab = -1
		} else {
			m.listSubTab = idx
		}
		m.listCursor, m.listScroll = 0, 0
		m.selectListRow()
	}
	return m, m.afterSelect()
}

// handleRightTabBarClick handles a click on the dashboard's right-pane
// detail-tab bar (updateMouse's Y=3/Y=4 region, right of leftW).
func (m Model) handleRightTabBarClick(x, leftW int) (tea.Model, tea.Cmd) {
	if idx := m.rightTabClickIdx(x, leftW); idx >= 0 {
		m.detailTab = idx
		m.detailVP.SetYOffset(0)
		return m, m.tabCmd()
	}
	// If they clicked the status line but missed a tab X-coord, just ignore.
	return m, nil
}

// handleListRowClick handles a click on one of the dashboard's left-pane
// bead list rows (updateMouse's Y >= 4 region), given the absolute screen Y.
func (m Model) handleListRowClick(y int) (tea.Model, tea.Cmd) {
	rows := m.currentRows()
	avail := m.splitPaneRows()
	start := min(m.listScroll, max(0, len(rows)-avail))
	idx := start + (y - 4)
	if idx >= 0 && idx < len(rows) && idx < start+avail {
		m.listCursor = idx
		m.selectListRow()
		return m, m.afterSelect()
	}
	return m, nil
}

// updatePromptMouse handles mouse click and wheel events in Prompt Mode (screenCrew).
func (m Model) updatePromptMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		return m.handlePromptWheel(msg)
	case tea.MouseClickMsg:
		return m.handlePromptClick(msg)
	}
	return m, nil
}

// handlePromptWheel dispatches a Prompt Mode mouse-wheel event to whichever
// of the 3 boxes (left agent chat, right-top list, right-bottom detail) the
// cursor is over.
func (m Model) handlePromptWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	leftW, _ := promptPaneWidths(m.width)
	topBoxRows := m.promptRightTopRows()
	mouse := msg.Mouse()
	if mouse.X < leftW {
		return m.handlePromptLeftBoxWheel(mouse)
	}
	if mouse.Y <= topBoxRows+2 {
		return m.handlePromptRightTopWheel(mouse)
	}
	return m.handlePromptRightBottomWheel(mouse)
}

// handlePromptLeftBoxWheel scrolls the left pane's live agent chat scrollback.
func (m Model) handlePromptLeftBoxWheel(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	m.promptFocus = promptPaneLeft
	id := m.activePromptAgentID()
	t := m.promptSessions[id]
	if t == nil || t.tmuxWindow == "" {
		return m, nil
	}
	bodyRows := max(1, m.splitBoxInnerRows()-3) // title + tabBar + blank
	hi := historyMaxOffset(len(t.history), bodyRows)
	switch mouse.Button {
	case tea.MouseWheelUp:
		if t.history == nil {
			m.promptTermScrollOffset += 3
		} else {
			m.promptTermScrollOffset = clampInt(m.promptTermScrollOffset+3, hi)
		}
	case tea.MouseWheelDown:
		m.promptTermScrollOffset = clampInt(m.promptTermScrollOffset-3, hi)
	}
	if m.promptTermScrollOffset > 0 && m.deps.AgentHost != nil && t.needsHistory() {
		t.capturing = true
		return m, paneHistoryCmd(m.ctx, m.deps.AgentHost, t.brn, t.kind, t.tmuxWindow)
	}
	return m, nil
}

// handlePromptRightTopWheel moves the cursor in the right-top box's active
// tab (Personas' selection cursor, or the Beads list cursor).
func (m Model) handlePromptRightTopWheel(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	m.promptFocus = promptPaneRightTop
	if m.promptRightTab == promptTabPersonas {
		dir := 1
		if mouse.Button == tea.MouseWheelUp {
			dir = -1
		}
		return m.movePromptRightTopCursor(dir)
	}
	// Beads tab
	rows := m.promptBeadRows()
	if len(rows) == 0 {
		return m, nil
	}
	if mouse.Button == tea.MouseWheelUp {
		m.promptBeadCursor = max(0, m.promptBeadCursor-1)
	} else {
		m.promptBeadCursor = min(len(rows)-1, m.promptBeadCursor+1)
	}
	m.clampPromptBeadScroll()
	return m, m.promptBeadDetailCmd()
}

// handlePromptRightBottomWheel scrolls the right-bottom box (persona detail
// or bead detail, depending on the active tab).
func (m Model) handlePromptRightBottomWheel(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	m.promptFocus = promptPaneRightBottom
	if m.promptRightTab == promptTabPersonas {
		if mouse.Button == tea.MouseWheelUp {
			m.promptPersonaDetailScroll = max(0, m.promptPersonaDetailScroll-3)
		} else {
			m.promptPersonaDetailScroll += 3
		}
		return m, nil
	}
	m.syncPromptBeadVP()
	if mouse.Button == tea.MouseWheelUp {
		m.detailVP.ScrollUp(3)
	} else {
		m.detailVP.ScrollDown(3)
	}
	return m, nil
}

// handlePromptClick dispatches a Prompt Mode left-click to the left pane or
// the right column.
func (m Model) handlePromptClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	leftW, _ := promptPaneWidths(m.width)
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return m, nil
	}
	if mouse.X < leftW {
		return m.handlePromptLeftPaneClick(mouse)
	}
	return m.handlePromptRightColumnClick(mouse, leftW)
}

// handlePromptLeftPaneClick handles a click in the left pane (agent chat):
// the agent tab bar first, then falling back to focusing the terminal body.
func (m Model) handlePromptLeftPaneClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	m.promptFocus = promptPaneLeft
	// Y=2..4: Agent Tab Bar (" agy  claude  gemini  opencode ")
	if (mouse.Y == 2 || mouse.Y == 3 || mouse.Y == 4) && len(m.promptAgentIDs) > 0 {
		next, handled := m.handlePromptAgentTabClick(mouse.X)
		m = asModel(next)
		if handled {
			return m, nil
		}
	}
	// Click on terminal body area → activate keyboard focus if session exists.
	if !m.promptTermFocus {
		id := m.activePromptAgentID()
		if t := m.promptSessions[id]; t != nil && !t.done {
			m.promptTermFocus = true
			t.focus = true
		}
	}
	return m, nil
}

// handlePromptAgentTabClick resolves a click on the left pane's agent tab
// bar to a tab index from an absolute screen X. Returns handled=true (with
// promptAgentTab/scroll/focus already applied) when the click landed on a
// specific tab — the caller returns immediately in that case, matching this
// function's own early return. Returns handled=false when x fell before the
// tab bar's start (leaving state untouched) or past its end (defaulting to
// the last tab), so the caller falls through to the terminal-body click.
func (m Model) handlePromptAgentTabClick(x int) (tea.Model, bool) {
	innerX := x - 2
	if innerX < 0 {
		return m, false
	}
	offset := 0
	for i, id := range m.promptAgentIDs {
		w := len(id) + 2
		if innerX < offset+w {
			m.promptAgentTab = i
			m.promptTermScrollOffset = 0
			if t := m.promptSessions[m.promptAgentIDs[m.promptAgentTab]]; t != nil && !t.done {
				m.promptTermFocus = true
				t.focus = true
			}
			return m, true
		}
		offset += w
	}
	m.promptAgentTab = len(m.promptAgentIDs) - 1
	m.promptTermScrollOffset = 0
	return m, false
}

// handlePromptRightColumnClick dispatches a click in the right column to the
// right-top box (tab bar, beads list, personas list) or the right-bottom
// detail box.
func (m Model) handlePromptRightColumnClick(mouse tea.Mouse, leftW int) (tea.Model, tea.Cmd) {
	innerRightX := mouse.X - leftW - 2 // inner X relative to right box
	topBoxRows := m.promptRightTopRows()
	if mouse.Y <= topBoxRows+2 {
		return m.handlePromptRightTopClick(mouse.Y, innerRightX)
	}
	m.promptFocus = promptPaneRightBottom
	return m, nil
}

// handlePromptRightTopClick handles a click in the right-top box: its own
// Beads/Personas tab bar first, then the active tab's own click regions.
func (m Model) handlePromptRightTopClick(y, innerRightX int) (tea.Model, tea.Cmd) {
	m.promptFocus = promptPaneRightTop

	// Y=1, Y=2: Top Tab Bar (Beads vs Personas)
	if (y == 1 || y == 2) && innerRightX >= 0 {
		// Beads label is ~8 chars (" Beads "), Personas is ~11 chars (" Personas ")
		if innerRightX < 9 {
			m.promptRightTab = promptTabBeads
			return m, m.promptBeadDetailCmd()
		}
		m.promptRightTab = promptTabPersonas
		return m, nil
	}

	if m.promptRightTab == promptTabBeads {
		return m.handlePromptBeadsTabClick(y, innerRightX)
	}
	if m.promptRightTab == promptTabPersonas {
		return m.handlePromptPersonasTabClick(y)
	}
	return m, nil
}

// handlePromptBeadsTabClick handles a click within the right-top box's Beads
// tab: its category tab bar, sub-tab bar, or bead list rows.
func (m Model) handlePromptBeadsTabClick(y, innerRightX int) (tea.Model, tea.Cmd) {
	if y == 3 {
		return m.promptBeadCategoryClick(innerRightX)
	}
	if y == 4 {
		return m.promptBeadSubTabClick(innerRightX)
	}
	return m.promptBeadRowClick(y)
}

func (m Model) promptBeadCategoryClick(x int) (tea.Model, tea.Cmd) {
	if x < 0 {
		return m, nil
	}
	idx := m.tabClickIdx(x)
	if idx < 0 {
		return m, nil
	}
	m.promptBeadTab, m.promptBeadSubTab, m.promptBeadCursor, m.promptBeadScroll = idx, -1, 0, 0
	return m, m.promptBeadDetailCmd()
}

func (m Model) promptBeadSubTabClick(x int) (tea.Model, tea.Cmd) {
	if x < 0 {
		return m, nil
	}
	idx := m.subTabClickIdxFor(x-2, m.promptBeadTab)
	if idx < 0 {
		return m, nil
	}
	if m.promptBeadSubTab == idx {
		m.promptBeadSubTab = -1
	} else {
		m.promptBeadSubTab = idx
	}
	m.promptBeadCursor, m.promptBeadScroll = 0, 0
	return m, m.promptBeadDetailCmd()
}

func (m Model) promptBeadRowClick(y int) (tea.Model, tea.Cmd) {
	if y < 5 {
		return m, nil
	}
	start, avail := m.promptBeadScroll, max(1, m.promptRightTopListRows())
	idx := start + y - 5
	rows := m.promptBeadRows()
	if idx < 0 || idx >= len(rows) || idx >= start+avail {
		return m, nil
	}
	m.promptBeadCursor = idx
	return m, m.promptBeadDetailCmd()
}

// handlePromptPersonasTabClick handles a click within the right-top box's
// Personas tab: its (possibly accordion-shifted) persona list rows.
func (m Model) handlePromptPersonasTabClick(y int) (tea.Model, tea.Cmd) {
	// Y >= 4: Persona List Rows (Y=3 is blank spacer line)
	if y >= 4 {
		flat := m.promptPersonaFlatRows()
		avail := max(1, m.promptRightTopListRows())
		start := clampInt(m.promptPersonaScroll, max(0, len(flat)-1))
		// Walk the exact same line sequence viewPromptPersonaList rendered
		// (see promptPersonaVisibleLines) — a click resolves to whichever
		// flat row actually drew at this screen line, not a naive
		// start+offset index, since an open-but-empty accordion's
		// placeholder line shifts every later row down by one screen line
		// without being itself a flat row.
		lines := m.promptPersonaVisibleLines(start, avail)
		li := y - 4
		if li >= 0 && li < len(lines) && lines[li].FlatIdx >= 0 {
			m.promptPersonaCursor = lines[li].FlatIdx
		}
	}
	return m, nil
}

// tabClickIdx returns which boardColumns tab index was clicked given an inner X coordinate.
func (m Model) tabClickIdx(innerX int) int {
	if innerX < 0 {
		return -1
	}
	offset := 0
	for i, col := range boardColumns {
		label := fmt.Sprintf("%s %d", col.name, len(m.bucketRows(i, -1)))
		w := 2 + len(label) + 1 // "▌ "/indent(2) + label + " " separator
		if innerX < offset+w {
			return i
		}
		offset += w
	}
	return len(boardColumns) - 1
}

// subTabClickIdxFor returns which subTab index was clicked given an inner X coordinate and tab index.
func (m Model) subTabClickIdxFor(innerX int, tabIdx int) int {
	if innerX < 0 || tabIdx < 0 || tabIdx >= len(boardColumns) {
		return -1
	}
	statuses := boardColumns[tabIdx].statuses
	offset := 0
	for i, st := range statuses {
		label := fmt.Sprintf("%s %d", bucketLabel(st), len(m.bucketRows(tabIdx, i)))
		w := len(label)
		if innerX < offset+w {
			return i
		}
		offset += w + 2 // "  " separator between pills
	}
	return len(statuses) - 1
}

// leftTabClickIdx returns which listTab index was clicked given an absolute
// screen X within the left pane. Returns -1 when the click misses all tabs.
// Each tab renders as (cursor/indent 2 cells) + label + (1 space separator).
func (m Model) leftTabClickIdx(x int) int {
	return m.tabClickIdx(x - 1)
}

// subTabClickIdx returns which listSubTab index was clicked given an absolute
// screen X within the left pane. Returns -1 when the click misses all pills.
// The sub-tab bar renders as "  " indent then pills separated by "  ".
func (m Model) subTabClickIdx(x int) int {
	return m.subTabClickIdxFor(x-3, m.listTab)
}

// rightTabClickIdx returns which detailTab index (see detailTabs) was
// clicked given the absolute screen X and the left-pane width. The detail
// tab bar renders inside the right box starting after its left border;
// tabs are separated by two spaces. Each rendered cell is len(name)+2 wide
// whether active (brackets replace the padding spaces) or not, so the
// hit-test width comes straight from detailTabs instead of a separately
// maintained literal table.
func (m Model) rightTabClickIdx(x, leftW int) int {
	// Right box left border: leftW (" " sep) + 1 (box left border) + 1 (padding)
	innerX := x - leftW - 3
	if innerX < 0 {
		return -1
	}
	offset := 0
	for i, t := range detailTabs {
		w := len(t) + 2
		if innerX < offset+w {
			return i
		}
		offset += w + 2 // "  " separator
	}
	return detailTabCount - 1 // click past the last tab → the last tab
}

// scrollSessionPane scrolls the active Terminal/Diff tab by n lines, toward
// tea.MouseWheelUp (older) or tea.MouseWheelDown (newer). The mouse wheel,
// pgup/pgdown and shift+up/down all funnel through here so the cases below
// can never drift out of sync with what is on screen.
//
// BARON keeps no scrollback of its own. Its emulator holds only the current
// frame, so where a session's backlog comes from depends on how it is hosted:
//
//   - tmux-backed: tmux owns the pane and keeps its history. Read it with
//     capture-pane (scrollPaneHistory). BARON's own emulator is a mirror here
//     — the pty is a `tmux attach-session` client receiving redraws of the
//     current pane — so scrolling that would show fragments, not the
//     transcript.
//   - tmux-backed with no backlog yet, or a child that paints its own screen:
//     nothing has recorded that history, so hand the gesture to the child,
//     which may scroll itself.
//   - The Diff tab, which is deliberately local (a tmux-hosted diff viewer per
//     bead is what accumulated hundreds of sessions): its pager scrolls
//     itself, so forward to it.
//   - No tmux at all: there is no record anywhere. Say so rather than appear
//     to scroll. Keeping a second copy purely for this case is what a patched
//     fork of the terminal emulator used to be for; dropping both is lighter
//     and more honest.
//
// A persisted summary (no live session) scrolls its own viewport instead,
// re-synced here first exactly like syncDetailVP: Update can run key events
// between renders, so the viewport's last-known content could be stale.
func (m *Model) scrollSessionPane(dir tea.MouseButton, n int) tea.Cmd {
	kind := m.currentKind()
	p := m.paneFor(m.liveBRN, kind)
	if t := m.sessionsFor(kind)[m.liveBRN]; t != nil {
		// Scrollback comes from the host, and tmux is the only host there is.
		// A tmux-backed session's own emulator is a mirror — the pty is an
		// attach client receiving redraws of the current pane — so the pane's
		// history has to be read from tmux (see scrollPaneHistory). Without
		// tmux there is no such record anywhere and BARON keeps none of its
		// own, so say that plainly rather than appear to scroll.
		if t.tmuxWindow != "" {
			// No backlog to page through (the pane has not scrolled yet, or
			// the child paints its own screen and so nothing records its
			// history): hand the gesture to the child, which may scroll
			// itself. Re-checked each time, since an agent with nothing to
			// show yet will have plenty later.
			if t.historyUnavailable {
				t.forwardScroll(dir, n)
				return m.recheckHistoryCmd(t)
			}
			return m.scrollPaneHistory(t, p, dir, n)
		}
		// The Diff tab is deliberately not tmux-backed (a tmux-hosted diff
		// viewer per bead was what accumulated hundreds of sessions), but it
		// runs a pager that scrolls itself, so the gesture goes to it.
		// Verified against hunk: PageUp/PageDown both move its content.
		if kind == kindDiff {
			t.forwardScroll(dir, n)
			return nil
		}
		return m.notify("scrollback needs tmux — install tmux and restart baron to page back through this agent's output")
	}
	content := m.contentFor(m.liveBRN, kind)
	_, right := splitPaneWidths(m.width)
	w, h := vpSize(max(10, right-4), m.detailPaneHeight(), m.height <= 0, content)
	p.vp.SetWidth(w)
	p.vp.SetHeight(h)
	syncPaneContent(p, content)
	switch dir {
	case tea.MouseWheelUp:
		p.vp.ScrollUp(n)
	case tea.MouseWheelDown:
		p.vp.ScrollDown(n)
	}
	return nil
}

// scrollPaneHistory moves through a tmux-backed session's captured pane
// scrollback, refreshing the capture when it is missing or stale.
//
// The clamp uses whatever has been captured so far, so the very first scroll
// up (before any capture has returned) still moves by a page rather than
// doing nothing visible — the refresh lands a moment later and the offset is
// re-clamped against the real length on the next render.
func (m *Model) scrollPaneHistory(t *agentTerminal, p *agentPane, dir tea.MouseButton, n int) tea.Cmd {
	hi := historyMaxOffset(len(t.history), m.detailPaneHeight())
	switch dir {
	case tea.MouseWheelUp:
		if t.history == nil {
			// Nothing captured yet: move anyway so the very first scroll is
			// not a dead key, and let the capture's own clamp correct it when
			// it lands (see the paneHistoryMsg handler).
			p.offset += n
		} else {
			p.offset = clampInt(p.offset+n, hi)
		}
	case tea.MouseWheelDown:
		p.offset = clampInt(p.offset-n, hi)
	}
	if p.offset == 0 || m.deps.AgentHost == nil || !t.needsHistory() {
		return nil
	}
	t.capturing = true
	return paneHistoryCmd(m.ctx, m.deps.AgentHost, t.brn, t.kind, t.tmuxWindow)
}

// recheckHistoryCmd re-reads the pane's backlog for a session currently
// believed to have none, rate-limited the same way a normal refresh is.
//
// Without this the "no backlog" finding would be permanent, and a scroll
// attempted before the agent had printed anything would leave scrolling
// broken for the rest of that session — the exact shape of the original bug
// report, arrived at from the other direction.
func (m *Model) recheckHistoryCmd(t *agentTerminal) tea.Cmd {
	if m.deps.AgentHost == nil || t.capturing || time.Since(t.capturedAt) <= paneHistoryMaxAge {
		return nil
	}
	t.capturing = true
	return paneHistoryCmd(m.ctx, m.deps.AgentHost, t.brn, t.kind, t.tmuxWindow)
}

// historyMaxOffset is how far back a captured scrollback of total lines can be
// scrolled in a pane of paneH rows: far enough that its oldest line sits at the
// top of the window. One row of the pane goes to the "showing scrollback"
// marker (see viewCapturedHistory), so the window itself is paneH-1 lines —
// counting it as paneH would leave the first line permanently unreachable.
func historyMaxOffset(total, paneH int) int {
	return max(0, total-historyWindow(paneH))
}

// historyWindow is how many history lines fit in a pane of paneH rows.
func historyWindow(paneH int) int {
	return max(1, paneH-1)
}

// clampInt clamps v into [0, hi] — every call site clamps a scroll
// offset/cursor, which is never negative.
func clampInt(v, hi int) int {
	return min(max(v, 0), hi)
}

// scrollShellOutput adjusts m.shellOutputScroll by delta, clamped to the
// valid range for the currently buffered shell output (a no-op when
// nothing is buffered) — shared by the command bar's shift+up/down keys
// (updateCommandBar) and the dashboard's mouse-wheel-over-the-shell-panel
// case (updateMouse).
func (m *Model) scrollShellOutput(delta int) {
	if len(m.shellOutput) == 0 {
		return
	}
	maxScroll := max(0, len(m.shellOutput)-min(shellOutputMaxLines, len(m.shellOutput)))
	m.shellOutputScroll = clampInt(m.shellOutputScroll+delta, maxScroll)
}

// nextSelectable walks rows from cur in direction dir (1 = down, -1 = up)
// to the next row that has a bead (note rows are skipped). cur outside the
// slice means "just before" (-1 down / len(rows) up). The result is clamped
// to the first/last bead row so j/k never walk the cursor past the list.
