package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

func nextSelectable(rows []splitRow, cur, dir int) int {
	i := cur + dir
	for i >= 0 && i < len(rows) && rows[i].bead.BRN == "" {
		i += dir
	}
	if i < 0 {
		return 0
	}
	if i >= len(rows) {
		for i = len(rows) - 1; i >= 0 && rows[i].bead.BRN == ""; i-- {
		}
		return max(0, i)
	}
	return i
}

func (m Model) updateSplitKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Any key other than ctrl+d cancels a pending delete confirmation so a
	// stray keystroke can never inadvertently confirm a close a moment later.
	if msg.String() != "ctrl+d" && m.deleteConfirmBRN != "" {
		m.deleteConfirmBRN = ""
		m.statusMsg = ""
	}
	switch msg.String() {
	// The split view's arrow model: a plain arrow acts on the left pane
	// (list sub-tabs, cursor), the same arrow with shift acts on the right
	// pane (detail tabs, scroll). shift+up/down already scrolled the detail
	// pane; shift+left/right complete the pairing and replace the tab/
	// shift+tab cycling, which named no pane at all.
	case "shift+right":
		return m.handleDetailTabNextKey()
	case "shift+left":
		return m.handleDetailTabPrevKey()
	case "[", "h":
		return m.handleListTabPrevKey()
	case "]", "l":
		return m.handleListTabNextKey()
	case "left":
		return m.handleSubTabPrevKey()
	case "right":
		return m.handleSubTabNextKey()
	case "1", "2", "3", "4":
		return m.handleListTabDigitKey(msg)
	case "j", "down":
		return m.handleListCursorDownKey()
	case "k", "up":
		return m.handleListCursorUpKey()
	case "g":
		return m.handleListCursorFirstKey()
	case "G":
		return m.handleListCursorLastKey()
	case "pgup", "pgdown":
		return m.handleDetailPageScrollKey(msg)
	case "shift+up", keyShiftDown:
		return m.handleDetailLineScrollKey(msg)
	case "ctrl+d":
		return m.handleSplitDeleteConfirmKey()
	case "n":
		return m.handleNewBeadKey()
	case "e":
		return m.handleEditBeadKey()
	case "c":
		return m.handleCommentKey()
	case "s":
		return m.handleStatusKey()
	case "x":
		return m.handleStopKey(msg)
	case "u":
		// Was 'e' — displaced when 'e' became "edit bead" (see that case's
		// doc comment). Mnemonic: expand comments = "unfold".
		m.commentsExpanded = !m.commentsExpanded
		return m, nil
	case "r":
		return m.handleRunKey()
	case "a":
		return m.handleAssignKey()
	case "y":
		return m.handleCopyKey()
	case "m":
		return m.handleMergeKey()
	case "t":
		return m.handleFocusKey()
	case "z":
		return m.handleZoomKey()
	}
	return m, nil
}

func (m Model) handleDetailTabNextKey() (tea.Model, tea.Cmd) {
	m.detailTab = (m.detailTab + 1) % detailTabCount
	m.detailVP.SetYOffset(0)
	return m, m.tabCmd()
}

func (m Model) handleDetailTabPrevKey() (tea.Model, tea.Cmd) {
	m.detailTab = (m.detailTab + detailTabCount - 1) % detailTabCount
	m.detailVP.SetYOffset(0)
	return m, m.tabCmd()
}

func (m Model) handleListTabPrevKey() (tea.Model, tea.Cmd) {
	m.listTab = (m.listTab + len(boardColumns) - 1) % len(boardColumns)
	m.listSubTab = -1
	m.listCursor, m.listScroll = 0, 0
	m.selectListRow()
	return m, m.afterSelect()
}

func (m Model) handleListTabNextKey() (tea.Model, tea.Cmd) {
	m.listTab = (m.listTab + 1) % len(boardColumns)
	m.listSubTab = -1
	m.listCursor, m.listScroll = 0, 0
	m.selectListRow()
	return m, m.afterSelect()
}

func (m Model) handleSubTabPrevKey() (tea.Model, tea.Cmd) {
	m.subTabPrev()
	m.listCursor, m.listScroll = 0, 0
	m.selectListRow()
	return m, m.afterSelect()
}

