package tui

import (
	"fmt"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

func (m Model) togglePromptMode() (tea.Model, tea.Cmd) {
	if m.screen == screenCrew {
		m.screen = m.prevScreen
		return m, nil
	}
	m.prevScreen = m.screen
	m.screen = screenCrew
	m.promptFocus = promptPaneRightTop
	m.promptRightTab = promptTabBeads
	m.promptPersonaCursor = 0
	m.promptPersonaScroll = 0
	m.promptPersonaOpenID = ""
	m.promptBeadTab = m.listTab
	m.promptBeadSubTab = m.listSubTab
	m.promptBeadCursor = 0
	m.promptBeadScroll = 0
	m.promptAgentTab = 0

	var cmds []tea.Cmd
	if m.deps.Personas == nil {
		m.personas = nil
	} else if personas, err := m.deps.Personas(); err != nil {
		cmds = append(cmds, m.notifyErr(fmt.Errorf("personas: %w", err)))
	} else {
		sort.Slice(personas, func(i, j int) bool { return personas[i].ID < personas[j].ID })
		m.personas = personas
		if m.deps.PersonaStatuses != nil {
			cmds = append(cmds, personaStatusesCmd(m.deps, personaIDs(personas)))
		}
	}
	// Left-pane sessions spawn lazily (first visit to that agent's tab, see
	// updatePromptLeftKey) — only the tab list itself is fetched eagerly.
	cmds = append(cmds, promptAgentIDsCmd(m.deps))
	// Beads is the default tab (entry order: Beads, then Personas) — load
	// its first row's detail immediately, or the right-bottom pane shows
	// whatever m.detail/m.comments Board Mode last left behind until the
	// cursor actually moves.
	if cmd := m.promptBeadDetailCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// personaIDs extracts every persona's ID, in order — Deps.PersonaStatuses'
// input shape (see togglePromptMode's initial fetch and the fire/dispatch
// refresh below). personaStatusesMsg replaces m.personaStatuses wholesale
// (it has no way to know which ids a partial fetch even covered), so every
// caller must pass the FULL roster, not just the one persona that just
// fired — a single-id fetch would silently erase every other persona's
// last-known running state.
func personaIDs(personas []persona.Persona) []string {
	ids := make([]string, len(personas))
	for i, p := range personas {
		ids[i] = p.ID
	}
	return ids
}

// updatePromptKey drives Prompt Mode (screenCrew) — v7's 3-pane redesign
// (docs/PRD/crew-mode.md): left is a live agent-CLI chat, right-top is a
// list (Personas or Beads, m.promptRightTab), right-bottom is that list
// selection's detail. 'tab' cycles pane focus (left -> right-top ->
// right-bottom -> left); shift+left/right switches promptRightTab and
// shift+up/down moves the right-top list cursor, both regardless of which
// pane has focus (mirrors Board Mode's own shift+arrow pairing — plain
// arrow acts on the "current" pane, shift+arrow always reaches the other
// one). The per-tab verbs below (space/e/n/ctrl+d/r/o for Personas;
// n/e/c/o for Beads) also act regardless of focus, for the same reason v6
// let persona-level verbs act regardless of roster/worklist/thread focus:
// "which item is selected" and "which pane has keyboard focus" are
// independent questions.
func (m Model) updatePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		return m.cyclePromptFocusKey()
	case "shift+left", "shift+right":
		return m.switchPromptRightTabKey()
	case "shift+up":
		return m.movePromptRightTopCursor(-1)
	case keyShiftDown:
		return m.movePromptRightTopCursor(1)
	}
	if m.promptRightTab == promptTabPersonas {
		next, cmd, handled := m.updatePromptPersonasTabKey(msg)
		m = asModel(next)
		if handled {
			return m, cmd
		}
	} else {
		next, cmd, handled := m.updatePromptBeadsTabKey(msg)
		m = asModel(next)
		if handled {
			return m, cmd
		}
	}
	switch m.promptFocus {
	case promptPaneLeft:
		return m.updatePromptLeftKey(msg)
	case promptPaneRightBottom:
		return m.updatePromptRightBottomKey(msg)
	default:
		return m.updatePromptRightTopKey(msg)
	}
}

