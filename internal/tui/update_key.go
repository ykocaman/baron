package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/domain"
)

// keyShiftDown is shift+down's key string — shared by every case matching
// it (updateCommandBar's shift+up/down scroll pair, and the equivalent
// pane-scroll keys in update_prompt.go/update_split.go) so the literal
// exists in exactly one place.
const keyShiftDown = "shift+down"

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	next, cmd, handled := m.dispatchOverlayKey(msg)
	if handled {
		return next, cmd
	}
	m = asModel(next)

	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, m.quitCmd()
	case "?":
		return m.handleHelpToggleKey()
	case "P":
		return m.togglePromptMode()
	case "p":
		if m.screen == screenDashboard {
			return m.openQuickPromptOnSelected()
		}
		return m, nil
	case ":":
		m.cmdMode = true
		m.cmdInput.SetValue("")
		m.cmdInput.Focus()
		// Opening the command bar dismisses the previous output panel.
		m.shellOutput = nil
		m.shellOutputScroll = 0
		return m, nil
	case "ctrl+l":
		return m, tea.ClearScreen
	case "/":
		return m.handleSearchSlashKey()
	case "q":
		// q always means quit (with confirmation) — never context-sensitive
		// "back". esc is the only back key.
		m.confirming = "quit"
		return m.updateConfirm(msg)
	case "esc":
		return m.handleEscKey()
	case keyShiftEsc:
		if m.screen == screenCrew {
			m.screen = m.prevScreen
		}
		return m, nil
	case "+", "=", "-", "_":
		return m.handleAutoToggleKey(msg)
	}

	switch m.screen {
	case screenDashboard:
		return m.updateSplitKey(msg)
	case screenHelp:
		return m.updateHelpKey(msg)
	case screenCrew:
		return m.updatePromptKey(msg)
	default:
		// screenForm and any other screen route through their own key
		// handler earlier (form/overlay dispatch happens before this
		// switch); nothing left to do here.
	}
	return m, nil
}

// handleHelpToggleKey handles "?", toggling the help screen.
func (m Model) handleHelpToggleKey() (tea.Model, tea.Cmd) {
	if m.screen == screenHelp {
		m.screen = m.prevScreen
	} else {
		m.prevScreen = m.screen
		m.screen = screenHelp
	}
	return m, nil
}

// handleSearchSlashKey handles "/", opening the dashboard's search bar —
// filtering only applies to list-shaped screens, so it's a no-op elsewhere.
func (m Model) handleSearchSlashKey() (tea.Model, tea.Cmd) {
	if m.screen == screenDashboard {
		m.searchMode = true
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.Focus()
	}
	return m, nil
}

// handleEscKey handles a bare "esc": dismissing the shell output panel takes
// priority, then navigating back — except on screenCrew, where esc is
// already consumed by Prompt Mode's own overlays before this switch is ever
// reached (see dispatchOverlayKey's quickPromptMode/screenForm guards);
// shift+esc owns leaving Prompt Mode entirely instead.
func (m Model) handleEscKey() (tea.Model, tea.Cmd) {
	// If the shell output panel is visible, Escape dismisses it first
	// before navigating back — so a single Esc always clears the panel.
	if len(m.shellOutput) > 0 {
		m.shellOutput = nil
		m.shellOutputScroll = 0
		return m, nil
	}
	if m.screen != screenDashboard && m.screen != screenCrew {
		m.screen = m.prevScreen
	}
	return m, nil
}

// handleAutoToggleKey handles "+"/"="/"-"/"_", toggling auto-run for the
// selected bead on or off.
func (m Model) handleAutoToggleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	bead, ok := m.listSelection()
	if !ok {
		return m, m.notify("no bead selected")
	}
	brn := string(bead.BRN)
	if msg.String() == "+" || msg.String() == "=" {
		delete(m.autoOffBRNs, brn)
		return m, m.notify("Auto: ON for " + brn)
	}
	if m.autoOffBRNs == nil {
		m.autoOffBRNs = make(map[string]bool)
	}
	m.autoOffBRNs[brn] = true
	return m, m.notify("Auto: OFF for " + brn)
}