func (m Model) handleSubTabNextKey() (tea.Model, tea.Cmd) {
	m.subTabNext()
	m.listCursor, m.listScroll = 0, 0
	m.selectListRow()
	return m, m.afterSelect()
}

func (m Model) handleListTabDigitKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	tab := int(msg.String()[0] - '1')
	if tab < len(boardColumns) {
		m.listTab = tab
		m.listSubTab = -1
		m.listCursor, m.listScroll = 0, 0
		m.selectListRow()
	}
	return m, m.afterSelect()
}

func (m Model) handleListCursorDownKey() (tea.Model, tea.Cmd) {
	rows := m.currentRows()
	m.listCursor = nextSelectable(rows, m.listCursor, 1)
	m.selectListRow()
	return m, m.afterSelect()
}

func (m Model) handleListCursorUpKey() (tea.Model, tea.Cmd) {
	rows := m.currentRows()
	m.listCursor = nextSelectable(rows, m.listCursor, -1)
	m.selectListRow()
	return m, m.afterSelect()
}

func (m Model) handleListCursorFirstKey() (tea.Model, tea.Cmd) {
	rows := m.currentRows()
	if len(rows) > 0 {
		m.listCursor = nextSelectable(rows, -1, 1)
		m.selectListRow()
	}
	return m, m.afterSelect()
}

func (m Model) handleListCursorLastKey() (tea.Model, tea.Cmd) {
	rows := m.currentRows()
	if len(rows) > 0 {
		m.listCursor = nextSelectable(rows, len(rows), -1)
		m.selectListRow()
	}
	return m, m.afterSelect()
}

func (m Model) handleDetailPageScrollKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	dir := tea.MouseWheelDown
	if msg.String() == "pgup" {
		dir = tea.MouseWheelUp
	}
	if m.detailTab == 1 || m.detailTab == 2 { // Terminal / Diff tab
		cmd := m.scrollSessionPane(dir, 10)
		return m, cmd
	}
	m.syncDetailVP()
	if dir == tea.MouseWheelUp {
		m.detailVP.ScrollUp(10)
	} else {
		m.detailVP.ScrollDown(10)
	}
	return m, nil
}

func (m Model) handleDetailLineScrollKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	dir := tea.MouseWheelDown
	if msg.String() == "shift+up" {
		dir = tea.MouseWheelUp
	}
	if m.detailTab == 1 || m.detailTab == 2 { // Terminal / Diff tab
		cmd := m.scrollSessionPane(dir, 1)
		return m, cmd
	}
	m.syncDetailVP()
	if dir == tea.MouseWheelUp {
		m.detailVP.ScrollUp(1)
	} else {
		m.detailVP.ScrollDown(1)
	}
	return m, nil
}

// handleSplitDeleteConfirmKey is Board Mode's ctrl+D double-press: first
// press shows a confirmation notice; second press within 2s on the same
// bead confirms the close.
func (m Model) handleSplitDeleteConfirmKey() (tea.Model, tea.Cmd) {
	bead, ok := m.listSelection()
	if !ok {
		return m, nil
	}
	brn := string(bead.BRN)
	if m.deleteConfirmBRN == brn && time.Since(m.deleteConfirmAt) < 2*time.Second {
		// Second press in time → close/delete the bead.
		m.deleteConfirmBRN = ""
		m.statusMsg = ""
		return m, runTypedCommand(func() (string, error) { return m.deps.CloseBead(brn) })
	}
	// First press → arm the confirmation and show a notice.
	m.deleteConfirmBRN = brn
	m.deleteConfirmAt = time.Now()
	return m, tea.Batch(
		m.notify(fmt.Sprintf("ctrl+d again within 2s to close %s", brn)),
		tea.Tick(2*time.Second, func(time.Time) tea.Msg { return deleteConfirmExpiredMsg{} }),
	)
}

