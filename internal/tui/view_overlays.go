package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

func (m Model) viewModelPicker() string {
	if m.modelForm == nil {
		return ""
	}
	form := m.modelForm
	// huh's Select.Title() is never shown here: with Filtering(true) always
	// on, the field's title line permanently doubles as the filter row (see
	// field_select.go's titleView — it renders s.filter.View() in place of
	// the title whenever s.filtering is true), so the title text itself
	// never reaches the screen. Render it ourselves above the form, same
	// convention as viewStatusMenu's OverlayTitle.
	content := m.styles.OverlayTitle.Render("Assign model") + "\n\n" + form.View()
	box := m.overlayBox().Render(content)
	if m.width > 0 && m.height > 4 {
		return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

// viewStatusMenu renders the status-change menu as a centered overlay box,
// driven by statusForm's own View() — same convention as viewEffortPicker/
// viewModelPicker. This used to render from statusTargets/statusCursor
// directly (a leftover from before the menu was backed by huh at all): the
// key handling had already moved onto huh.Form.Update, but nothing ever
// pointed the *view* at the form it was driving, so j/k or the arrow keys
// silently updated the form's real selection (and thus statusResult, and
// thus which status actually got applied on enter) while the screen kept
// showing the cursor frozen on the first option — pressing down looked like
// it did nothing, but "enter" right after would apply whatever the
// unrendered selection had actually moved to.
// overlayBox returns the modal container sized to the terminal: the
// classic fixed 76-column width pushed every modal past the right edge on
// narrower terminals. Unsized models (width <= 0) keep the historical width.
func (m Model) overlayBox() lipgloss.Style {
	w := overlayBoxMaxWidth
	if m.width > 0 && m.width < w {
		w = m.width
	}
	return m.styles.OverlayBox.Width(w)
}

const overlayBoxMaxWidth = 76

func (m Model) viewStatusMenu() string {
	if m.statusForm != nil {
		form := m.statusForm
		box := m.overlayBox().Render(form.View())
		if m.width > 0 && m.height > 4 {
			return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, box)
		}
		return box
	}
	// Fallback (should not happen once the form is initialized).
	var b strings.Builder
	b.WriteString(m.styles.OverlayTitle.Render("Change status"))
	b.WriteString("\n\n")
	for i, target := range m.statusTargets {
		if i == m.statusCursor {
			b.WriteString(m.styles.ListCursor.Render("▌ "))
			b.WriteString(m.styles.CardSelected.Render(string(target)))
		} else {
			fmt.Fprintf(&b, "  %s", target)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.styles.Muted.Render("enter: apply  esc: cancel"))
	box := m.overlayBox().Render(b.String())
	if m.width > 0 && m.height > 4 {
		return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

// viewEffortPicker renders the follow-up reasoning-effort overlay that
// opens after a model choice with selectable effort (Deps.EffortChoices) —
// esc/q skips it and assigns with the agent's own default effort, matching
// updateEffortPickerKey.
func (m Model) viewEffortPicker() string {
	if m.effortForm != nil {
		form := m.effortForm
		box := m.overlayBox().Render(form.View())
		if m.width > 0 && m.height > 4 {
			return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, box)
		}
		return box
	}
	// Fallback (should not happen once form is initialized)
	var b strings.Builder
	b.WriteString(m.styles.OverlayTitle.Render("Reasoning effort — " + m.pendingAssignModel))
	b.WriteString("\n\n")
	for i, level := range m.effortChoices {
		if i == m.effortCursor {
			b.WriteString(m.styles.ListCursor.Render("▌ "))
			b.WriteString(m.styles.CardSelected.Render(level))
		} else {
			fmt.Fprintf(&b, "  %s", level)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.styles.Muted.Render("enter: apply  esc: skip (agent default)"))
	box := m.overlayBox().Render(b.String())
	if m.width > 0 && m.height > 4 {
		return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

// viewConfirmV2 renders the yes/no confirm overlay, centered when the
// terminal size is known. Yes is [Y] (enter/y), No is [N] (n/esc/q) — the
// only keys the confirm scope offers. Any other key leaves the dialog up
// (modal): a stray keystroke must not silently dismiss a confirmation.
func (m Model) viewConfirmV2() string {
	var prompt string
	switch m.confirming {
	case "quit":
		prompt = "Quit BARON?"
	case "kill-agent":
		if m.currentKind() == kindDiff {
			prompt = fmt.Sprintf("Close the diff view for %s?", m.detail.BRN)
		} else {
			prompt = fmt.Sprintf("Stop the agent for %s?", m.detail.BRN)
		}
	case "close":
		prompt = fmt.Sprintf("Close %s?", m.detail.BRN)
	}
	yes := m.styles.CardSelected.Render("[Y] Yes")
	no := m.styles.CardSelected.Render("[N] No (esc/q)")
	var b strings.Builder
	b.WriteString(m.styles.OverlayTitle.Render("Confirm"))
	b.WriteString("\n\n")
	b.WriteString(prompt)
	b.WriteString("\n\n")
	b.WriteString(yes)
	b.WriteString("  ")
	b.WriteString(no)
	box := m.overlayBox().Render(b.String())
	if m.width > 0 && m.height > 4 {
		return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

// listPaneRows is how many rows fit in a list screen's body (title 1 +
// blank 1 + box borders 2 + remaining chrome 4).