// dispatchOverlayKey runs updateKey's sequential mode guards — termFocus,
// promptTermFocus, statusForm, effortForm, modelForm, cmdMode, searchMode,
// quickPromptMode, screenForm, confirming — in the exact order updateKey
// itself used to inline. The order is load-bearing (e.g. termFocus must be
// checked before promptTermFocus, and both before confirming) so this must
// stay one function, not a reorderable table. Returns handled=false with the
// (possibly mutated, e.g. termFocus/promptTermFocus cleared) Model when none
// of the guards apply, so the caller continues to its own key switch.
//
// Note: m.huhForm only ever backs the confirm dialog, and is forwarded from
// within updateConfirm (via m.confirming != "") — that's the one place that
// also knows how to run the confirmed action once the form completes. A
// generic forward here would intercept keys (e.g. a stray "x") before
// m.confirming's own key handling runs.
func (m Model) dispatchOverlayKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.termFocus {
		if next, cmd, handled := m.handleTermFocusKey(msg); handled {
			return asModel(next), cmd, true
		}
	}
	// Prompt Mode's left-pane live agent chat forwards keys the same way
	// Board Mode's own embedded terminal does just above — a separate gate
	// since m.promptSessions is a fully separate registry (see that field's
	// doc comment), never m.sessionFor/m.currentKind.
	if m.screen == screenCrew && m.promptTermFocus {
		if next, cmd, handled := m.handlePromptTermFocusKey(msg); handled {
			return asModel(next), cmd, true
		}
	}
	return m.dispatchOverlayState(msg)
}

func (m Model) dispatchOverlayState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.statusForm != nil {
		model, cmd := m.updateStatusMenuKey(msg)
		return model, cmd, true
	}
	if m.effortForm != nil {
		model, cmd := m.updateEffortPickerKey(msg)
		return model, cmd, true
	}
	if m.modelForm != nil || m.modelChoices != nil {
		model, cmd := m.updateModelPickerKey(msg)
		return model, cmd, true
	}
	if m.cmdMode {
		model, cmd := m.updateCommandBar(msg)
		return model, cmd, true
	}
	if m.searchMode {
		model, cmd := m.updateSearchKey(msg)
		return model, cmd, true
	}
	if m.quickPromptMode {
		model, cmd := m.updateQuickPromptKey(msg)
		return model, cmd, true
	}
	if m.screen == screenForm {
		model, cmd := m.updateFormKey(msg)
		return model, cmd, true
	}
	if m.confirming != "" {
		model, cmd := m.updateConfirm(msg)
		return model, cmd, true
	}
	return m, nil, false
}

// handleTermFocusKey handles a key while m.termFocus is set — the terminal
// owns the keyboard, so every key forwards to the agent's pty, including a
// bare esc — opencode (and most agent TUIs) use plain Esc themselves
// (closing a menu, canceling input, and critically, esc-esc is opencode's
// OWN gesture to interrupt a running turn); BARON must never intercept it,
// or a user meaning to stop the agent instead gets bounced out of focus
// with the agent left running. shift+esc is BARON's own release key
// precisely because it cannot collide with anything an agent CLI does with
// plain Escape. Returns handled=false (with termFocus cleared) when the
// session has already finished, so the caller's guard chain continues.
func (m Model) handleTermFocusKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if t := m.sessionFor(m.currentKind()); t != nil && !t.done {
		if msg.String() == keyShiftEsc {
			m.termFocus = false
			t.focus = false
			// Zoomed and focused is a dead end otherwise: the split
			// (with its nav) is off screen, so this release must clear
			// zoom too, not leave the terminal filling the screen with
			// no visible way to reach 'z'.
			m.termZoom = false
			t.zoom = false
			m.resizeSessions()
			return m, m.notify("terminal unfocused"), true
		}
		t.sendKeys(msg)
		return m, nil, true
	}
	m.termFocus = false
	return m, nil, false
}

// handlePromptTermFocusKey is handleTermFocusKey's Prompt Mode counterpart —
// the left pane's live agent chat, forwarding keys the same way while
// m.promptTermFocus is set.
func (m Model) handlePromptTermFocusKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if id := m.activePromptAgentID(); id != "" {
		if t := m.promptSessions[id]; t != nil && !t.done {
			if msg.String() == keyShiftEsc {
				m.promptTermFocus = false
				t.focus = false
				m.resizePromptSessions()
				return m, m.notify("agent chat unfocused"), true
			}
			t.sendKeys(msg)
			return m, nil, true
		}
	}
	m.promptTermFocus = false
	return m, nil, false
}

// updateSearchKey handles the / search bar. Typing
// filters live; Esc clears and closes; Enter commits.
func (m Model) updateSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Key().Code {
	case tea.KeyEscape:
		m.exitSearchMode()
		return m, nil
	case tea.KeyEnter:
		m.searchMode = false
		m.searchInput.Blur()
		return m, nil
	default:
		// Every other key (runes, backspace, arrows) is text input; handled
		// below via the textinput component.
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	m.recompileSearchQuery()
	m.jumpToFirstSearchMatch()
	return m, cmd
}

