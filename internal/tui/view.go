package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// View implements tea.Model. Renders header + screen body + footer. The alt
// screen and mouse capture are declared here as View fields (v2 moved them
// out of the NewProgram options).
func (m Model) View() tea.View {
	v := tea.NewView(m.viewString())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// viewString builds the screen's string content; View wraps it into a
// tea.View with the terminal-mode fields.
func (m Model) viewString() string {
	if m.quitting {
		return ""
	}
	// Zoomed embedded terminal ('z'): the emulator fills the whole screen and
	// only its frame is drawn — no header, no footer, nothing else, so no
	// toast either (an error/status notice waits for the split to return).
	if m.termZoom && m.confirming == "" {
		kind := m.currentKind()
		if t := m.sessionFor(kind); t != nil {
			cols, rows := m.termDims()
			offset := 0
			if p := m.panesFor(kind)[t.brn]; p != nil {
				offset = p.offset
			}
			base := m.viewEmbeddedTerminal(t, cols, rows, offset)
			return m.viewWithToastAt(base, 0, 0, cols)
		}
	}
	var b strings.Builder
	b.WriteString(m.viewHeaderV2())
	b.WriteString("\n")

	switch {
	case m.confirming != "":
		b.WriteString(m.viewConfirmV2())
	case m.statusForm != nil:
		b.WriteString(m.viewStatusMenu())
	case m.effortForm != nil:
		b.WriteString(m.viewEffortPicker())
	case m.modelForm != nil:
		b.WriteString(m.viewModelPicker())
	default:
		switch m.screen {
		case screenDashboard:
			b.WriteString(m.viewSplitPane())
		case screenForm:
			b.WriteString(m.viewFormV2())
		case screenHelp:
			b.WriteString(m.viewHelpV2())
		case screenCrew:
			b.WriteString(m.viewPromptMode())
		}
	}

	b.WriteString("\n")
	switch {
	case m.cmdMode:
		b.WriteString(m.cmdInput.View())
	case m.searchMode:
		b.WriteString(m.searchInput.View())
	case m.quickPromptMode:
		b.WriteString(m.viewQuickPrompt())
	default:
		// The footer's hints stay put regardless of a pending notice — see
		// viewWithToast, which floats the error/status message as its own
		// box instead of stealing this row.
		b.WriteString(m.viewFooterV2())
	}
	base := b.String()
	base = m.viewWithToast(base)
	return m.viewWithShellPanel(base)
}

// shellOutputMaxLines is the maximum number of output lines the inline shell
// panel shows at once. The panel grows from 1 to this cap as output fills it;
// the rest is reachable by scrolling (mouse wheel or the scroll indicators).
const shellOutputMaxLines = 30

// toastMaxWidth caps a floating notice's content width so a long error
// message wraps into a readable box instead of stretching edge to edge.
const toastMaxWidth = 56

// viewToast renders the active error/status notice as styled text (no
// border yet — viewWithToast boxes it): a plain color-only line was easy to
// miss among the rest of the screen, and vanished completely under
// NO_COLOR, since color was the only signal it carried. Error takes
// priority over a status notice, matching the footer's old priority. ""
// means there is nothing to show.
func (m Model) viewToast() string {
	var style lipgloss.Style
	var text string
	switch {
	case m.err != nil:
		style, text = m.styles.ToastError, "✗ "+m.err.Error()
	case m.statusMsg != "":
		// "ℹ" (U+2139) rendered as a tofu box in a fresh freeze check even
		// though an older glyph-safety pass hadn't flagged it — see
		// typeInfos' doc comment. "•" is on the confirmed-safe list.
		style, text = m.styles.ToastNotice, "• "+m.statusMsg
	default:
		return ""
	}
	w := toastMaxWidth
	if m.width > 0 {
		w = min(w, max(1, m.width-8))
	}
	return style.Render(m.wrap(w, text))
}

// viewShellOutput renders the inline output panel for the last :cmd result.
// The panel shows the final N lines of output (newest at the bottom, matching
// how a real terminal works), capped to shellOutputMaxLines. A scroll
// indicator appears at the top when there is more content above.
func (m Model) viewShellOutput() string {
	if len(m.shellOutput) == 0 {
		return ""
	}
	total := len(m.shellOutput)
	h := min(shellOutputMaxLines, total)

	// Apply scroll: shellOutputScroll 0 = bottom of output (newest last).
	scroll := clampInt(m.shellOutputScroll, max(0, total-h))
	end := total - scroll
	start := max(0, end-h)
	visible := m.shellOutput[start:end]

	var b strings.Builder
	// Show a scroll hint at the top when there is hidden content above.
	if start > 0 {
		b.WriteString(m.styles.Muted.Render(fmt.Sprintf("↑ %d more line(s) — scroll up", start)))
		b.WriteString("\n")
	}
	for _, line := range visible {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if scroll > 0 {
		b.WriteString(m.styles.Muted.Render(fmt.Sprintf("↓ %d more line(s) — scroll down", scroll)))
		b.WriteString("\n")
	}
	content := strings.TrimRight(b.String(), "\n")
	boxW := m.width - 2
	if boxW < 10 {
		return m.styles.ShellOutput.Render(content)
	}
	return m.styles.ShellOutput.Width(boxW).Render(content)
}

// viewWithShellPanel composites base with the inline command-output panel (if
// any) as a full-width box floating above the footer row — the same
// compositor approach as viewWithToast but full-width instead of corner-pinned.
func (m Model) viewWithShellPanel(base string) string {
	panel := m.viewShellOutput()
	if panel == "" {
		return base
	}
	if m.width <= 0 || m.height <= 0 {
		return base + "\n" + panel
	}
	ph := lipgloss.Height(panel)
	bh := lipgloss.Height(base)
	// Position panel one row above the footer (base's last row).
	y := max(0, bh-ph-1)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(panel).X(0).Y(y).Z(2),
	).Render()
}

// toastRegion returns where a notice anchors: the right pane's own box on
// the split-pane dashboard or Prompt Mode (x0 = the pane's left edge,
// y0 one row inside its top border, w = the pane's width), or the top of
// the whole screen on every other screen — one common rule for "top-right,
// inside the active window" instead of each screen inventing its own
// placement.
func (m Model) toastRegion() (x0, y0, w int) {
	if m.screen == screenDashboard && !m.termZoom {
		leftW, rightW := splitPaneWidths(m.width)
		return leftW + 1, 2, rightW
	}
	if m.screen == screenCrew {
		leftW, rightW := promptPaneWidths(m.width)
		// Board Mode's own y0=2 (just inside the pane's top border) would
		// land on the right column's own tab-bar row here, or — a row or
		// two further down — on the top list's very first content line:
		// both boxes put meaningful text on their first row, unlike Board
		// Mode's single tall detail pane where row 2 is blank padding under
		// the title. A deliberately oversized y0 relies on viewWithToastAt's
		// own clamp (y := clampInt(y0, bh-th)) to bottom-align the toast
		// instead — the right column's lower rows are blank padding far more
		// often than its top rows are.
		return leftW + 1, m.height, rightW
	}
	return 0, 1, m.width
}

// viewWithToast composites base with the active notice (if any), anchored
// per toastRegion.
func (m Model) viewWithToast(base string) string {
	x0, y0, w := m.toastRegion()
	return m.viewWithToastAt(base, x0, y0, w)
}

// viewWithToastAt composites base with the active notice (if any) as a
// small floating box pinned to the top-right corner of the region [x0,
// x0+regionW), starting at row y0, via lipgloss's cell-accurate layer
// compositor (lipgloss.NewCompositor) — drawn on top without disturbing any
// cell it doesn't cover, unlike string surgery on individual lines. The
// notice always stays at least 5s (see notify/notifyDuration) regardless of
// keypresses or later notices in between — see statusExpiredMsg's gen
// guard — so this is purely a placement/render concern. Unsized (no
// WindowSizeMsg yet, or tests that never set one) falls back to appending
// the notice as a plain trailing line, so the total-line-count invariant
// only has to hold once a real size is known.
func (m Model) viewWithToastAt(base string, x0, y0, regionW int) string {
	toast := m.viewToast()
	if toast == "" {
		return base
	}
	if m.width <= 0 || m.height <= 0 {
		return base + "\n" + toast
	}
	tw, th := lipgloss.Width(toast), lipgloss.Height(toast)
	bw, bh := lipgloss.Width(base), lipgloss.Height(base)
	x := clampInt(x0+regionW-tw-2, max(0, bw-tw))
	// Keep the toast above the footer row (bh-1).
	maxY := max(0, bh-th-1)
	y := clampInt(y0, maxY)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(toast).X(x).Y(y).Z(1),
	).Render()
}

// viewHeaderV2 renders the project/status summary bar: project vVersion on
// the left, project metrics right-aligned on the right — nothing centered.
// Active/Needs You counts used to live here too, but they only duplicated
// what the left pane's tab bar already shows right next to the tabs
// themselves; the header's job now is to surface what's NOT shown
// anywhere else on the board (currently: git branch + changed-file count).