func (m Model) handleNewBeadKey() (tea.Model, tea.Cmd) {
	var prefillParent string
	if sel, ok := m.listSelection(); ok {
		// Prefill the selected epic as parent so creating its child is
		// one submit away.
		if sel.IssueType == "epic" {
			prefillParent = string(sel.BRN)
		}
	}
	m.formReturnScreen = screenDashboard
	m.formKind = formKindNewBead

	title, typ, tier, parent, desc, accept := "", "task", string(agent.TierFast), prefillParent, "", ""
	// No Placeholder here: huh renders the placeholder's first character
	// in the empty field's cursor position (v2.0.3), so "required"
	// painted a stray "r" into the Title field on open.
	titleField := huh.NewInput().
		Title("Title").
		CharLimit(500).
		Value(&title)
	typeField := huh.NewSelect[string]().
		Title("Type").
		Options(m.newBeadFormTypeOptions()...).
		Value(&typ)
	// Tier is mandatory (see store.Bead.Tier) and defaults to fast —
	// the cheapest/quickest tier — rather than opening on whichever
	// option the list happens to sort first, so a bead nobody bothers
	// to reclassify costs the least by default.
	tierField := huh.NewSelect[string]().
		Title("Tier").
		Options(m.newBeadFormTierOptions()...).
		Value(&tier)
	parentField := huh.NewSelect[string]().
		Title("Parent (epic)").
		Options(m.newBeadFormParentOptions()...).
		Value(&parent)
	descField := huh.NewText().
		Title(fieldLabelDescription).
		CharLimit(2000).
		Lines(4).
		Value(&desc)
	acceptField := huh.NewText().
		Title("Acceptance criteria").
		CharLimit(1000).
		Lines(2).
		Value(&accept)
	form := newOverlayForm(huh.NewGroup(titleField, typeField, tierField, parentField, descField, acceptField))
	m.beadForm = initHuhForm(form)
	m.formTitleResult = &title
	m.formTypeResult = &typ
	m.formTierResult = &tier
	m.formParentResult = &parent
	m.formDescResult = &desc
	m.formAcceptResult = &accept
	m.screen = screenForm
	return m, nil
}

// handleEditBeadKey edits the selected bead's title/description — the fix
// for a typo made at 'n' (new bead), which otherwise has no in-app
// correction path at all (see Deps.EditBead's doc comment). Pre-filled from
// the bead already on screen, same pattern as 'n's prefill, not a fresh
// fetch. Not 'i': that key was deliberately freed (see
// TestLiveAgentAttachIsNoOp) and TUI.md still reserves it for a future
// tmux-window jump. 'e' displaced the old expand/collapse-comments binding,
// moved to 'u'.
func (m Model) handleEditBeadKey() (tea.Model, tea.Cmd) {
	bead, ok := m.listSelection()
	if !ok {
		return m, nil
	}
	m.detail = bead
	m.formReturnScreen = screenDashboard
	m.formKind = formKindEditBead

	title, desc := bead.Title, bead.Description
	titleField := huh.NewInput().
		Title("Title").
		CharLimit(500).
		Value(&title)
	descField := huh.NewText().
		Title(fieldLabelDescription).
		CharLimit(2000).
		Lines(6).
		Value(&desc)
	form := newOverlayForm(huh.NewGroup(titleField, descField))
	m.beadForm = initHuhForm(form)
	m.formTitleResult = &title
	m.formDescResult = &desc
	m.screen = screenForm
	return m, nil
}

func (m Model) handleCommentKey() (tea.Model, tea.Cmd) {
	bead, ok := m.listSelection()
	if !ok {
		return m, nil
	}
	m.detail = bead
	return m.openCommentFormFor()
}

func (m Model) handleStatusKey() (tea.Model, tea.Cmd) {
	bead, ok := m.listSelection()
	if !ok {
		return m, nil
	}
	m.detail = bead
	from := m.detail.DomainState()
	targets := domain.NewBeadStateMachine().AllowedTargets(from)
	if len(targets) == 0 {
		return m, m.notify(fmt.Sprintf("no status transitions from %s", from))
	}
	m.statusTargets = targets
	m.statusCursor = 0
	var result domain.BeadState
	selectField := huh.NewSelect[domain.BeadState]().
		Title("Change status").
		Options(huh.NewOptions(targets...)...).
		Value(&result)
	form := newOverlayForm(huh.NewGroup(selectField))
	m.statusForm = initHuhForm(form)
	m.statusResult = &result
	return m, nil
}

// handleStopKey stops the agent (if running) and queues the bead for a
// human — one gesture, the same regardless of which detail tab is focused.
// Only valid from a state that can actually reach human_queue (working,
// validating, retry); anything else gets a notice explaining why rather
// than silently doing nothing.
func (m Model) handleStopKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	bead, ok := m.listSelection()
	if !ok {
		return m, nil
	}
	m.detail = bead
	from := bead.DomainState()
	if !domain.NewBeadStateMachine().CanTransition(from, domain.BeadStateHumanQueue) {
		return m, m.notify(fmt.Sprintf("%s can't be sent to Needs You from %s", bead.BRN, from))
	}
	m.confirming = "stop"
	return m.updateConfirm(msg)
}

