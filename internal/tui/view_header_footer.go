package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

func (m Model) viewHeaderV2() string {
	// Left is always the BARON brand, never the working-directory path — the
	// path is project metadata, not identity, and cluttered the one place
	// that should read at a glance.
	left := m.styles.HeaderProj.Render("BARON")
	if m.deps.Version != "" {
		left += m.styles.HeaderStat.Render(" v" + m.deps.Version)
	}
	// Prompt Mode has no single selected bead of its own the way Board
	// Mode does (the thread pane follows the worklist selection, which
	// changes with every persona/row move) — the usual git/agent stats
	// below would describe whichever bead Board Mode's list last pointed
	// at, which reads as stale noise here. Swap in a persona-scoped
	// summary instead ("sağ üst taraf prompt mode için uygun göstergeler
	// olsun veya boş olsun" — the user's own framing).
	if m.screen == screenCrew {
		return m.layoutHeader(left, m.personaSummaryLine(), false)
	}

	var parts []string
	if s := m.personaAmbientStat(); s != "" {
		parts = append(parts, s)
	}
	if s := m.headerAgentsStat(); s != "" {
		parts = append(parts, s)
	}
	parts = append(parts, m.headerGitStatParts()...)
	parts = append(parts, m.headerAutoRunPart())

	if len(parts) == 0 {
		return left
	}
	right := strings.Join(parts, m.styles.HeaderStat.Render(" │ "))
	return m.layoutHeader(left, right, true)
}

// personaAmbientStat is Board Mode's own ambient "is a persona doing
// something right now" signal (crew-mode.md §2's whole point: you
// shouldn't have to open Prompt Mode just to find out) — but only once
// personas have been loaded at least once this session (Prompt Mode's own
// entry does this eagerly; Board Mode deliberately never polls on its own,
// same "fetch on demand, not continuously" discipline every persona fetch
// in this app follows). First visit to Prompt Mode "primes" this for the
// rest of the session. Empty until then.
func (m Model) personaAmbientStat() string {
	if len(m.personas) == 0 {
		return ""
	}
	running := 0
	for _, p := range m.personas {
		if m.personaStatuses[p.ID].Running {
			running++
		}
	}
	if running == 0 {
		return m.styles.HeaderStat.Render("personas idle")
	}
	return m.styles.HeaderVal.Foreground(m.styles.color(ansiAccent)).Render(strconv.Itoa(running)) +
		m.styles.HeaderStat.Render(" running")
}

// headerAgentsStat leads the right-hand stats when non-empty — it's the one
// figure that answers "is anything running" regardless of which bead is
// selected, so it stays leftmost (closest to the brand) rather than
// trailing behind numbers that change with the selection.
func (m Model) headerAgentsStat() string {
	if m.headerStats.Agents == 0 {
		return ""
	}
	return m.styles.HeaderVal.Foreground(m.styles.color(ansiAccent)).Render(strconv.Itoa(m.headerStats.Agents)) + m.styles.HeaderStat.Render(" agents")
}

// headerGitStatParts renders the branch/changed/deleted/untracked/diff
// stats, empty when there's no current branch to report against.
func (m Model) headerGitStatParts() []string {
	if m.headerStats.Branch == "" {
		return nil
	}
	return []string{
		m.styles.HeaderStat.Render("⎇ ") + m.styles.HeaderVal.Foreground(m.styles.color(ansiAccent)).Render(m.headerStats.Branch),
		m.styles.HeaderVal.Foreground(m.styles.color(ansiAmber)).Render(strconv.Itoa(m.headerStats.ChangedFiles)) + m.styles.HeaderStat.Render(" changed"),
		m.styles.HeaderVal.Foreground(m.styles.color(ansiRed)).Render(strconv.Itoa(m.headerStats.DeletedFiles)) + m.styles.HeaderStat.Render(" deleted"),
		m.styles.HeaderVal.Foreground(m.styles.color(ansiAmber)).Render(strconv.Itoa(m.headerStats.UntrackedFiles)) + m.styles.HeaderStat.Render(" untracked"),
		m.styles.HeaderVal.Foreground(m.styles.color(ansiGreen)).Render("+"+strconv.Itoa(m.headerStats.DiffInserted)) +
			" " + m.styles.HeaderVal.Foreground(m.styles.color(ansiRed)).Render("-"+strconv.Itoa(m.headerStats.DiffDeleted)),
	}
}

// headerAutoRunPart renders the selected bead's auto-run indicator — always
// present, ON by default unless the selected bead opted out.
func (m Model) headerAutoRunPart() string {
	autoLabel := "Auto: ON"
	autoColor := ansiGreen
	if bead, ok := m.listSelection(); ok && m.autoOffBRNs[string(bead.BRN)] {
		autoLabel = "Auto: OFF"
		autoColor = ansiDim
	}
	return m.styles.HeaderVal.Foreground(m.styles.color(autoColor)).Render(autoLabel)
}

// layoutHeader combines left and right into one header line: right-aligned
// against m.width when both fit, else just left — truncateLeft additionally
// truncates left itself when even that alone overflows (Board Mode only;
// Prompt Mode's own header never truncates its brand).
func (m Model) layoutHeader(left, right string, truncateLeft bool) string {
	if m.width <= 0 {
		return left + "  " + right
	}
	if pad := m.width - lipgloss.Width(left) - lipgloss.Width(right); pad >= 0 {
		return left + strings.Repeat(" ", pad) + right
	}
	if !truncateLeft || lipgloss.Width(left) <= m.width {
		return left
	}
	return m.trunc(m.width, left)
}

