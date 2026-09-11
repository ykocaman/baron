package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

func (m Model) viewPromptMode() string {
	if m.width <= 0 {
		return m.viewPromptLeft(0) + " " + m.viewPromptRight(0)
	}
	leftW, rightW := promptPaneWidths(m.width)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.viewPromptLeft(leftW), " ", m.viewPromptRight(rightW))
}

// promptPaneWidths splits Prompt Mode's total width roughly in half
// between the left agent-chat pane and the right Personas/Beads column —
// unlike splitPaneWidths' list/detail asymmetry, both halves here hold
// comparably wide content (a live terminal on the left, a list+detail
// stack on the right). On narrow terminals the half-split gives way,
// shrinking left while keeping right's usable floor; left + right + 1
// (the join gap) never exceeds the terminal width.
func promptPaneWidths(total int) (left, right int) {
	left = max(total/2, 30)
	right = max(total-left-1, 24)
	if over := left + right + 1 - total; over > 0 {
		left -= over
		if left < 0 {
			right += left
			left = 0
			right = max(right, 0)
		}
	}
	return left, right
}

// promptRightTopRows is the right column's top box (the Personas/Beads
// list)'s inner row budget — a share of the column's total height
// (splitBoxInnerRows) rather than a fixed constant: a persona's full
// work-output accordion, or a bead tree with several epics, needs real
// room to browse, not a handful of rows squeezed to nothing on an
// ordinary terminal. 60% top / 40% bottom, with a floor on each so neither
// box collapses on a short terminal.
func (m Model) promptRightTopRows() int {
	return max(10, m.splitBoxInnerRows()*3/5)
}

// promptRightTopListRows is the list's own content budget within
// promptRightTopRows, after its tab-bar/blank chrome — shared by the
// clamp funcs (update.go) and the render funcs below so scroll windowing
// and rendering never disagree about how many rows are visible.
func (m Model) promptRightTopListRows() int {
	return max(1, m.promptRightTopRows()-2)
}

// promptRightBottomRows is the right column's bottom box (the detail
// pane)'s inner row budget: the same total column height Board Mode's own
// panes and the left pane use (splitBoxInnerRows) minus the top box above
// it (promptRightTopRows content rows plus its own 2 border rows).
func (m Model) promptRightBottomRows() int {
	return max(4, m.splitBoxInnerRows()-m.promptRightTopRows()-2)
}

// viewPromptLeft renders the left pane: a tab bar (one tab per active
// agent CLI, Deps.PromptAgentIDs) over whichever tab's live session is
// selected (m.promptAgentTab) — reuses viewEmbeddedTerminal verbatim, the
// same renderer Board Mode's own Terminal tab uses, since the underlying
// agentTerminal/pty/vt-emulator plumbing is identical, just a fully
// separate registry (m.promptSessions, keyed by agent ID not BRN).
func (m Model) viewPromptLeft(w int) string {
	title := m.styles.Accent.Render("▎") + m.styles.ScreenTitle.Render(" Agent Chat")
	lines := []string{title, m.promptAgentTabBar(), ""}

	inner := max(10, w-4)
	avail := m.splitBoxInnerRows()
	bodyRows := max(1, avail-len(lines))
	switch {
	case len(m.promptAgentIDs) == 0:
		lines = append(lines, m.styles.EmptyState.Render("no active agent CLI found"))
	case m.deps.PromptAgentEnsure == nil:
		lines = append(lines, m.styles.EmptyState.Render(msgAgentChatNotWiredUp))
	default:
		id := m.activePromptAgentID()
		if t := m.promptSessions[id]; t != nil {
			body := m.viewEmbeddedTerminal(t, inner, bodyRows, m.promptTermScrollOffset)
			lines = append(lines, strings.Split(body, "\n")...)
		} else {
			lines = append(lines, m.styles.EmptyState.Render("t to start chatting with "+id))
		}
	}
	for m.height > 0 && len(lines) < avail {
		lines = append(lines, "")
	}
	return m.fixedBox(w, strings.Join(lines, "\n"), m.promptFocus == promptPaneLeft)
}