func (m Model) handleRunKey() (tea.Model, tea.Cmd) {
	bead, ok := m.listSelection()
	if !ok {
		return m, m.notify("no bead selected — move to a row first")
	}
	// An unassigned bead can't run — open the model picker instead and
	// chain the run behind the assign, so 'r' always ends in a run. The
	// notice says why the run didn't start: the picker alone doesn't.
	if bead.Assignee == "" {
		m.runAfterAssign = string(bead.BRN)
		m, cmd := m.openModelPicker(string(bead.BRN))
		return m, tea.Batch(m.notify(string(bead.BRN)+" has no assignee — pick a model and the run starts"), cmd)
	}
	// A live session can't be spawned twice — re-show it and say so.
	if t := m.sessionsFor(kindAgent)[string(bead.BRN)]; t != nil && !t.done {
		return m, tea.Batch(m.notify("agent for "+string(bead.BRN)+" is already running"), m.embeddedSpawnCmd(string(bead.BRN), kindAgent))
	}
	// The agent runs inside the right pane's Agent tab: an embedded
	// terminal on a pty, rendered by the vt emulator.
	return m, m.embeddedSpawnCmd(string(bead.BRN), kindAgent)
}

func (m Model) handleAssignKey() (tea.Model, tea.Cmd) {
	bead, ok := m.listSelection()
	if !ok {
		return m, nil
	}
	return m.openModelPicker(string(bead.BRN))
}

// handleCopyKey: on the Terminal/Diff tabs the thing worth copying is
// what's actually on screen — the agent's output or the diff — since
// BARON's own mouse-motion capture (needed for wheel-scroll inside those
// panes) blocks the terminal's native click-drag selection. Everywhere else
// 'y' copies the bead's BRN, the identifier everything else (git branch,
// tmux window, `bd show`) is keyed on. Either way it's OSC 52, which
// reaches the real system clipboard even over SSH/tmux.
func (m Model) handleCopyKey() (tea.Model, tea.Cmd) {
	if m.detailTab == 1 || m.detailTab == 2 {
		if text, label, ok := m.paneCopyText(m.currentKind()); ok {
			return m, tea.Batch(tea.SetClipboard(text), m.notify("copied "+label+" to clipboard"))
		}
		return m, m.notify("nothing to copy yet")
	}
	bead, ok := m.listSelection()
	if !ok {
		return m, nil
	}
	brn := string(bead.BRN)
	return m, tea.Batch(tea.SetClipboard(brn), m.notify("copied "+brn+" to clipboard"))
}

// handleMergeKey merges the selected bead's branch immediately, no
// confirmation screen: the branch is already visible inline in the
// Overview tab (see mergeTargetForBead), deps.Merge re-runs its own
// authoritative preflight/config checks before touching anything
// (internal/cli/merge.go's runMerge), and a failure surfaces as a toast
// exactly like any other one-key dashboard action (see commandRanMsg) — so
// there is nothing left for a separate review screen to gather that isn't
// already visible or re-validated by the merge itself.
func (m Model) handleMergeKey() (tea.Model, tea.Cmd) {
	bead, ok := m.listSelection()
	if !ok {
		return m, nil
	}
	if bead.Status != store.BeadStatusMergable {
		// There is no manual "ready" command — a branch becomes ready
		// to merge automatically once a run's gate passes
		// (internal/cli/run_checks.go's readyForMerge).
		return m, m.notify("no branch ready to merge yet — that happens automatically once the gate passes (r to run)")
	}
	brn := string(bead.BRN)
	return m, runTypedCommand(func() (string, error) { return m.deps.Merge(brn) })
}