// personaSummaryLine renders "N/M personas enabled" — Prompt Mode's header
// (the persona layer has no single selected bead the way Board Mode does,
// so this replaces the usual git/agent stats there entirely).
func (m Model) personaSummaryLine() string {
	if len(m.personas) == 0 {
		return ""
	}
	enabled := 0
	for _, p := range m.personas {
		if p.Enabled {
			enabled++
		}
	}
	return m.styles.HeaderVal.Foreground(m.styles.color(ansiAccent)).Render(strconv.Itoa(enabled)) +
		m.styles.HeaderStat.Render(fmt.Sprintf("/%d personas enabled", len(m.personas)))
}

// hintPair is one footer hint: a key (bold) and its label (muted).
type hintPair struct{ key, label string }

// footerHints returns the per-screen hint pairs. While an overlay is open
// (confirm / pickers), the footer shows only that overlay's scope — the
// dashboard hints underneath would imply dead keys.
func (m Model) footerHints() []hintPair {
	if m.confirming != "" {
		return []hintPair{{"enter/y", "yes"}, {"n/esc", "no"}}
	}
	if m.effortChoices != nil {
		return []hintPair{{"j/k", "move"}, {"enter", "apply"}, {"esc", "skip"}}
	}
	if m.modelChoices != nil {
		return []hintPair{{"type", "filter"}, {"j/k", "move"}, {"enter", "assign"}, {"esc", "cancel"}}
	}
	if m.statusTargets != nil {
		return []hintPair{{"j/k", "move"}, {"enter", "apply"}, {"esc", "cancel"}}
	}
	if m.quickPromptMode {
		return []hintPair{{"enter", "send"}, {"esc", "cancel"}}
	}
	switch m.screen {
	case screenDashboard:
		return []hintPair{
			{"q", "quit"},
			{"↑/↓", "move"},
			{keyShiftLeftRight, "detail tab"},
			{"c", "comment"},
			{"p", "prompt"},
			{"s", "status"},
			{"a", "assign"},
			{"m", "merge"},
			{"x", "stop"},
			{"r", "run"},
			{"t", "terminal"},
			{"z", "zoom"},
			{"P", "prompt mode"},
			{"?", "help"},
		}
	case screenForm:
		// formKindComment is the only single-field form — "tab" has nothing
		// else to move to there, so it's the one case that omits the hint.
		if m.formKind == formKindComment {
			return []hintPair{{"ctrl+j/alt+enter", "newline"}, {"enter", "submit"}, {"esc", "cancel"}}
		}
		return []hintPair{{"tab", "next"}, {"ctrl+j/alt+enter", "newline"}, {"enter", "submit"}, {"esc", "cancel"}}
	case screenHelp:
		return []hintPair{{"j/k", "scroll"}, {"esc", "close"}}
	case screenCrew:
		// shift+left/right (tab) and shift+up/down (list cursor) always
		// act, regardless of focus — see updatePromptKey — so they're
		// always shown; only the per-tab verbs change.
		hints := []hintPair{
			{"tab", "focus"}, {keyShiftLeftRight, "tab"}, {keyShiftUpDown, "select"},
		}
		if m.promptRightTab == promptTabPersonas {
			hints = append(
				hints,
				hintPair{"l", "outputs"}, hintPair{"space", "on/off"},
				hintPair{"e", "edit"}, hintPair{"n", "new"}, hintPair{"r", "run"},
			)
		} else {
			hints = append(
				hints,
				hintPair{"n", "new"}, hintPair{"e", "edit"},
				hintPair{"c", "comment"}, hintPair{"o", "open in board"},
			)
		}
		hints = append(hints, hintPair{keyShiftEsc, "board mode"})
		return hints
	}
	return nil
}

// renderHints renders hint pairs as "key label" groups separated by two spaces.
func (m Model) renderHints(hints []hintPair) string {
	var b strings.Builder
	for i, h := range hints {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(m.styles.FooterKey.Render(h.key))
		b.WriteString(m.styles.FooterHint.Render(" " + h.label))
	}
	return b.String()
}

// viewFooterV2 renders the ": cmd" affordance on the left and the
// per-screen keyboard hints right-aligned; lowest-priority hints (the tail
// of footerHints, in declared order) are dropped first until everything
// fits the pane width.
func (m Model) viewFooterV2() string {
	hints := m.footerHints()
	left := m.styles.FooterCmd.Render(":shell")
	if m.width <= 0 {
		return left + "  " + m.renderHints(hints)
	}
	avail := m.width - lipgloss.Width(left)
	for len(hints) > 1 && lipgloss.Width(m.renderHints(hints)) > avail-2 {
		hints = hints[:len(hints)-1]
	}
	right := m.renderHints(hints)
	if pad := avail - lipgloss.Width(right); pad >= 0 {
		return left + strings.Repeat(" ", pad) + right
	}
	if lipgloss.Width(right) <= m.width {
		return right
	}
	return m.trunc(m.width, right)
}