// promptAgentTabBar renders the left pane's agent-CLI tab bar, the active
// tab highlighted — same CardSelected/Muted convention promptRightTabBar
// uses for Personas/Beads.
func (m Model) promptAgentTabBar() string {
	if len(m.promptAgentIDs) == 0 {
		return m.styles.Muted.Render("(none)")
	}
	var b strings.Builder
	for i, id := range m.promptAgentIDs {
		label := " " + id + " "
		if i == m.promptAgentTab {
			b.WriteString(m.styles.CardSelected.Render(label))
		} else {
			b.WriteString(m.styles.Muted.Render(label))
		}
	}
	return b.String()
}

// viewPromptRight stacks the top list box (Personas or Beads,
// promptRightTopRows) over the bottom detail box (promptRightBottomRows)
// — total height matches the left pane's own single box, same algebra
// v6's worklist/thread split used.
func (m Model) viewPromptRight(w int) string {
	return lipgloss.JoinVertical(lipgloss.Left, m.viewPromptRightTop(w), m.viewPromptRightBottom(w))
}

// promptRightTabBar renders the Personas/Beads tab bar shared by both
// right-side boxes' header — shift+left/right switches it.
func (m Model) promptRightTabBar() string {
	beads, personas := " Beads ", " Personas "
	if m.promptRightTab == promptTabPersonas {
		personas = m.styles.CardSelected.Render(personas)
		beads = m.styles.Muted.Render(beads)
	} else {
		personas = m.styles.Muted.Render(personas)
		beads = m.styles.CardSelected.Render(beads)
	}
	return beads + personas
}

// viewPromptRightTop renders the right column's top box: the tab bar, then
// the active tab's list (viewPromptPersonaList or viewPromptBeadList).
func (m Model) viewPromptRightTop(w int) string {
	topRows, listRows := m.promptRightTopRows(), m.promptRightTopListRows()
	lines := []string{m.promptRightTabBar()}
	if m.promptRightTab == promptTabBeads {
		inner := max(1, w-4)
		tabBar := m.renderBoardTabBar(m.promptBeadTab, inner)
		subTabBar := m.renderSubTabBar(m.promptBeadTab, m.promptBeadSubTab, inner)
		lines = append(lines, tabBar, subTabBar)
		listRows = max(1, listRows-2)
	} else {
		lines = append(lines, "")
	}
	if m.promptRightTab == promptTabPersonas {
		lines = append(lines, m.viewPromptPersonaList(w, listRows)...)
	} else {
		lines = append(lines, m.viewPromptBeadList(w, listRows)...)
	}
	for len(lines) < topRows {
		lines = append(lines, "")
	}
	return m.fixedBoxN(w, topRows, strings.Join(lines, "\n"), m.promptFocus == promptPaneRightTop)
}

// viewPromptPersonaList renders the Personas-tab list: one row per persona
// — an icon (status glyph) and its name, nothing else, per the redesign's
// "looks like the beads list" ask — plus, under whichever persona's
// accordion is open (m.promptPersonaOpenID), its own work-output rows
// (reusing promptWorklistLine), all walked from the SAME flattened
// sequence promptPersonaCursor navigates (promptPersonaFlatRows) so the
// highlighted row and the actually-selected row can never disagree.
// Windowed by flat-row index (m.promptPersonaScroll).
func (m Model) viewPromptPersonaList(w, rows int) []string {
	switch {
	case m.deps.Personas == nil:
		return []string{m.styles.HeaderStat.Render("Personas aren't wired up.")}
	case len(m.personas) == 0:
		return []string{m.styles.HeaderStat.Render("No personas found.")}
	}
	flat := m.promptPersonaFlatRows()
	start := clampInt(m.promptPersonaScroll, max(0, len(flat)-1))
	lines := m.promptPersonaVisibleLines(start, rows)
	body := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln.FlatIdx < 0 {
			body = append(body, "    "+m.styles.Muted.Render(ln.Placeholder))
			continue
		}
		row := flat[ln.FlatIdx]
		p := m.personas[row.personaIdx]
		var line string
		switch row.eventIdx {
		case -1:
			glyphLine := promptPersonaGlyphLine(p, m.personaStatuses[p.ID])
			if ln.FlatIdx == m.promptPersonaCursor {
				pad := max(0, w-4-2-lipgloss.Width(glyphLine))
				line = m.styles.ListCursor.Render("▌ ") + m.styles.CardSelected.Render(glyphLine+strings.Repeat(" ", pad))
				line = reapplySelection(line, m.styles.reselectSeq)
			} else {
				line = "  " + glyphLine
			}
		default:
			evLine := m.promptWorklistLine(m.promptPersonaActivity[p.ID][row.eventIdx])
			if ln.FlatIdx == m.promptPersonaCursor {
				pad := max(0, w-4-4-lipgloss.Width(evLine))
				line = m.styles.ListCursor.Render("▌ ") + "  " + m.styles.CardSelected.Render(evLine+strings.Repeat(" ", pad))
				line = reapplySelection(line, m.styles.reselectSeq)
			} else {
				line = "    " + evLine
			}
		}
		body = append(body, line)
	}
	return body
}