// handleFocusKey toggles keyboard focus: while focused, keystrokes forward
// to the session's pty; shift+esc returns focus to the TUI (a bare esc goes
// through like any other key — see updateKey's termFocus branch — since
// esc-esc is opencode's own interrupt gesture). For the Agent tab, no
// session means nothing to focus — 'r' is the deliberate separate action
// that starts one. The Diff tab has no separate start gesture (tabCmd
// already auto-starts it on tab entry), but that auto-start can fail (hunk
// not installed, a transient spawn error) and land the pane on its "no
// diff view yet — press t to open it" empty state (viewSessionPane). This
// case has to make that instruction true rather than re-notifying
// "starting" forever with nothing actually retrying: with no live Diff
// session, 't' retries the spawn.
func (m Model) handleFocusKey() (tea.Model, tea.Cmd) {
	kind := m.currentKind()
	t := m.sessionFor(kind)
	if t == nil || t.done {
		if kind == kindDiff {
			return m, tea.Batch(m.embeddedSpawnCmd(m.liveBRN, kindDiff), m.notify("diff view starting for "+m.liveBRN+"…"))
		}
		return m, m.notify("no running agent for this bead — press r to start it")
	}
	t.focus = !t.focus
	m.termFocus = t.focus
	m.resizeSessions()
	return m, m.notify(focusNoticeText(m.termFocus))
}

// handleZoomKey zooms the embedded terminal fullscreen (hides the
// header/footer), or restores the split. Zooming also focuses the terminal
// — as if 't' had been pressed — since a zoomed frame with no visible way
// to type into it is dead: the only way back out either way is shift+esc
// (see updateKey's termFocus branch), which clears both.
func (m Model) handleZoomKey() (tea.Model, tea.Cmd) {
	kind := m.currentKind()
	t := m.sessionFor(kind)
	if t == nil || t.done {
		if kind == kindDiff {
			return m, m.notify("no running diff view for this bead — press t to start it")
		}
		return m, m.notify("no running agent for this bead — press r to start it")
	}
	t.zoom = !t.zoom
	m.termZoom = t.zoom
	if m.termZoom {
		t.focus = true
		m.termFocus = true
		m.resizeSessions()
		return m, m.notify(focusNoticeText(true))
	}
	m.resizeSessions()
	return m, nil
}

// selectListRow loads the row under the cursor into the right pane and keeps
// the cursor visible.
func (m *Model) selectListRow() {
	rows := m.currentRows()
	if len(rows) == 0 {
		m.clearEmptySelection()
		return
	}
	if m.listCursor < 0 {
		m.listCursor = 0
	} else if m.listCursor >= len(rows) {
		m.listCursor = len(rows) - 1
	}
	newBRN := string(rows[m.listCursor].bead.BRN)
	// A Diff session has no standing value once you've moved on — it's
	// a passive viewer that auto-starts just from landing on the tab
	// (see tabCmd), not deliberate background work the way a running
	// agent is, so leaving it alive per bead-ever-glanced-at only
	// accumulates dead weight the render tick keeps re-scanning every
	// frame (see spawnDiffTerminal's doc comment for what that cost
	// live: 230+ tmux sessions, 20+ live hunk processes, a pegged
	// CPU). Kill it the moment selection actually moves to a
	// different bead — re-selecting this one later just respawns it.
	if newBRN != m.liveBRN {
		if t := m.diffSessions[m.liveBRN]; t != nil {
			t.kill()
		}
	}
	m.detail = rows[m.listCursor].bead
	m.detailVP.SetYOffset(0)
	// The Agent tab's persisted-summary fallback (view.go) reads
	// liveBRN, not detail.BRN directly — without this, switching
	// selection left it pointed at whichever bead was last run,
	// showing that stale bead's summary instead of the newly
	// selected one's.
	m.liveBRN = newBRN
	vis := m.listPaneRows()
	m.listScroll = min(max(0, m.listCursor-vis+1), max(0, len(rows)-vis))
}

func (m *Model) clearEmptySelection() {
	if m.liveBRN != "" {
		if t := m.diffSessions[m.liveBRN]; t != nil {
			t.kill()
		}
	}
	if t := m.sessions[m.liveBRN]; t != nil && !t.done && m.detailTab == tabIndexFor(kindAgent) {
		if b, ok := m.findBeadByBRN(domain.BRN(m.liveBRN)); ok {
			m.detail = b
		}
	} else if !m.spawning[spawnKey(m.liveBRN, kindAgent)] {
		m.detail, m.liveBRN = store.Bead{}, ""
	}
	m.listCursor, m.listScroll = 0, 0
}