// cyclePromptFocusKey advances promptFocus one step (left -> right-top ->
// right-bottom -> left), the "tab" key's action in updatePromptKey.
func (m Model) cyclePromptFocusKey() (tea.Model, tea.Cmd) {
	switch m.promptFocus {
	case promptPaneLeft:
		m.promptFocus = promptPaneRightTop
	case promptPaneRightTop:
		m.promptFocus = promptPaneRightBottom
	default:
		m.promptFocus = promptPaneLeft
	}
	return m, nil
}

// switchPromptRightTabKey toggles promptRightTab between Personas and Beads
// (shift+left/shift+right in updatePromptKey), priming the newly-active
// tab's data the same way it would load on first view.
func (m Model) switchPromptRightTabKey() (tea.Model, tea.Cmd) {
	if m.promptRightTab == promptTabPersonas {
		m.promptRightTab = promptTabBeads
		return m, m.promptBeadDetailCmd()
	}
	m.promptRightTab = promptTabPersonas
	p, ok := m.selectedPersona()
	if !ok {
		return m, nil
	}
	m.promptPersonaDetailScroll = 0
	var cmds []tea.Cmd
	if _, cached := m.promptPersonaOutput[p.ID]; !cached && m.deps.PersonaOutput != nil {
		cmds = append(cmds, personaOutputCmd(m.deps, p.ID))
	}
	if _, cached := m.promptPersonaActivity[p.ID]; !cached && m.deps.PersonaActivity != nil {
		cmds = append(cmds, personaActivityCmd(m.deps, p.ID))
	}
	return m, tea.Batch(cmds...)
}

// fireSelectedPersonaKey is the Personas tab's 'r' key: opens the selected
// persona's accordion, moves the cursor onto its own row, and fires every
// refresh Deps offers (FireNow, plus re-fetching activity/output/statuses)
// so the roster reflects the run immediately instead of waiting for the
// next natural poll.
func (m Model) fireSelectedPersonaKey() (tea.Model, tea.Cmd) {
	p, ok := m.selectedPersona()
	if !ok {
		return m, nil
	}
	m.promptPersonaOpenID = p.ID
	m.promptPersonaDetailScroll = 0
	for i, row := range m.promptPersonaFlatRows() {
		if row.personaIdx >= 0 && row.personaIdx < len(m.personas) && m.personas[row.personaIdx].ID == p.ID && row.eventIdx == -1 {
			m.promptPersonaCursor = i
			break
		}
	}
	var cmds []tea.Cmd
	if m.deps.FireNow != nil {
		cmds = append(cmds, fireNowCmd(m.deps, p.ID))
	}
	if m.deps.PersonaActivity != nil {
		cmds = append(cmds, personaActivityCmd(m.deps, p.ID))
	}
	if m.deps.PersonaOutput != nil {
		cmds = append(cmds, personaOutputCmd(m.deps, p.ID))
	}
	if m.deps.PersonaStatuses != nil {
		cmds = append(cmds, personaStatusesCmd(m.deps, personaIDs(m.personas)))
	}
	return m, tea.Batch(cmds...)
}

// updatePromptPersonasTabKey handles the Personas tab's own "regardless of
// pane focus" verbs (space/e/n/ctrl+d/r/o — see updatePromptKey's own doc
// comment). handled reports whether msg matched one of them, in which case
// the caller returns immediately; the returned Model always carries
// whatever state this method mutated either way (the delete-confirm-cancel
// below fires even when nothing else matches).
func (m Model) updatePromptPersonasTabKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	// Any key other than ctrl+d cancels a pending delete confirmation,
	// same reasoning as updateSplitKey's own bead-delete gesture.
	if msg.String() != "ctrl+d" && m.deleteConfirmPersonaID != "" {
		m.deleteConfirmPersonaID = ""
		m.statusMsg = ""
	}
	switch msg.String() {
	case " ", "space":
		next, cmd := m.toggleSelectedPersonaEnabled()
		return next, cmd, true
	case "e":
		p, ok := m.selectedPersona()
		if !ok {
			return m, nil, true
		}
		return m.openPersonaEditForm(p), nil, true
	case "n":
		return m.openPersonaNewForm(), nil, true
	case "ctrl+d":
		next, cmd := m.confirmDeleteSelectedPersona()
		return next, cmd, true
	case "r":
		next, cmd := m.fireSelectedPersonaKey()
		return next, cmd, true
	case "o":
		// Hand the row's bead off to Board Mode — only meaningful on an
		// accordion event row (eventIdx >= 0); a plain persona row has
		// nothing to open, same as pressing 'o' with the Beads tab's
		// own list empty.
		row, ok := m.selectedPersonaRow()
		if !ok || row.eventIdx < 0 {
			return m, nil, true
		}
		events := m.promptPersonaActivity[m.personas[row.personaIdx].ID]
		if row.eventIdx >= len(events) || events[row.eventIdx].Target == "" {
			return m, nil, true
		}
		m.screen = screenDashboard
		m.pendingFocusBRN = events[row.eventIdx].Target
		return m, loadBeads(m.ctx, m.deps), true
	}
	return m, nil, false
}