// exitSearchMode clears search state and, when a bead was selected, jumps
// the dashboard's tab/cursor back to wherever it actually lives.
//
// While search was active, currentRows() (and so m.listCursor) indexed into
// searchAllRows() — a cross-tab, deduped, filtered list. Clearing
// searchQuery makes currentRows() revert to this tab's own bucketRows(), a
// different ordering the same numeric cursor can silently misindex into
// (e.g. a search that matched a hierarchical child as searchAllRows()'s
// only/first row leaves listCursor at 0, which after this reverts to the
// tab's own tree order — index 0 there is the child's own *epic*, not the
// child search actually found). m.detail was kept correct throughout
// (selectListRow reads it from whichever list was active at the time), so
// it is the source of truth to reselect by, not the stale index — jump to
// whatever tab/sub-tab actually holds it first, since a cross-tab match can
// leave the dashboard on a tab that never contained it at all.
func (m *Model) exitSearchMode() {
	m.searchMode = false
	m.searchQuery = ""
	m.searchRegex = nil
	m.searchInput.Blur()
	m.searchInput.SetValue("")
	if m.detail.BRN == "" {
		return
	}
	if tab, sub, ok := m.tabContaining(m.detail.BRN); ok {
		m.listTab, m.listSubTab = tab, sub
	}
	for i, r := range m.currentRows() {
		if r.bead.BRN == m.detail.BRN {
			m.listCursor = i
			break
		}
	}
}

// recompileSearchQuery syncs m.searchQuery from the search input and
// recompiles m.searchRegex — a case-insensitive regexp, falling back to nil
// (plain substring matching) when the query doesn't parse as one, or is
// empty.
func (m *Model) recompileSearchQuery() {
	m.searchQuery = m.searchInput.Value()
	if m.searchQuery == "" {
		m.searchRegex = nil
		return
	}
	if re, err := regexp.Compile("(?i)" + m.searchQuery); err == nil {
		m.searchRegex = re
	} else {
		m.searchRegex = nil
	}
}

// jumpToFirstSearchMatch moves the split-pane dashboard's selection to the
// first row matching the current search query, so the right pane shows a
// filtered result — a no-op outside the dashboard or with an empty query.
func (m *Model) jumpToFirstSearchMatch() {
	if m.screen != screenDashboard || m.searchQuery == "" {
		return
	}
	for i, r := range m.currentRows() {
		if m.searchMatches(r.bead) {
			m.listCursor = i
			m.selectListRow()
			return
		}
	}
}

func (m Model) updateConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action := m.confirming
	if action == "" {
		return m, nil
	}

	// If a huh confirm form is active, forward the message to it.
	if m.huhForm != nil {
		form := m.huhForm
		newModel, cmd := form.Update(msg)
		newForm, ok := newModel.(*huh.Form)
		if !ok {
			return m, cmd
		}
		m.huhForm = newForm

		if newForm.State == huh.StateAborted {
			m.confirming = ""
			m.huhForm = nil
			m.confirmAction = ""
			m.confirmResult = nil
			return m, nil
		}

		// Accept/Reject advance a single-field group via an async
		// NextField/nextGroupMsg round trip that only a real tea.Program
		// would pump back into Update, so newForm.State never reaches
		// StateCompleted synchronously here. A non-nil cmd from that
		// advance is the synchronous signal instead — same convention as
		// updateStatusMenuKey.
		if newForm.State == huh.StateCompleted || cmd != nil {
			confirmed := m.confirmResult != nil && *m.confirmResult
			m.confirming = ""
			m.huhForm = nil
			m.confirmAction = ""
			m.confirmResult = nil
			if confirmed {
				return m.runConfirmedAction(action)
			}
			return m, nil
		}
		return m, cmd
	}

	// First time opening confirm - create huh form (don't process the trigger key)
	var result bool
	prompt := confirmPrompt(action, m)
	confirm := huh.NewConfirm().
		Title(prompt).
		Affirmative("y").
		Negative("n").
		Value(&result)
	form := newOverlayForm(huh.NewGroup(confirm))
	m.huhForm = initHuhForm(form)
	m.confirmAction = action
	m.confirmResult = &result

	// Process the current key if it's a confirmation key (y/n/esc)
	// This handles cases where the action was set by a different key (e.g., x for stop)
	// and the user immediately presses y/n/esc to confirm/decline
	if msg.String() == "y" || msg.String() == "n" || msg.String() == "esc" {
		return m.updateConfirm(msg)
	}

	return m, nil
}

func confirmPrompt(action string, m Model) string {
	switch action {
	case "quit":
		return "Quit BARON?"
	case "stop":
		return fmt.Sprintf("Stop %s and send it to Needs You for a human decision?", m.detail.BRN)
	}
	return "Confirm?"
}

