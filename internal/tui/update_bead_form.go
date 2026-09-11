package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/agent"
)

func (m Model) openPromptNewBeadForm() tea.Model {
	var prefillParent string
	if sel, ok := m.promptSelectedBead(); ok && sel.IssueType == "epic" {
		prefillParent = string(sel.BRN)
	}
	m.formReturnScreen = screenCrew
	m.formKind = formKindPromptNewBead

	title, typ, tier, parent, desc, accept := "", "task", string(agent.TierFast), prefillParent, "", ""
	titleField := huh.NewInput().
		Title("Title").
		CharLimit(500).
		Value(&title)
	typeField := huh.NewSelect[string]().
		Title("Type").
		Options(m.newBeadFormTypeOptions()...).
		Value(&typ)
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
	return m
}

// openPromptEditBeadForm builds the Beads tab's 'e' — same field set as
// Board Mode's own inline 'e', same non-touching rationale as
// openPromptNewBeadForm above. Reuses formKindEditBead (not a new const):
// submitForm's existing case for it is already screen-agnostic (keys off
// m.detail, set here from the Prompt-owned cursor, same as Board Mode's
// own 'e' sets it from m.listSelection()).
func (m Model) openPromptEditBeadForm() tea.Model {
	bead, ok := m.promptSelectedBead()
	if !ok {
		return m
	}
	m.detail = bead
	m.formReturnScreen = screenCrew
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
	return m
}

// openQuickPromptOnSelected opens Board Mode's lowercase-'p' quick prompt:
// always anchored to m.detail (the currently selected bead) — v8's
// replacement for the old Developer-persona dispatch (docs/PRD/crew-mode.md
// §2.9): the submitted text is typed straight into whichever agent-CLI tab
// is currently active in Prompt Mode's left pane (quickPromptCmd), the
// left pane's live chat now being the one "manual prompt" surface.
func (m Model) openQuickPromptOnSelected() (tea.Model, tea.Cmd) {
	if m.detail.BRN == "" {
		return m, m.notify("select a bead first")
	}
	m.quickPromptMode = true
	m.quickPromptBRN = string(m.detail.BRN)
	m.quickPromptInput.SetValue("")
	m.quickPromptInput.Placeholder = "prompt — enter to send"
	m.quickPromptInput.Focus()
	m.applyQuickPromptInputWidth()
	return m, nil
}

// applyQuickPromptInputWidth sizes quickPromptInput against the current
// label (quickPromptPrefixWidth) and terminal width, so a long prompt
// scrolls within the input box instead of running the whole line off the
// terminal's right edge.
func (m *Model) applyQuickPromptInputWidth() {
	if m.width <= 0 {
		return
	}
	m.quickPromptInput.SetWidth(max(10, m.width-m.quickPromptPrefixWidth()-2))
}

// updateQuickPromptKey drives Board Mode's lowercase-'p' quick prompt box.
// esc cancels back without leaving the surrounding screen; enter sends the
// typed text into the active left-pane agent session (quickPromptCmd) —
// never a persona dispatch any more.
func (m Model) updateQuickPromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.quickPromptMode = false
		m.quickPromptInput.Blur()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.quickPromptInput.Value())
		m.quickPromptMode = false
		m.quickPromptInput.SetValue("")
		m.quickPromptInput.Blur()
		if text == "" {
			return m, nil
		}
		return m, quickPromptCmd(m, m.quickPromptBRN, text)
	}
	var cmd tea.Cmd
	m.quickPromptInput, cmd = m.quickPromptInput.Update(msg)
	return m, cmd
}

func (m Model) updateHelpKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down", "pgdown":
		m.helpScroll += 10
	case "k", "up", "pgup":
		m.helpScroll = max(0, m.helpScroll-10)
	case "g":
		m.helpScroll = 0
	case "G":
		m.helpScroll = len(bindings) * 2 // clamped at render
	}
	return m, nil
}

// updateFormKey drives the new-bead/comment overlay's huh.Form — the same
// forward-and-check-state convention as updateStatusMenuKey/
// updateModelPickerKey, plus one twist those single-field pickers don't
// need: with more than one field, huh's own nextField() always returns a
// non-nil cmd on every plain field-to-field advance (see charm.land/huh/v2's
// group.go), so "cmd != nil" can't mean "submitted" here the way it does for
// a one-field picker. Instead every keypress is driven all the way through
// via pumpHuhForm, and newForm.State is checked directly afterwards — this
// codebase never routes a running tea.Program's async messages back into
// Model.Update (there is no default case for it in Update's top-level
// switch), so nothing else will ever resolve huh's own nextField/nextGroup
// messages if we don't do it here ourselves.
// formFieldIsFiltering reports whether form's currently focused field is a
// Filterable MultiSelect actively in its own filter-typing mode — huh's own
// esc convention there is "clear/exit the filter," not "abandon the whole
// form" (see charm.land/huh/v2's keymap.go: MultiSelect's Filter/SetFilter/
// ClearFilter all bind esc). updateFormKey's blanket "esc aborts the form"
// intercept has to step aside for that case specifically, or a user who
// presses '/' to search the event-trigger picker (this codebase's first
// Filterable field) and then hits esc meaning "never mind, clear my
// search" instead loses every other field they'd already filled in.