// updatePromptBeadsTabKey handles the Beads tab's own "regardless of pane
// focus" verbs. handled reports whether msg matched one of them; "0"/"a"
// deliberately mutates state and returns handled=false, since (unlike every
// other case here) it falls through to updatePromptKey's own final
// promptFocus-based dispatch rather than returning immediately — same as
// it always has.
func (m Model) updatePromptBeadsTabKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "1", "2", "3", "4":
		tab := int(msg.String()[0] - '1')
		m.promptBeadTab = tab
		m.promptBeadSubTab = -1
		m.promptBeadCursor = 0
		m.promptBeadScroll = 0
		return m, m.promptBeadDetailCmd(), true
	case "0", "a":
		m.promptBeadTab = -1
		m.promptBeadSubTab = -1
		m.promptBeadCursor = 0
		m.promptBeadScroll = 0
		return m, nil, false
	case "[":
		if m.promptBeadTab <= 0 {
			m.promptBeadTab = len(boardColumns) - 1
		} else {
			m.promptBeadTab--
		}
		m.promptBeadSubTab = -1
		m.promptBeadCursor = 0
		m.promptBeadScroll = 0
		return m, m.promptBeadDetailCmd(), true
	case "]":
		if m.promptBeadTab >= len(boardColumns)-1 {
			m.promptBeadTab = 0
		} else {
			m.promptBeadTab++
		}
		m.promptBeadSubTab = -1
		m.promptBeadCursor = 0
		m.promptBeadScroll = 0
		return m, m.promptBeadDetailCmd(), true
	case "n":
		return m.openPromptNewBeadForm(), nil, true
	case "e":
		return m.openPromptEditBeadForm(), nil, true
	case "c":
		bead, ok := m.promptSelectedBead()
		if !ok {
			return m, nil, true
		}
		m.detail = bead
		next, cmd := m.openCommentFormFor()
		return next, cmd, true
	case "o":
		if m.detail.BRN == "" {
			return m, nil, true
		}
		m.screen = screenDashboard
		m.pendingFocusBRN = string(m.detail.BRN)
		return m, loadBeads(m.ctx, m.deps), true
	case "u":
		// detailContent(w) (reused verbatim for this pane, see
		// viewPromptBeadDetail) renders "press u to expand/collapse"
		// once a bead has more than 3 comments — Board Mode's own 'u'
		// (updateSplitKey) toggles the same m.commentsExpanded field,
		// but that handler only ever fires on screenDashboard, so
		// Prompt Mode needs its own case reaching the same field or
		// the hint it renders would be a dead end here.
		m.commentsExpanded = !m.commentsExpanded
		return m, nil, true
	}
	return m, nil, false
}

