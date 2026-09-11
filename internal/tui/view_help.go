package tui

import (
	"fmt"
	"strings"
)

func (m Model) viewHelpV2() string {
	var b strings.Builder
	b.WriteString(m.styles.Accent.Render("▎"))
	b.WriteString(m.styles.ScreenTitle.Render(" Help"))
	helpW := 60
	if m.width > helpIndent+20 {
		helpW = m.width - helpIndent
	}
	for _, section := range bindingsByScope() {
		fmt.Fprintf(&b, "\n\n%s:\n", m.styles.DetailHead.Render(string(section.scope)))
		for _, bind := range section.bindings {
			wrapped := strings.Split(m.wrap(helpW, bind.help), "\n")
			fmt.Fprintf(&b, "  %-14s %s\n", bind.key, wrapped[0])
			for _, cont := range wrapped[1:] {
				fmt.Fprintf(&b, "%*s%s\n", helpIndent, "", cont)
			}
		}
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if m.height > 0 && len(lines) > m.height-3 {
		h := m.height - 3
		start := min(m.helpScroll, len(lines)-h)
		return strings.Join(lines[start:start+h], "\n")
	}
	return b.String()
}