// promptPersonaGlyphLine is one Personas-tab row: a status icon (● running
// — a real tmux-checked fact from Deps.PersonaStatuses, ○ idle/enabled, ✕
// disabled) and the name, nothing else.
func promptPersonaGlyphLine(p persona.Persona, st PersonaStatus) string {
	glyph := "✕"
	if p.Enabled {
		glyph = "○"
	}
	if st.Running {
		glyph = "●"
	}
	return glyph + " " + p.Name
}

// viewPromptBeadList renders the Beads-tab list as a tree — epic headers,
// children indented with a tree connector, exactly Board Mode's own
// splitRowLine rendering over promptBeadRows()'s splitRow tree (see
// promptBeadTreeRows) — Prompt Mode's own cursor/scroll, never Board
// Mode's listCursor/listTab/listSubTab.
func (m Model) viewPromptBeadList(w, rows int) []string {
	beads := m.promptBeadRows()
	if len(beads) == 0 {
		msg := "No beads found."
		if m.promptBeadTab >= 0 && m.promptBeadTab < len(boardColumns) {
			msg = fmt.Sprintf("No beads in %s.", boardColumns[m.promptBeadTab].name)
		}
		return []string{m.styles.HeaderStat.Render(msg)}
	}
	start := clampInt(m.promptBeadScroll, max(0, len(beads)-rows))
	end := min(start+rows, len(beads))
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		r := beads[i]
		prefix := 2 + 2*r.depth
		line := m.splitRowLine(r, w, prefix)
		// Highlight the cursor row regardless of pane focus — see
		// viewPromptPersonaList's identical fix for why (shift+up/down
		// must visibly move the selection from any pane, not just when
		// promptPaneRightTop is focused).
		if i == m.promptBeadCursor {
			pad := max(0, w-4-2-2*r.depth-lipgloss.Width(line))
			line = m.styles.ListCursor.Render("▌ ") + strings.Repeat(" ", 2*r.depth) + m.styles.CardSelected.Render(line+strings.Repeat(" ", pad))
			line = reapplySelection(line, m.styles.reselectSeq)
		} else {
			line = strings.Repeat(" ", prefix) + line
		}
		lines = append(lines, line)
	}
	return lines
}

// viewPromptRightBottom renders the right column's bottom box: the active
// tab's detail pane (viewPromptPersonaDetail or viewPromptBeadDetail).
func (m Model) viewPromptRightBottom(w int) string {
	var content string
	if m.promptRightTab == promptTabPersonas {
		content = m.viewPromptPersonaDetail(w)
		if m.promptPersonaDetailScroll > 0 {
			lines := strings.Split(content, "\n")
			if m.promptPersonaDetailScroll < len(lines) {
				content = strings.Join(lines[m.promptPersonaDetailScroll:], "\n")
			} else {
				content = ""
			}
		}
	} else {
		content = m.viewPromptBeadDetail(w)
	}
	bottomRows := m.promptRightBottomRows()
	return m.fixedBoxN(w, bottomRows, content, m.promptFocus == promptPaneRightBottom)
}