// togglePersonaAccordion drives 'l' on a Personas-tab row: at most one
// persona's work-outputs accordion is open at a time — opening a second
// closes the first, and pressing 'l' again on the open one closes it.
// Fetches Deps.PersonaActivity lazily, only the first time a given persona
// is opened (promptPersonaActivity caches by id after that). Closing always
// lands the cursor back on id's own persona row: a persona's row sits at
// the same flat index as its plain position in m.personas regardless of
// whether its OWN accordion is open (only events AFTER it shift with it —
// see promptPersonaFlatRows), and since at most one accordion is ever
// open, no other persona's rows can be shifting id's row around either.
func (m Model) togglePersonaAccordion(id string) (tea.Model, tea.Cmd) {
	if m.promptPersonaOpenID == id {
		m.promptPersonaOpenID = ""
		for i, p := range m.personas {
			if p.ID == id {
				m.promptPersonaCursor = i
				break
			}
		}
		m.promptPersonaDetailScroll = 0
		return m, nil
	}
	m.promptPersonaOpenID = id
	m.promptPersonaDetailScroll = 0
	for i, row := range m.promptPersonaFlatRows() {
		if row.personaIdx >= 0 && row.personaIdx < len(m.personas) && m.personas[row.personaIdx].ID == id && row.eventIdx == -1 {
			m.promptPersonaCursor = i
			break
		}
	}
	var cmds []tea.Cmd
	if _, cached := m.promptPersonaActivity[id]; !cached && m.deps.PersonaActivity != nil {
		cmds = append(cmds, personaActivityCmd(m.deps, id))
	}
	if _, cached := m.promptPersonaOutput[id]; !cached && m.deps.PersonaOutput != nil {
		cmds = append(cmds, personaOutputCmd(m.deps, id))
	}
	return m, tea.Batch(cmds...)
}

// movePromptRightTopCursor moves whichever right-top list is on the active
// tab, regardless of pane focus (shift+up/down) — clamped to bounds, a
// no-op on an empty list. Moving the Beads-tab cursor also reloads the
// right-bottom detail pane (m.detail/m.comments), same as Board Mode's own
// list-cursor movement does via afterSelect. The Personas-tab cursor walks
// promptPersonaFlatRows — persona rows AND, under the open one, its own
// work-output rows — so browsing reaches individual outputs too, not just
// personas (see selectedPersona/selectedPersonaRow).
func (m Model) movePromptRightTopCursor(delta int) (tea.Model, tea.Cmd) {
	if m.promptRightTab == promptTabPersonas {
		n := len(m.promptPersonaFlatRows())
		if n == 0 {
			return m, nil
		}
		m.promptPersonaCursor = clampInt(m.promptPersonaCursor+delta, n-1)
		m.clampPromptPersonaScroll()
		m.promptPersonaDetailScroll = 0
		if p, ok := m.selectedPersona(); ok {
			var cmds []tea.Cmd
			if _, cached := m.promptPersonaOutput[p.ID]; !cached && m.deps.PersonaOutput != nil {
				cmds = append(cmds, personaOutputCmd(m.deps, p.ID))
			}
			if _, cached := m.promptPersonaActivity[p.ID]; !cached && m.deps.PersonaActivity != nil {
				cmds = append(cmds, personaActivityCmd(m.deps, p.ID))
			}
			return m, tea.Batch(cmds...)
		}
		return m, nil
	}
	rows := m.promptBeadRows()
	if len(rows) == 0 {
		return m, nil
	}
	oldCursor := m.promptBeadCursor
	m.promptBeadCursor = clampInt(m.promptBeadCursor+delta, len(rows)-1)
	m.clampPromptBeadScroll()
	if m.promptBeadCursor == oldCursor {
		return m, nil
	}
	return m, m.promptBeadDetailCmd()
}

// updatePromptRightTopKey drives j/k when the right-top list itself has
// keyboard focus — identical movement to movePromptRightTopCursor
// (shift+up/down's always-available alias), just the plain-key path. 'l'
// (Personas tab only) toggles the selected row's accordion here rather
// than in updatePromptKey's "act regardless of focus" verb switch,
// specifically BECAUSE it needs to be focus-gated: unlike space/e/n/r,
// 'l' is ambiguous with the left pane's own agent-tab-switch key of the
// same letter, and it must lose that ambiguity by only ever firing when
// the right-top list genuinely has focus — otherwise a real bug (found by
// hand: pressing 'l' with the left pane focused silently did nothing,
// since the earlier "regardless of focus" switch was catching it as a
// no-op before updatePromptLeftKey ever got a turn to treat it as a tab
// switch).
func (m Model) updatePromptRightTopKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		return m.movePromptRightTopCursor(1)
	case "k", "up":
		return m.movePromptRightTopCursor(-1)
	case "h", "ctrl+h", "left":
		if m.promptRightTab == promptTabBeads {
			m.promptSubTabPrev()
			m.promptBeadCursor = 0
			m.promptBeadScroll = 0
			return m, m.promptBeadDetailCmd()
		}
	case "l", "ctrl+l", "right":
		if m.promptRightTab == promptTabBeads {
			m.promptSubTabNext()
			m.promptBeadCursor = 0
			m.promptBeadScroll = 0
			return m, m.promptBeadDetailCmd()
		}
		if m.promptRightTab == promptTabPersonas {
			p, ok := m.selectedPersona()
			if !ok {
				return m, nil
			}
			return m.togglePersonaAccordion(p.ID)
		}
	case "enter":
		if m.promptRightTab == promptTabPersonas {
			p, ok := m.selectedPersona()
			if !ok {
				return m, nil
			}
			return m.togglePersonaAccordion(p.ID)
		}
	}
	return m, nil
}

