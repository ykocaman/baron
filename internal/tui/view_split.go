package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// viewSplitPane renders the split-pane dashboard: the bead tree on the left
// and the selected bead's detail on the right. Both panes always draw: the
// right pane's Agent tab is the embedded terminal, rendered by BARON itself.
func (m Model) viewSplitPane() string {
	if m.width <= 0 {
		return m.viewSplitLeft(0) + " " + m.viewSplitRight(0)
	}
	leftW, rightW := splitPaneWidths(m.width)
	return lipgloss.JoinHorizontal(lipgloss.Top, m.viewSplitLeft(leftW), " ", m.viewSplitRight(rightW))
}

// splitPaneWidths divides the dashboard's total width between the left list
// and right detail pane: left ≈38% — it only needs to show a tab bar and
// short bead rows, so the detail pane (comments, diffs, live agent output)
// gets the room it actually needs. On narrow terminals the minimums give
// way (left shrinks first), and left + right + 1 (the join gap) never
// exceeds the terminal width.
func splitPaneWidths(total int) (left, right int) {
	left = max(total*38/100, 24)
	right = max(total-left-1, 20)
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

// viewSubTabBar renders the active tab's status buckets as a row of
// pills — one per entry in boardColumns[m.listTab].statuses — so the
// sub-tab names are always visible rather than only surfacing on
// selection: it's both the legend for left/right sub-tab navigation and the
// cue that buckets exist at all. There's no "All" pill: the unfiltered
// total already sits in the tab bar above. Each count comes from
// renderSubTabBar renders the status sub-tab bar for a given column and sub-tab index.
func (m Model) renderSubTabBar(colIdx, subTabIdx, inner int) string {
	if colIdx < 0 || colIdx >= len(boardColumns) {
		return ""
	}
	statuses := boardColumns[colIdx].statuses
	var bar, plain strings.Builder
	for i, st := range statuses {
		if i > 0 {
			bar.WriteString("  ")
			plain.WriteString("  ")
		}
		label := fmt.Sprintf("%s %d", bucketLabel(st), m.searchCountForBucket(colIdx, i))
		if subTabIdx == i {
			bar.WriteString(m.styles.TabActive.Render(label))
		} else {
			bar.WriteString(m.styles.TabInactive.Render(label))
		}
		plain.WriteString(label)
	}
	out := "  " + bar.String()
	if plainOut := "  " + plain.String(); xansi.StringWidth(plainOut) > inner {
		out = m.trunc(inner, plainOut)
	}
	return out
}

func (m Model) viewSubTabBar(inner int) string {
	return m.renderSubTabBar(m.listTab, m.listSubTab, inner)
}

// viewSplitLeft renders the left pane: tab bar + windowed list rows.
// splitBoxInnerRows is how many rows sit inside each split-pane box, borders
// excluded. Both boxes are pinned to it so the dashboard's geometry is a
// function of the terminal size alone.
//
// Letting a box size itself to its content is what made the layout jitter:
// the left list padded itself to a fixed height while the right pane trimmed
// its own padding away, so every change in detail content — switching tabs,
// scrolling into a scrollback view that adds a marker row, an agent printing
// a shorter frame — moved the right box's bottom edge, and with it the footer
// and shortcut row. Those belong on the terminal's last line, always.
func (m Model) splitBoxInnerRows() int {
	// viewString lays the screen out as header(1) + body + footer(1), so the
	// body's budget is height-2 and a box's own two border rows come out of
	// that: height-4 rows inside.
	return max(1, m.height-4)
}

// fixedBox renders content in the split-pane box style at exactly
// splitBoxInnerRows rows — padded when short, clipped when long — so the
// box's outer height never depends on what is inside it. A thin wrapper
// over fixedBoxN for the common case (a box claiming the whole column);
// see fixedBoxN for the general form and the focused param.
func (m Model) fixedBox(w int, content string, focused ...bool) string {
	return m.fixedBoxN(w, m.splitBoxInnerRows(), content, focused...)
}

// fixedBoxN is fixedBox generalized over an explicit row count, for panes
// that don't claim the whole column budget — Prompt Mode's two stacked
// right-column boxes (viewPromptRightTop/viewPromptRightBottom) each need
// their own row count so one can be a fixed height (promptRightTopRows)
// while the other takes the remainder (promptRightBottomRows), rather than
// both independently claiming the full splitBoxInnerRows and overflowing
// the column between them.
//
// focused, if given (only its first element is used — a variadic optional
// param, same convention as NewGateRunner's failFast), draws the box's
// border in the accent color instead of the default dim one. Board Mode's
// own two panes never pass it (both always "active" at once — arrows and
// shift+arrows each own a different pane simultaneously, no ambiguity to
// resolve visually); Prompt Mode's three panes do, since j/k, enter, and
// 'e' mean different things depending on which one has focus (m.promptFocus)
// — the border is how a player tells which pane their next keystroke
// reaches.
func (m Model) fixedBoxN(w, rows int, content string, focused ...bool) string {
	style := m.styles.ListBox
	if len(focused) > 0 && focused[0] {
		style = style.BorderForeground(m.styles.color(ansiAccent))
	}
	if w > 0 {
		style = style.Width(w)
	}
	if m.height <= 0 {
		// Unsized (tests): no budget to pin to, so render as-is.
		return style.Render(content)
	}
	// The line count is fixed here rather than left to lipgloss's Height /
	// MaxHeight, which measure the bordered block and did not agree with each
	// other for the two boxes: with identical calls the left came out 26 rows
	// and the right 24. Padding and clipping the content directly is exact.
	//
	// Each line is also fitted to the box's inner width, because a line wider
	// than the box wraps into two and silently costs a row — one line too
	// long anywhere and the box grows, taking the footer with it. The
	// scrollback marker did exactly that on a narrow pane.
	innerW := max(1, w-4) // 2 border columns + 2 padding columns
	lines := strings.Split(content, "\n")
	for len(lines) < rows {
		lines = append(lines, "")
	}
	lines = lines[:rows]
	if w > 0 {
		for i, ln := range lines {
			lines[i] = m.trunc(innerW, ln)
		}
	}
	return style.Render(strings.Join(lines, "\n"))
}

// renderBoardTabBar renders the top tab bar (Backlog, Active, Needs You, Done)
// with counts and active indicator, shared by both Board Mode and Prompt Mode Beads tab.
func (m Model) renderBoardTabBar(activeTab int, inner int) string {
	var bar strings.Builder
	var plain strings.Builder
	for i, col := range boardColumns {
		count := m.searchCountForTab(i)
		var label string
		if m.searchQuery != "" {
			label = fmt.Sprintf("%s (%d)", col.name, count)
		} else {
			label = fmt.Sprintf("%s %d", col.name, len(m.bucketRows(i, -1)))
		}
		if i == activeTab {
			bar.WriteString(m.styles.CardSelBar.Render("▌ "))
			bar.WriteString(m.styles.TabActive.Render(label))
		} else {
			bar.WriteString("  " + m.styles.TabInactive.Render(label))
		}
		plain.WriteString(label)
		if i < len(boardColumns)-1 {
			bar.WriteString(" ")
			plain.WriteString("  ")
		}
	}
	tabBar := bar.String()
	if xansi.StringWidth(strings.TrimRight(plain.String(), " ")) > inner {
		tabBar = m.trunc(inner, strings.TrimRight(plain.String(), " "))
	}
	return tabBar
}

func (m Model) viewSplitLeft(w int) string {
	inner := max(0, w-4)
	tabBar := m.renderBoardTabBar(m.listTab, inner)
	subTabBar := m.viewSubTabBar(inner)
	// currentRows already switches to searchAllRows while a search is
	// active (matches across ALL tabs, so the user sees the full result
	// set at once — the tab bar counts above tell them which tab to
	// navigate to afterwards) — the same source m.listCursor indexes into,
	// which is exactly the property that matters here: what's drawn
	// highlighted must be what a keypress actually acts on.
	rows := m.currentRows()
	avail := m.splitPaneRows()
	start := min(m.listScroll, max(0, len(rows)-avail))
	lines := []string{tabBar, subTabBar}
	if len(rows) == 0 {
		lines = append(lines, m.styles.EmptyState.Render("— nothing here"))
	} else {
		for i := start; i < min(start+avail, len(rows)); i++ {
			r := rows[i]
			// No inline per-status section header here: the sub-tab bar
			// above already names every bucket with its count, so
			// repeating the bucket name inline before its first row was
			// pure duplication and made the list feel busier than it is.
			prefix := 2 + 2*r.depth
			line := m.splitRowLine(r, w, prefix)
			if i == m.listCursor {
				// The cursor bar replaces only the base indent; the depth
				// padding stays so children keep their column on hover.
				// Pad to the box inner width minus the cursor bar ("▌ ", 2
				// cells) and depth indent: an over-wide row makes the
				// terminal wrap it, shifting the tree and leaving a wrapped
				// fragment behind.
				pad := max(0, w-4-2-2*r.depth-lipgloss.Width(line))
				line = m.styles.ListCursor.Render("▌ ") + strings.Repeat(" ", 2*r.depth) + m.styles.CardSelected.Render(line+strings.Repeat(" ", pad))
				line = reapplySelection(line, m.styles.reselectSeq)
			} else {
				line = strings.Repeat(" ", prefix) + line
			}
			lines = append(lines, line)
		}
	}
	if m.height > 0 {
		// lines starts with two header rows (tabBar, subTabBar), so the
		// pad target is avail+2, not avail+1 — short by one left the
		// footer floating a row above the terminal's actual last line
		// whenever the list didn't fully fill avail (the common case).
		for len(lines) < avail+2 {
			lines = append(lines, "")
		}
	}
	return m.fixedBox(w, strings.Join(lines, "\n"))
}

// reapplySelection re-injects the selection style's start sequence (seq —
// see styles.reselectSeq) after every inner reset in an already-rendered
// selected line, except the outer/final one. A colored inner segment (a
// type glyph, say) ends with its own reset, which clears the selection's
// reverse-video for the rest of the row unless it is turned back on right
// after; the outer reset at the very end is left alone so the row properly
// stops being reversed there. seq == "" (NO_COLOR: CardSelected carries no
// styling at all) makes this a no-op.
func reapplySelection(line, seq string) string {
	if seq == "" {
		return line
	}
	const reset = "\x1b[m"
	i := strings.LastIndex(line, reset)
	if i <= 0 {
		return line
	}
	return strings.ReplaceAll(line[:i], reset, reset+seq) + line[i:]
}

// splitRowLine renders one list row: epic headers bold, children indented
// with a tree connector, flats plain. Rows carry no BRN. The prefix (cursor
// bar or indent) is budgeted out of the truncation so the composed row never
// exceeds the box's inner width — an over-wide row makes lipgloss wrap it,
// inflating the box height and pushing the header off screen.
func (m Model) splitRowLine(r splitRow, w, prefix int) string {
	inner := max(0, w-4-prefix)
	if r.epic {
		return m.styles.typeStyle("epic").Render(typeGlyph("epic")) + " " + m.styles.DetailHead.Render(m.trunc(inner-2, r.bead.Title))
	}
	line := statusGlyph(string(effectiveStatus(r.bead))) + " " + m.styles.typeStyle(string(r.bead.IssueType)).Render(typeGlyph(string(r.bead.IssueType))) + " " + r.bead.Title
	if r.depth > 0 {
		line = "└ " + line
	}
	if len(m.activeBlockers(r.bead)) > 0 {
		// ⛔ used to be here — same tofu-box problem as statusGlyph's old
		// glyphs (see its doc comment); ■ is from the same confirmed-safe set.
		line = m.trunc(max(0, inner-3), line) + " ■"
	}
	return m.trunc(inner, line)
}

// viewSplitRight renders the right pane: title, status line, tab bar and the
// active tab's content, bordered like the left pane so focus is visible on
// both sides.
func (m Model) viewSplitRight(w int) string {
	b, ok := m.splitDetailBead()
	if !ok {
		return m.fixedBox(w, m.styles.EmptyState.Render("select a bead to inspect it"))
	}
	titleW := max(8, w-4)
	inner := max(10, w-4)

	var out strings.Builder
	out.WriteString(m.styles.Accent.Render("▎"))
	out.WriteString(m.styles.ScreenTitle.Render(" " + m.truncWords(titleW, b.Title)))
	out.WriteString("\n")
	out.WriteString(m.trunc(inner, m.splitBadgeLine(b)))
	out.WriteString("\n")
	out.WriteString(m.splitTabsWithDates(inner, splitDetailDates(b)))
	out.WriteString("\n")
	out.WriteString(m.styles.Muted.Render(strings.Repeat("─", inner)))
	out.WriteString("\n")
	out.WriteString(m.viewSplitDetailPane(w))
	// No manual padding here: this used to append blank lines and then strip
	// them right back off with TrimRight, so the box silently sized itself to
	// its content. fixedBox pins the height instead.
	return m.fixedBox(w, out.String())
}

// splitDetailBead resolves the bead viewSplitRight describes: m.detail, or
// (when that's empty) the live-attached bead via m.liveBRN. ok is false
// when neither resolves to a real bead.
func (m Model) splitDetailBead() (store.Bead, bool) {
	b := m.detail
	if b.BRN == "" && m.liveBRN != "" {
		if bead, ok := m.findBeadByBRN(domain.BRN(m.liveBRN)); ok {
			b = bead
		}
	}
	return b, b.BRN != ""
}

// splitBadgeLine renders b's type/BRN/status badge line, plus its
// priority/tier/assignee/model/effort extras.
func (m Model) splitBadgeLine(b store.Bead) string {
	line := ""
	if b.IssueType != "" {
		line = m.styles.typeStyle(string(b.IssueType)).Render(typeGlyph(string(b.IssueType)) + " " + string(b.IssueType))
	}
	if b.BRN != "" {
		if line != "" {
			line += m.styles.Muted.Render(" · ")
		}
		line += m.styles.CardL1.Render(string(b.BRN))
	}
	eff := effectiveStatus(b) // derived state: bd stores no literal "assigned"
	line += m.styles.Muted.Render(" · ") + m.styles.statusStyle(string(eff)).Render(statusGlyph(string(eff))+" "+string(eff))
	var extra []string
	if b.Priority != "" {
		extra = append(extra, m.styles.priorityStyle(b.Priority).Render(string(b.Priority)))
	}
	// Tier (b.Tier(), the mandatory capability class picked in the new-bead
	// form — see newBeadFormTierOptions) never rendered anywhere after
	// creation: this header line has priority/assignee/model/effort but no
	// tier, so there was no way to see what a bead was even set to short of
	// `bd show`/`:log`. b.Tier() always returns a real tier — a bead with
	// no/invalid tier metadata defaults to agent.TierFast (see its doc
	// comment) — so this always has something to show, never an empty
	// state. "≡" (freeze-verified safe, unused by any status/type glyph —
	// see the visual-debugging memory) marks it as a tier badge rather than
	// a bare word floating among priority/assignee/model/effort with
	// nothing distinguishing which is which; a "tier:" text label was
	// tried first and dropped in favor of this.
	extra = append(extra, m.styles.Info.Render("≡ "+string(b.Tier())))
	if b.Assignee != "" {
		extra = append(extra, m.styles.Info.Render(b.Assignee))
	}
	if mdl := b.Model(); mdl != "" {
		// Display-only trim of a provider-namespaced ID's "provider/" prefix
		// (e.g. "relayhaus/sonnet" -> "sonnet") — never the routing decision,
		// which always comes from b.Assignee (rendered just above, from the
		// catalog's own Agent field, see agent.Model's doc comment). Trimmed
		// here purely so this line doesn't show the same CLI-ish-looking
		// prefix twice next to Assignee.
		if i := strings.LastIndex(mdl, "/"); i >= 0 {
			mdl = mdl[i+1:]
		}
		extra = append(extra, m.styles.Info.Render(mdl))
	}
	if eff := b.Effort(); eff != "" {
		extra = append(extra, m.styles.Info.Render(eff))
	}
	if len(extra) > 0 {
		line += m.styles.Muted.Render(" · ") + strings.Join(extra, m.styles.Muted.Render(" · "))
	}
	return line
}

// splitDetailDates renders b's created/updated timestamps, empty for
// either that's still zero.
func splitDetailDates(b store.Bead) string {
	var dates string
	if !b.CreatedAt.IsZero() {
		dates = "created: " + b.CreatedAt.Format(dateTimeLayout)
	}
	if !b.UpdatedAt.IsZero() {
		if dates != "" {
			dates += "  "
		}
		dates += "updated: " + b.UpdatedAt.Format(dateTimeLayout)
	}
	return dates
}

// splitTabsWithDates rides dates onto the tab bar line, right-aligned; on a
// narrow pane the tabs win and the dates drop off rather than truncating
// the tab labels.
func (m Model) splitTabsWithDates(inner int, dates string) string {
	tabs := m.viewSplitTabBar()
	if dates == "" {
		return tabs
	}
	if avail := inner - 1 - xansi.StringWidth(dates); xansi.StringWidth(tabs) > avail {
		tabs = m.trunc(max(0, avail), tabs)
	}
	pad := max(1, inner-xansi.StringWidth(tabs)-xansi.StringWidth(dates))
	return tabs + strings.Repeat(" ", pad) + m.styles.Muted.Render(dates)
}

// viewSplitTabBar renders the right pane's tabs (Overview/Terminal/Audit).
// detailTabs are the right pane's tabs, indexed by m.detailTab: 0=Overview,
// 1=Terminal (the Agent tab), 2=Diff (a live `hunk diff` session — see
// currentKind/tabIndexFor in terminal.go), 3=Audit. detailTabCount is the
// single source every tab-index computation (cycling, click hit-testing)
// derives from, so adding a tab never means hunting down a stray literal.
var detailTabs = []string{"Overview", "Terminal", "Diff", "Audit"}

var detailTabCount = len(detailTabs)

func (m Model) viewSplitTabBar() string {
	var b strings.Builder
	for i, t := range detailTabs {
		if i == m.detailTab {
			b.WriteString(m.styles.TabActive.Render("[" + t + "]"))
		} else {
			b.WriteString(m.styles.TabInactive.Render(" " + t + " "))
		}
		if i < len(detailTabs)-1 {
			b.WriteString("  ")
		}
	}
	return b.String()
}

// viewSplitDetailPane windows the right pane's content; the Terminal tab (1)
// and Diff tab (2) render their embedded terminal's emulator frame via
// viewSessionPane, falling back to an empty state when the bead has no
// session.
func (m Model) viewSplitDetailPane(w int) string {
	if m.detailTab == 1 || m.detailTab == 2 {
		return m.viewSessionPane(m.currentKind(), w)
	}
	m.syncDetailVP()
	return m.detailVP.View()
}

// vpSize returns the width/height to feed a viewport for lines, given the
// pane's real budget (paneW, paneH): height is always capped to the
// content's own line count, since viewport.View() unconditionally pads (or
// truncates) its output to exactly the configured height via lipgloss —
// unlike the old manual-slice rendering, which just returned however many
// lines there were and let an outer, separate padding pass (viewSplitRight)
// handle matching the left box's height. Width only widens past paneW when
// unsized is true (paneH is the "don't clamp" sentinel — 1<<30, used
// throughout this package for unknown terminal size / tests): a real
// terminal's width should still clip an overlong line exactly like
// m.trunc/xansi.Truncate already do elsewhere, but the unsized fallback
// (splitPaneWidths(0)'s narrow 20-ish columns) must not clip content that
// was never width-constrained by the old rendering path at all.