func (m Model) runConfirmedAction(action string) (tea.Model, tea.Cmd) {
	switch action {
	case "quit":
		m.quitting = true
		return m, m.quitCmd()
	case "stop":
		// 'x': one gesture, tab-independent — stop the agent if it's
		// running, then hand the bead to a human (human_queue, the "Needs
		// You" column). It used to differ by which detail tab happened to
		// be focused (kill the diff view vs. close the bead outright,
		// neither of which is "stop and queue for a human"); ctrl+d is the
		// dedicated close-the-bead gesture, so 'x' no longer needs to
		// double as one.
		brn := string(m.detail.BRN)
		var cmds []tea.Cmd
		if killCmd := m.killSession(kindAgent); killCmd != nil {
			cmds = append(cmds, killCmd)
		}
		cmds = append(cmds, runTypedCommand(func() (string, error) {
			return m.deps.ChangeStatus(brn, string(domain.BeadStateHumanQueue))
		}))
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

// startCommandPrefill opens the command bar with a partially typed line
// (ponytail: used by assign/reassign/request-changes instead of a dedicated
// picker overlay).
func (m Model) startCommandPrefill(line string) Model {
	m.cmdMode = true
	m.cmdInput.SetValue(line)
	m.cmdInput.SetCursor(len(line))
	m.cmdInput.Focus()
	return m
}

func (m Model) updateCommandBar(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == keyShiftEsc || key == "esc" {
		return m.closeCommandBar()
	}
	if key == "enter" {
		return m.submitCommandBar()
	}
	if key == "up" || key == "down" {
		return m.navigateCommandHistory(key)
	}
	if key == "shift+up" || key == keyShiftDown {
		delta := 1
		if key == keyShiftDown {
			delta = -1
		}
		m.scrollShellOutput(delta)
		return m, nil
	}
	var cmd tea.Cmd
	m.cmdInput, cmd = m.cmdInput.Update(msg)
	return m, cmd
}

func (m Model) closeCommandBar() (tea.Model, tea.Cmd) {
	m.cmdMode = false
	m.cmdInput.Blur()
	m.shellOutput = nil
	return m, nil
}

func (m Model) submitCommandBar() (tea.Model, tea.Cmd) {
	m.shellOutput, m.shellOutputScroll = nil, 0
	line := strings.TrimSpace(m.cmdInput.Value())
	m.cmdInput.SetValue("")
	m.shellHistIdx = -1
	if line == "" {
		return m, nil
	}
	if len(m.shellHistory) == 0 || m.shellHistory[len(m.shellHistory)-1] != line {
		m.shellHistory = append(m.shellHistory, line)
	}
	return m, runShellCmd(m.ctx, m.deps, line)
}

func (m Model) navigateCommandHistory(key string) (tea.Model, tea.Cmd) {
	if len(m.shellHistory) == 0 {
		return m, nil
	}
	if key == "up" {
		if m.shellHistIdx == -1 {
			m.shellHistIdx = len(m.shellHistory) - 1
		} else if m.shellHistIdx > 0 {
			m.shellHistIdx--
		}
		m.cmdInput.SetValue(m.shellHistory[m.shellHistIdx])
		m.cmdInput.SetCursor(len(m.cmdInput.Value()))
		return m, nil
	}
	if m.shellHistIdx == -1 {
		return m, nil
	}
	if m.shellHistIdx < len(m.shellHistory)-1 {
		m.shellHistIdx++
		m.cmdInput.SetValue(m.shellHistory[m.shellHistIdx])
		m.cmdInput.SetCursor(len(m.cmdInput.Value()))
	} else {
		m.shellHistIdx = -1
		m.cmdInput.SetValue("")
	}
	return m, nil
}

// oneLineStatus collapses command output into a single footer line, so a
// multi-line ":" command result never wraps the footer.
func oneLineStatus(out string) string {
	for ln := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			return ln
		}
	}
	return ""
}

// createdBRN extracts the bead BRN from `work create`'s confirmation line
// ("created <brn>: <title>"), or "" when the output isn't a create result.
func createdBRN(out string) string {
	m := createdBRNRe.FindStringSubmatch(oneLineStatus(out))
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

var createdBRNRe = regexp.MustCompile(`^created ([a-z0-9-]+):`)

// focusNoticeText is the toast text for a terminal focus change — shared by
// 't' and 'z' (zooming focuses too, see updateSplitKey's "z" case) so both
// paths read identically instead of drifting into two slightly different
// wordings.
func focusNoticeText(focused bool) string {
	if focused {
		return "terminal focused — type into the agent (shift+esc to come back)"
	}
	return "terminal unfocused"
}

// updateSplitKey handles keys on the split-pane dashboard: list navigation,
// tab switching (Tab/Shift+Tab or 1-4), and per-bead actions. While the
// embedded terminal is focused ('t') every key is forwarded to the agent's
// pty except esc, which returns focus to the TUI. (Focus is handled in
// updateKey before this dispatch, so the terminal never reaches here while
// focused.)