// boardDetailVisible reports whether a bead's detail (comments, audit,
// header stats) is actually on screen right now and worth fetching —
// Board Mode itself, or Prompt Mode's Beads-tab detail pane (v7,
// docs/PRD/crew-mode.md §2.8), which reuses the exact same m.detail/
// m.comments fields (see viewPromptBeadDetail/promptBeadDetailCmd).
func (m Model) boardDetailVisible() bool {
	// Prompt Mode's thread pane always shows m.detail's content (title +
	// comments), regardless of which of its three panes currently has
	// keyboard focus — unlike the old design, there's no beads-focus
	// toggle to gate this on anymore.
	return m.screen == screenDashboard || m.screen == screenCrew
}

// afterSelect reloads the newly selected bead's comments + audit trail (so
// the right pane never shows a stale list) and keeps the embedded terminal's
// frame tick running when a session is live.
func (m *Model) afterSelect() tea.Cmd {
	var cmds []tea.Cmd
	if m.boardDetailVisible() && m.detail.BRN != "" {
		// Clear the previous bead's comments/audit immediately rather than
		// leaving them on screen until the new fetch lands — see
		// commentsLoading's doc comment.
		m.comments = nil
		m.auditEvents = nil
		m.commentsLoading = true
		cmds = append(
			cmds,
			loadComments(m.ctx, m.deps, string(m.detail.BRN)),
			loadAuditEvents(m.deps, string(m.detail.BRN)),
			loadHeaderStats(m.deps, string(m.detail.BRN)),
		)
	}
	// Moving to another bead kills the previous bead's Diff session
	// (selectListRow), so if the Diff tab is the one on screen it needs a
	// session for the bead now selected — otherwise the tab sits empty until
	// the user switches away and back. hunk runs for exactly the bead whose
	// diff is being looked at, and no others.
	if m.detailTab == 2 && m.detail.BRN != "" {
		cmds = append(cmds, m.embeddedSpawnCmd(string(m.detail.BRN), kindDiff))
	}
	if cmd := m.termTick(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// tabCmd refreshes comments when the right pane is on the Overview tab (its
// comment list) and keeps the embedded terminal's frame tick running.
func (m *Model) tabCmd() tea.Cmd {
	m.syncTermFocus()
	var cmds []tea.Cmd
	if m.detailTab == 0 && m.detail.BRN != "" {
		cmds = append(cmds, loadComments(m.ctx, m.deps, string(m.detail.BRN)))
	}
	if m.detailTab == 2 && m.detail.BRN != "" {
		// Unlike the Agent tab (needs an assigned agent, only starts on an
		// explicit 'r'), the Diff tab has nothing to wait on — opening it
		// is the whole ask, so it starts hunk itself instead of making the
		// user press 't' first just to get something on screen.
		// embeddedSpawnCmd already no-ops the actual spawn when a session
		// is already running for this bead, so landing here repeatedly
		// (switching tabs back and forth) is safe.
		cmds = append(cmds, m.embeddedSpawnCmd(string(m.detail.BRN), kindDiff))
	}
	if cmd := m.termTick(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// syncTermFocus refreshes termFocus/termZoom to the newly active detail
// tab's own session state, and releases focus/zoom on every OTHER kind's
// session for this bead. Without this, switching tabs by click or tab/
// shift+tab (instead of shift+esc) while a session is focused would leave
// keystrokes silently routed to a session that's no longer even the one on
// screen — termFocus is a single shared flag, but two different kinds of
// session can now be live for the same bead at once.
func (m *Model) syncTermFocus() {
	active := m.currentKind()
	for _, kind := range []termKind{kindAgent, kindDiff} {
		if kind == active {
			continue
		}
		if t := m.sessionsFor(kind)[string(m.detail.BRN)]; t != nil {
			t.focus, t.zoom = false, false
		}
	}
	if t := m.sessionFor(active); t != nil {
		m.termFocus, m.termZoom = t.focus, t.zoom
	} else {
		m.termFocus, m.termZoom = false, false
	}
}

// openModelPicker opens the assign-model overlay for brn. The model list
// comes from Deps.Models, fetched in the background (modelsLoadedMsg) so
// opening stays instant: Deps.Models can spawn provider subprocesses
// (opencode models) and must never block the update loop. While the fetch
// runs the picker renders a loading state; on arrival the list is
// ranked by the usage cache (recent + popular first) and the huh form is created.