// updatePromptRightBottomKey scrolls the right-bottom detail pane. Only the
// Beads tab has scrollable content (detailContent's Overview rendering,
// via m.detailVP — the SAME viewport Board Mode's own Overview/Audit tabs
// use, safe to share since the two screens are never visible together, see
// boardDetailVisible); the Personas tab's detail is short enough to need
// no scrolling.
func (m Model) updatePromptRightBottomKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.promptRightTab == promptTabPersonas {
		switch msg.String() {
		case "j", "down":
			m.promptPersonaDetailScroll++
		case "k", "up":
			m.promptPersonaDetailScroll = max(0, m.promptPersonaDetailScroll-1)
		case "pgdown":
			m.promptPersonaDetailScroll += 10
		case "pgup":
			m.promptPersonaDetailScroll = max(0, m.promptPersonaDetailScroll-10)
		}
		return m, nil
	}
	m.syncPromptBeadVP()
	switch msg.String() {
	case "j", "down":
		m.detailVP.ScrollDown(1)
	case "k", "up":
		m.detailVP.ScrollUp(1)
	case "pgdown":
		m.detailVP.ScrollDown(m.detailVP.Height())
	case "pgup":
		m.detailVP.ScrollUp(m.detailVP.Height())
	}
	return m, nil
}

// updatePromptLeftKey drives the left pane when it has keyboard focus: h/l
// switches which agent-CLI tab is active; 't'/enter toggles keyboard focus
// into that tab's live session (mirrors Board Mode's own 't' — see
// focusPromptAgent, terminal.go), lazily spawning it on first visit.
func (m Model) updatePromptLeftKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "h", "ctrl+h", "left":
		return m.movePromptAgent(-1)
	case "l", "ctrl+l", "right":
		return m.movePromptAgent(1)
	case "t", "enter":
		m.promptTermScrollOffset = 0
		return m.focusPromptAgent()
	case "up", "k":
		if m.promptTermFocus {
			return m, nil
		}
		id := m.activePromptAgentID()
		t := m.promptSessions[id]
		if t == nil {
			return m, nil
		}
		bodyRows := max(1, m.splitBoxInnerRows()-3)
		hi := historyMaxOffset(len(t.history), bodyRows)
		if t.history == nil {
			m.promptTermScrollOffset += 3
		} else {
			m.promptTermScrollOffset = clampInt(m.promptTermScrollOffset+3, hi)
		}
		if m.promptTermScrollOffset > 0 && m.deps.AgentHost != nil && t.needsHistory() {
			t.capturing = true
			return m, paneHistoryCmd(m.ctx, m.deps.AgentHost, t.brn, t.kind, t.tmuxWindow)
		}
		return m, nil
	case "down", "j":
		if m.promptTermFocus {
			return m, nil
		}
		id := m.activePromptAgentID()
		t := m.promptSessions[id]
		if t == nil {
			return m, nil
		}
		m.promptTermScrollOffset = clampInt(m.promptTermScrollOffset-3, 0)
		return m, nil
	}
	return m, nil
}

func (m Model) movePromptAgent(delta int) (tea.Model, tea.Cmd) {
	if n := len(m.promptAgentIDs); n > 0 {
		m.promptAgentTab = (m.promptAgentTab + n + delta) % n
		m.promptTermScrollOffset = 0
	}
	return m, nil
}

