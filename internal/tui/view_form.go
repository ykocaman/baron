package tui

import (
	"charm.land/lipgloss/v2"
)

// viewFormV2 renders the form (new bead / add comment) as a centered popup
// box — never a full separate screen — so the user types and submits without
// losing the dashboard behind it. Driven by beadForm, a huh/v2 Form (see
// updateFormKey): huh owns field navigation, editing, and the select/text
// widgets themselves, same convention as viewModelPicker/viewStatusMenu.
func (m Model) viewFormV2() string {
	if m.beadForm == nil {
		return ""
	}
	form := m.beadForm
	var title string
	switch m.formKind {
	case formKindNewBead, formKindPromptNewBead:
		title = "New Bead"
	case formKindComment:
		title = "Add Comment"
	case formKindEditBead:
		title = "Edit Bead — " + string(m.detail.BRN)
	case formKindPersonaEdit:
		title = "Edit Persona — " + m.editingPersonaName
	case formKindPersonaNew:
		title = "New Persona"
	}
	content := m.styles.OverlayTitle.Render(title) + "\n\n" + form.View()
	box := m.overlayBox().Render(content)
	if m.width > 0 && m.height > 4 {
		return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}