// trimAndCapLines splits raw into lines, trims leading/trailing blank
// lines, and caps the result to the last maxLines — the shared shape every
// raw-output preview in viewPromptPersonaDetail (Run Detail, the open
// accordion's own top-of-detail log, and the closed accordion's
// end-of-detail "recent output") renders.
func trimAndCapLines(raw string, maxLines int) []string {
	lines := strings.Split(raw, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

// viewPromptPersonaDetail renders the selected persona's full details:
// header with name/id/source/status/model badges, description, trigger rules,
// skills, authority/permissions, instructions/prompt, and key action hints.
func (m Model) viewPromptPersonaDetail(w int) string {
	p, ok := m.selectedPersona()
	if !ok {
		return m.styles.EmptyState.Render("no persona selected")
	}
	inner := max(10, w-4)

	if row, hasRow := m.selectedPersonaRow(); hasRow && row.eventIdx >= 0 {
		if content, ok := m.personaRunDetailSection(p, row, inner); ok {
			return content
		}
	}

	var out strings.Builder
	out.WriteString(m.personaHeaderSection(p, inner))
	out.WriteString(m.personaOutputLogTopSection(p, inner))
	out.WriteString(m.personaDescriptionSection(p, inner))
	out.WriteString(m.personaPromptSection(p, inner))
	out.WriteString(m.personaTriggerSection(p))
	out.WriteString(m.personaSkillsSection(p))
	out.WriteString(m.personaAuthoritySection(p))
	out.WriteString(m.personaRecentOutputSection(p, inner))
	out.WriteString(m.styles.HeaderStat.Render("e edit · n new · space on/off · r run · l runs · ctrl+d delete"))
	return out.String()
}

// personaRunDetailSection renders row's own accordion work-output entry as
// the whole detail pane (header/badges/description/etc. are skipped
// entirely for this view) — ok is false when row doesn't actually resolve
// to a cached activity event, in which case the caller falls through to the
// persona's own regular detail sections instead.
func (m Model) personaRunDetailSection(p persona.Persona, row promptPersonaRow, inner int) (content string, ok bool) {
	events := m.promptPersonaActivity[p.ID]
	if row.eventIdx >= len(events) {
		return "", false
	}
	ev := events[row.eventIdx]
	var out strings.Builder
	out.WriteString(m.styles.Accent.Render("▎") + m.styles.ScreenTitle.Render(" "+p.Name+" ▸ Run Detail") + "\n")
	meta := fmt.Sprintf("%s · %s", p.ID, timeAgo(ev.Time))
	if ev.Target != "" {
		meta += " · " + m.styles.CardL1.Render(ev.Target)
	}
	out.WriteString(m.trunc(inner, meta) + "\n")
	out.WriteString(m.styles.Muted.Render(strings.Repeat("─", inner)) + "\n\n")

	out.WriteString(m.styles.DetailLabel.Render("Action: ") + m.styles.DetailValue.Render(ev.Action) + "\n")
	if ev.Target != "" {
		out.WriteString(m.styles.DetailLabel.Render("Target: ") + m.styles.DetailValue.Render(ev.Target) + "\n")
	}
	if ev.Detail != "" {
		out.WriteString(m.styles.DetailLabel.Render("Detail: ") + m.styles.DetailValue.Render(ev.Detail) + "\n")
	}
	out.WriteString("\n")

	out.WriteString(m.styles.SectionHead.Render("Output / Execution Log:") + "\n")
	rawOut, hasOut := m.promptPersonaOutput[p.ID]
	switch allLines := trimAndCapLines(rawOut, 25); {
	case !hasOut:
		out.WriteString("  " + m.styles.Muted.Render("loading output…") + "\n")
	case len(allLines) == 0:
		out.WriteString("  " + m.styles.Muted.Render("no output recorded for this run") + "\n")
	default:
		for _, ln := range allLines {
			out.WriteString("  " + m.trunc(inner-4, ln) + "\n")
		}
	}
	out.WriteString("\n")
	out.WriteString(m.styles.HeaderStat.Render("o open bead in board · l close runs · r run again"))
	return out.String(), true
}

// personaHeaderSection renders the Accent-bar persona name followed by its
// metadata badges (ID · running/enabled/disabled status · source · model).
func (m Model) personaHeaderSection(p persona.Persona, inner int) string {
	var out strings.Builder
	out.WriteString(m.styles.Accent.Render("▎") + m.styles.ScreenTitle.Render(" "+p.Name) + "\n")

	var badges []string
	badges = append(badges, m.styles.CardL1.Render(p.ID))

	st := m.personaStatuses[p.ID]
	switch {
	case st.Running:
		badges = append(badges, m.styles.statusStyle("working").Render("● RUNNING"))
	case p.Enabled:
		badges = append(badges, m.styles.statusStyle("open").Render("○ ENABLED"))
	default:
		badges = append(badges, m.styles.statusStyle("closed").Render("✕ DISABLED"))
	}

	src := string(p.Source)
	if src == "" {
		src = "builtin"
	}
	badges = append(badges, m.styles.Muted.Render("src: "+src))

	if p.Model.Agent != "" {
		mStr := p.Model.Agent
		if p.Model.Tier != "" {
			mStr += " (" + p.Model.Tier + ")"
		}
		badges = append(badges, m.styles.Muted.Render("agent: "+mStr))
	}

	out.WriteString(m.trunc(inner, strings.Join(badges, m.styles.Muted.Render(" · "))) + "\n")
	out.WriteString(m.styles.Muted.Render(strings.Repeat("─", inner)) + "\n\n")
	return out.String()
}

// personaOutputLogTopSection shows the persona's own output/execution log
// right under the header, but only while its accordion is open — empty
// otherwise, since personaRecentOutputSection covers the closed-accordion
// case at the bottom of the pane instead.
func (m Model) personaOutputLogTopSection(p persona.Persona, inner int) string {
	if p.ID != m.promptPersonaOpenID {
		return ""
	}
	var out strings.Builder
	out.WriteString(m.styles.SectionHead.Render("Output / Execution Log:") + "\n")
	rawOut, hasOut := m.promptPersonaOutput[p.ID]
	switch allLines := trimAndCapLines(rawOut, 25); {
	case !hasOut:
		out.WriteString("  " + m.styles.Muted.Render("loading output…") + "\n")
	case len(allLines) == 0:
		out.WriteString("  " + m.styles.Muted.Render("no output recorded yet (press 'r' to run)") + "\n")
	default:
		for _, ln := range allLines {
			out.WriteString("  " + m.trunc(inner-4, ln) + "\n")
		}
	}
	out.WriteString("\n")
	return out.String()
}

// personaDescriptionSection renders p's Description, empty when unset.
func (m Model) personaDescriptionSection(p persona.Persona, inner int) string {
	if p.Description == "" {
		return ""
	}
	return m.wrap(inner, p.Description) + "\n\n"
}

// personaPromptSection renders p's Prompt under its own header, empty when
// unset.
func (m Model) personaPromptSection(p persona.Persona, inner int) string {
	if p.Prompt == "" {
		return ""
	}
	return m.styles.SectionHead.Render("Prompt / Instructions:") + "\n" + m.wrap(inner, p.Prompt) + "\n\n"
}

// personaTriggerSection renders p's Trigger — schedule, event rules, and
// issue-type scope, or "Manual only" when it's Trigger.Manual().
func (m Model) personaTriggerSection(p persona.Persona) string {
	var out strings.Builder
	out.WriteString(m.styles.SectionHead.Render("Trigger:") + "\n")
	if p.Trigger.Manual() {
		out.WriteString("  " + m.styles.Muted.Render("Manual only (run with 'r')") + "\n")
	} else {
		if p.Trigger.Schedule != "" {
			out.WriteString("  " + m.styles.DetailLabel.Render("Schedule: ") + m.styles.HeaderStat.Render(p.Trigger.Schedule) + "\n")
		}
		if len(p.Trigger.On) > 0 {
			var rules []string
			for _, r := range p.Trigger.On {
				rules = append(rules, r.From+" → "+r.To)
			}
			out.WriteString("  " + m.styles.DetailLabel.Render("Events:   ") + m.styles.HeaderStat.Render(strings.Join(rules, ", ")) + "\n")
		}
		if len(p.Trigger.IssueTypes) > 0 {
			out.WriteString("  " + m.styles.DetailLabel.Render("Types:    ") + m.styles.Muted.Render(strings.Join(p.Trigger.IssueTypes, ", ")) + "\n")
		}
	}
	out.WriteString("\n")
	return out.String()
}

// personaSkillsSection renders p's Skills as a bullet list, empty when unset.
func (m Model) personaSkillsSection(p persona.Persona) string {
	if len(p.Skills) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString(m.styles.SectionHead.Render("Skills:") + "\n")
	for _, s := range p.Skills {
		out.WriteString("  • " + m.styles.DetailValue.Render(s) + "\n")
	}
	out.WriteString("\n")
	return out.String()
}

// personaAuthoritySection renders p's Authority: bd write scope, plus its
// allowed actions when any are declared.
func (m Model) personaAuthoritySection(p persona.Persona) string {
	var out strings.Builder
	out.WriteString(m.styles.SectionHead.Render("Authority:") + "\n")
	bdWrite := "isolated (read-only)"
	if p.Authority.BDWrite {
		bdWrite = "allowed (read/write)"
	}
	out.WriteString("  " + m.styles.DetailLabel.Render("BD Write: ") + m.styles.DetailValue.Render(bdWrite) + "\n")
	if len(p.Authority.Actions) > 0 {
		out.WriteString("  " + m.styles.DetailLabel.Render("Actions:  ") + m.styles.DetailValue.Render(strings.Join(p.Authority.Actions, ", ")) + "\n")
	}
	out.WriteString("\n")
	return out.String()
}

// personaRecentOutputSection shows the persona's most recent output at the
// bottom of the pane, but only while its accordion is closed (open shows it
// at the top instead, via personaOutputLogTopSection) and it actually has
// output recorded — empty otherwise.
func (m Model) personaRecentOutputSection(p persona.Persona, inner int) string {
	if p.ID == m.promptPersonaOpenID {
		return ""
	}
	rawOut, hasOut := m.promptPersonaOutput[p.ID]
	if !hasOut || strings.TrimSpace(rawOut) == "" {
		return ""
	}
	allLines := trimAndCapLines(rawOut, 15)
	if len(allLines) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString(m.styles.SectionHead.Render("Recent Output / Log:") + "\n")
	for _, ln := range allLines {
		out.WriteString("  " + m.trunc(inner-4, ln) + "\n")
	}
	out.WriteString("\n")
	return out.String()
}

// promptBeadDetailChromeRows is viewPromptBeadDetail's own header height
// above the scrollable Overview body: title(1) + type/BRN/status badge(1)
// + separator(1) — same shape v6's thread pane used.
const promptBeadDetailChromeRows = 3

// viewPromptBeadDetail renders the selected bead's detail: a light title/
// badge header (mirroring viewSplitRight's, not calling it — Board Mode's
// own header carries tabs/dates this pane deliberately omits), then
// detailContent(w)'s exact Overview rendering — reused verbatim via
// syncPromptBeadVP's value-copy trick, not reimplemented, so any future
// change to what Overview shows (tags, blockers, description, comments)
// applies here automatically. No Terminal/Diff/Audit tabs, by design (the
// redesign's explicit ask: Overview only).
func (m Model) viewPromptBeadDetail(w int) string {
	if m.detail.BRN == "" {
		return m.styles.EmptyState.Render("select a bead to read it")
	}
	inner := max(10, w-4)
	var out strings.Builder
	out.WriteString(m.styles.Accent.Render("▎") + m.styles.ScreenTitle.Render(" "+m.truncWords(inner, m.detail.Title)) + "\n")
	eff := effectiveStatus(m.detail)
	line := m.styles.typeStyle(string(m.detail.IssueType)).Render(typeGlyph(string(m.detail.IssueType)) + " " + string(m.detail.IssueType))
	line += m.styles.Muted.Render(" · ") + m.styles.CardL1.Render(string(m.detail.BRN))
	line += m.styles.Muted.Render(" · ") + m.styles.statusStyle(string(eff)).Render(statusGlyph(string(eff))+" "+string(eff))
	out.WriteString(m.trunc(inner, line) + "\n")
	out.WriteString(m.styles.Muted.Render(strings.Repeat("─", inner)) + "\n")
	m.syncPromptBeadVP()
	out.WriteString(m.detailVP.View())
	return out.String()
}

// syncPromptBeadVP refreshes m.detailVP's dimensions/content from
// detailContent(w) — a value-copy of m with .detailTab forced to 0, so
// this reuses Board Mode's exact Overview rendering (tags, blockers,
// description, acceptance, comments) without calling into or mutating any
// Board Mode code. Safe to share detailVP with Board Mode's own
// Overview/Audit tabs and v6's old thread pane did — the screens are never
// visible at once (see boardDetailVisible).
func (m *Model) syncPromptBeadVP() {
	_, right := promptPaneWidths(m.width)
	inner := max(10, right-4)
	mm := *m
	mm.detailTab = 0
	lines := strings.Split(mm.detailContent(inner), "\n")
	rows := 1 << 30
	if m.height > 0 {
		rows = max(1, m.promptRightBottomRows()-promptBeadDetailChromeRows)
	}
	vw, vh := vpSize(inner, rows, m.height <= 0, lines)
	m.detailVP.SetWidth(vw)
	m.detailVP.SetHeight(vh)
	m.detailVP.SetContentLines(lines)
}

// promptWorklistLine renders one worklist row: the bead's title (via
// findBeadByBRN — the worklist only ever stores a BRN, the same shared
// bead cache Board Mode's own list rows resolve display text from, never a
// second copy of it), why the persona touched it, and how long ago. A
// target-less event (a cron sweep with no single subject bead) has nowhere
// for a cursor move to load, so it shows "(sweep)" in place of a title.
func (m Model) promptWorklistLine(e store.AuditEvent) string {
	brn := e.Target
	if brn == "" {
		return fmt.Sprintf("  %-12s %s — %s", "(sweep)", e.Detail, timeAgo(e.Time))
	}
	title := brn
	if bead, ok := m.findBeadByBRN(domain.BRN(brn)); ok && bead.Title != "" {
		title = bead.Title
	}
	return fmt.Sprintf("  %-12s %s %s — %s", brn, title, e.Detail, timeAgo(e.Time))
}

// timeAgo renders a coarse relative time ("just now", "5m ago", "3h ago",
// "2d ago") for the activity feed — the feed never needs finer than that,
// so this stays a small local helper instead of a full dependency.
func timeAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// quickPromptTargetName names where the quick-prompt box's text will
// actually go — the agent-CLI tab currently active in Prompt Mode's left
// pane, or a plain hint when there isn't one (no active agent CLI yet
// fetched, e.g. Prompt Mode has never been opened this session).
func (m Model) quickPromptTargetName() string {
	if id := m.activePromptAgentID(); id != "" {
		return id
	}
	return "(no active agent)"
}

// viewQuickPrompt renders the one-line box that replaces the footer while
// m.quickPromptMode is active — "claude ▸ brn-42 ▸ …", Board Mode's own
// lowercase-'p' quick prompt (see updateQuickPromptKey).
// quickPromptPrefixWidth returns the visible cell width of
// viewQuickPrompt's label — everything before the input box itself — used
// to size quickPromptInput so typing scrolls within the terminal width
// instead of running the line off the right edge.
func (m Model) quickPromptPrefixWidth() int {
	return xansi.StringWidth(m.quickPromptTargetName() + " ▸ " + m.quickPromptBRN + " ▸ ")
}

func (m Model) viewQuickPrompt() string {
	var b strings.Builder
	b.WriteString(m.styles.CmdPrompt.Render(m.quickPromptTargetName() + " ▸ "))
	b.WriteString(m.styles.CmdPrompt.Render(m.quickPromptBRN + " ▸ "))
	b.WriteString(m.quickPromptInput.View())
	line := b.String()
	// The textinput itself has no width budget (its own internal
	// scrolling only kicks in once one is set, and the label prefix
	// length varies with the persona name/BRN/title, so there's no fixed
	// value to give it) — a long-enough instruction otherwise overflows
	// straight past the terminal's right edge instead of wrapping,
	// corrupting the row below it in a real terminal. Same trunc-as-a-
	// safety-net convention viewHeaderV2 already uses for its own
	// narrow-pane fallback.
	if m.width > 0 {
		line = m.trunc(m.width, line)
	}
	return line
}