// clampPromptPersonaScroll keeps promptPersonaCursor visible within the
// Personas-tab list's scrollable window — m.promptRightTopListRows(), the
// SAME row budget viewPromptPersonaList renders against, so this and the
// render never disagree about how many rows are actually visible.
func (m *Model) clampPromptPersonaScroll() {
	visible := max(1, m.promptRightTopListRows())
	if m.promptPersonaCursor < m.promptPersonaScroll {
		m.promptPersonaScroll = m.promptPersonaCursor
	}
	if m.promptPersonaCursor >= m.promptPersonaScroll+visible {
		m.promptPersonaScroll = m.promptPersonaCursor - visible + 1
	}
}

// clampPromptBeadScroll keeps promptBeadCursor visible within the
// Beads-tab list's scrollable window — same row budget clampPromptPersonaScroll
// uses, for the same reason.
func (m *Model) clampPromptBeadScroll() {
	visible := max(1, m.promptRightTopListRows())
	if m.promptBeadCursor < m.promptBeadScroll {
		m.promptBeadScroll = m.promptBeadCursor
	}
	if m.promptBeadCursor >= m.promptBeadScroll+visible {
		m.promptBeadScroll = m.promptBeadCursor - visible + 1
	}
}

// selectedPersona returns the persona promptPersonaCursor currently points
// at, whether the cursor sits on that persona's own row or on one of its
// open accordion's work-output rows (selectedPersonaRow resolves which) —
// verbs like space/e/r/l apply to "the persona this row belongs to"
// regardless of which of its own rows is under the cursor.
func (m Model) selectedPersona() (persona.Persona, bool) {
	row, ok := m.selectedPersonaRow()
	if !ok {
		return persona.Persona{}, false
	}
	return m.personas[row.personaIdx], true
}

// selectedPersonaRow returns the flattened row (see promptPersonaFlatRows)
// promptPersonaCursor currently points at — a persona row (eventIdx == -1)
// or one of its own accordion's work-output rows (eventIdx >= 0). A "loading…"
// / "no work outputs yet" placeholder is rendered right under an open
// persona's row (viewPromptPersonaList) but is never itself a flat row —
// nothing to select, so it never owns a cursor position or an eventIdx of
// its own. 'o' uses eventIdx >= 0 to know whether there's an actual bead
// reference to hand off.
func (m Model) selectedPersonaRow() (promptPersonaRow, bool) {
	rows := m.promptPersonaFlatRows()
	if m.promptPersonaCursor < 0 || m.promptPersonaCursor >= len(rows) {
		return promptPersonaRow{}, false
	}
	return rows[m.promptPersonaCursor], true
}

// promptBeadRows is Prompt Mode's Beads-tab list source: the bead set as
// a tree (epic headers, children indented), filtered by promptBeadTab and
// promptBeadSubTab (using bucketRows for exact parity with Board Mode).
func (m Model) promptBeadRows() []splitRow {
	if m.promptBeadTab >= 0 && m.promptBeadTab < len(boardColumns) {
		return m.bucketRows(m.promptBeadTab, m.promptBeadSubTab)
	}
	return m.promptBeadTreeRows()
}

// promptSelectedBead returns the bead promptBeadCursor currently points at
// (an epic header row's bead is just as selectable as a leaf's).
func (m Model) promptSelectedBead() (store.Bead, bool) {
	rows := m.promptBeadRows()
	if m.promptBeadCursor < 0 || m.promptBeadCursor >= len(rows) {
		return store.Bead{}, false
	}
	return rows[m.promptBeadCursor].bead, true
}

// promptBeadDetailCmd loads the Beads-tab cursor's selection into
// m.detail/m.comments/m.auditEvents — Prompt Mode's own version of Board
// Mode's afterSelect, since Prompt Mode's own list has no shared code path
// into afterSelect to inherit that from.
func (m *Model) promptBeadDetailCmd() tea.Cmd {
	bead, ok := m.promptSelectedBead()
	if !ok {
		m.detail = store.Bead{}
		m.comments = nil
		m.auditEvents = nil
		return nil
	}
	if bead.BRN == m.detail.BRN && (len(m.comments) > 0 || !m.commentsLoading) {
		m.detail = bead
		return nil
	}
	m.detail = bead
	m.comments = nil
	m.auditEvents = nil
	m.commentsLoading = true
	return tea.Batch(loadComments(m.ctx, m.deps, string(bead.BRN)), loadAuditEvents(m.deps, string(bead.BRN)))
}

// openCommentFormFor opens formKindComment for m.detail (the caller sets
// it first — Board Mode's own list selection, or Prompt Mode's worklist
// selection) — shared by both screens' 'c' key so there is exactly one
// comment-form construction to keep in sync with submitForm's
// formKindComment case.
func (m Model) openCommentFormFor() (tea.Model, tea.Cmd) {
	if m.detail.BRN == "" {
		return m, nil
	}
	m.formReturnScreen = m.screen
	m.formKind = formKindComment

	var comment string
	// No Placeholder either — same huh first-char quirk as the title
	// field would paint a stray "C" here.
	commentField := huh.NewText().
		CharLimit(2000).
		Lines(4).
		Value(&comment)
	form := newOverlayForm(huh.NewGroup(commentField))
	m.beadForm = initHuhForm(form)
	m.formCommentResult = &comment
	m.screen = screenForm
	return m, nil
}

// toggleSelectedPersonaEnabled flips the selected persona's Enabled flag
// via Deps.UpdatePersona, leaving description/schedule/events untouched —
// the roster's 'space' quick-toggle, for "I want this one off without
// opening the full edit form."
func (m Model) toggleSelectedPersonaEnabled() (tea.Model, tea.Cmd) {
	p, ok := m.selectedPersona()
	if !ok {
		return m, nil
	}
	if m.deps.UpdatePersona == nil {
		return m, nil
	}
	f := p.FormFields()
	f.Enabled = !p.Enabled
	return m, personaUpdateCmd(m.deps, f)
}

// confirmDeleteSelectedPersona is the Personas tab's own ctrl+D double-
// press delete gesture, same shape as updateSplitKey's bead-delete one:
// first press arms deleteConfirmPersonaID and shows a 2s notice, a second
// ctrl+D on the same persona within that window confirms. Deleting a
// repo/embedded built-in with no local file (Deps.DeletePersona errors —
// see DeletePersonaByID's own doc comment) surfaces that error instead of
// silently no-op'ing, so "why didn't ctrl+D do anything" always has an
// answer on screen.
func (m Model) confirmDeleteSelectedPersona() (tea.Model, tea.Cmd) {
	p, ok := m.selectedPersona()
	if !ok || m.deps.DeletePersona == nil {
		return m, nil
	}
	if m.deleteConfirmPersonaID == p.ID && time.Since(m.deleteConfirmAt) < 2*time.Second {
		m.deleteConfirmPersonaID = ""
		m.statusMsg = ""
		return m, personaDeleteCmd(m.deps, p.ID)
	}
	m.deleteConfirmPersonaID = p.ID
	m.deleteConfirmAt = time.Now()
	return m, tea.Batch(
		m.notify(fmt.Sprintf("ctrl+d again within 2s to delete %s", p.Name)),
		tea.Tick(2*time.Second, func(time.Time) tea.Msg { return deleteConfirmExpiredMsg{} }),
	)
}

// personaEventPresetStates is every store.BeadStatus a persona's event
// trigger can react to, in the app's own canonical status order. Every
// seeded/default persona trigger (internal/persona/personas/*.md) uses
// {From: "*", To: X} exclusively — no real usage anywhere reacts to one
// specific prior state — so "fires when a bead reaches X" as a flat,
// searchable multi-select (see personaEventOptions, openPersonaEditForm/
// openPersonaNewForm's eventsField) covers the real shape of this feature.
// A persona's rarer non-wildcard-From rule (only reachable by hand-editing
// its markdown frontmatter outside the TUI) still survives a round trip through this form
// without being silently dropped — see splitPersonaEventRules.
var personaEventPresetStates = []store.BeadStatus{
	store.BeadStatusOpen, store.BeadStatusAssigned, store.BeadStatusWorking,
	store.BeadStatusValidating, store.BeadStatusRetry, store.BeadStatusBlocked,
	store.BeadStatusMergable, store.BeadStatusHumanQueue, store.BeadStatusMerged,
	store.BeadStatusClosed, store.BeadStatusCancelled,
}

// personaEventOptions renders personaEventPresetStates as huh.MultiSelect
// options — label and value are both the plain status string, since that's
// already short and unambiguous (unlike, say, a bead's title).
