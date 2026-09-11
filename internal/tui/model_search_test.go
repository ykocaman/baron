package tui

import (
	"context"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/baron-cli/baron/internal/store"
)

func stripAnsi(s string) string { return ansiStripRe.ReplaceAllString(s, "") }

func TestSplitPaneSelectionLoadsDetail(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "Beta", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	if m.detail.BRN != "baron-a" {
		t.Fatalf("detail = %s after load, want the first Backlog row selected", m.detail.BRN)
	}
	next, _ = m.Update(key("j"))
	m = asModel(next)
	if m.listCursor != 1 || m.detail.BRN != "baron-b" {
		t.Fatalf("after j: listCursor=%d detail=%s, want 1/baron-b", m.listCursor, m.detail.BRN)
	}
	next, _ = m.Update(key("k"))
	m = asModel(next)
	if m.listCursor != 0 || m.detail.BRN != "baron-a" {
		t.Fatalf("after k: listCursor=%d detail=%s, want 0/baron-a", m.listCursor, m.detail.BRN)
	}
	next, _ = m.Update(key("enter"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v after enter, want to stay on the dashboard", m.screen)
	}
}

func TestStatusStyleOpenDistinctFromClosed(t *testing.T) {
	// lipgloss v2 has no Renderer: styles are pure values, so colors are
	// assertable via GetForeground instead of rendered SGR strings.
	s := newStyles("dark", false)
	open := s.statusStyle("open").GetForeground()
	closed := s.statusStyle("closed").GetForeground()
	if open == closed {
		t.Fatalf("statusStyle(open) and statusStyle(closed) share a color (%v) — open and closed must not both read as gray", open)
	}
	if open != lipgloss.Color("6") {
		t.Errorf("statusStyle(open) = %v, want cyan (ANSI16 6)", open)
	}
	if closed != lipgloss.Color("8") {
		t.Errorf("statusStyle(closed) = %v, want dim gray (ANSI16 8)", closed)
	}
	if got := s.statusStyle("blocked").GetForeground(); got != lipgloss.Color("1") {
		t.Errorf("statusStyle(blocked) = %v, want red (ANSI16 1)", got)
	}
}

func TestNextSelectableClampsAtBounds(t *testing.T) {
	rows := []splitRow{
		{bead: store.Bead{BRN: "baron-a"}},
		{bead: store.Bead{BRN: "baron-b"}},
	}
	if got := nextSelectable(rows, 1, 1); got != 1 {
		t.Fatalf("j past the last bead = %d, want clamped to the last bead row (1)", got)
	}
	if got := nextSelectable(rows, 0, -1); got != 0 {
		t.Fatalf("k past the first bead = %d, want clamped to 0", got)
	}
	if got := nextSelectable(rows, -1, 1); got != 0 {
		t.Fatalf("g sentinel = %d, want 0 (first bead)", got)
	}
	if got := nextSelectable(rows, len(rows), -1); got != 1 {
		t.Fatalf("G sentinel = %d, want 1 (last bead)", got)
	}
}

// TestTruncIsAnsiAware: m.trunc (charmbracelet/x/ansi.Truncate under the
// hood) must not let escape bytes eat the truncation budget, and must keep
// the surviving text styled.
func TestTruncIsAnsiAware(t *testing.T) {
	m := Model{}
	got := m.trunc(5, "\x1b[33mmedium\x1b[39m")
	if stripAnsi(got) != "medi…" {
		t.Fatalf("trunc colored = %q (visible %q), want medi… — escape bytes must not eat the truncation budget", got, stripAnsi(got))
	}
	if !strings.HasPrefix(got, "\x1b[33m") || !strings.Contains(got, "medi") {
		t.Fatalf("trunc dropped the styling escape: %q", got)
	}
	if got := m.trunc(8, "medium"); got != "medium" {
		t.Fatalf("trunc short plain = %q, want unchanged", got)
	}
	if got := m.trunc(9, "red medium"); stripAnsi(got) != "red medi…" {
		t.Fatalf("trunc plain = %q, want red medi… (fills the width budget, ellipsis included)", got)
	}
}

// TestReapplySelectionRestoresReverseAfterInnerReset: lipgloss v2 resets
// with a bare "\x1b[m" (not "\x1b[0m" — an earlier version of this fix
// matched the wrong string and never fired), so a colored inner segment
// (a type glyph, say) would silently drop the selection's reverse-video
// for the rest of the row. reapplySelection must re-inject the selection
// style after every inner reset except the outer/final one.
func TestReapplySelectionRestoresReverseAfterInnerReset(t *testing.T) {
	amber := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	sel := lipgloss.NewStyle().Bold(true).Reverse(true)
	seq := styleStartSeq(sel)
	if seq == "" {
		t.Fatal("styleStartSeq(bold+reverse) = \"\", want a non-empty SGR sequence")
	}

	inner := amber.Render("⬢") + " title"
	line := sel.Render(inner + " pad")
	got := reapplySelection(line, seq)

	// Every reset up to the last one must be immediately followed by seq,
	// so the reverse-video never lapses mid-row.
	const reset = "\x1b[m"
	innerResets := strings.Count(got, reset) - 1
	if innerResets < 1 {
		t.Fatalf("reapplySelection(%q) = %q, want at least one inner reset to patch", line, got)
	}
	if got2 := strings.ReplaceAll(got, reset+seq, ""); strings.Count(got2, reset) != 1 {
		t.Fatalf("reapplySelection(%q) = %q, want every inner reset immediately followed by %q", line, got, seq)
	}
	if !strings.HasSuffix(got, reset) {
		t.Fatalf("reapplySelection(%q) = %q, want the outer/final reset left untouched at the end", line, got)
	}

	// "" means CardSelected carries no styling (NO_COLOR) — must be a no-op.
	if got := reapplySelection(line, ""); got != line {
		t.Fatalf("reapplySelection with empty seq = %q, want the line unchanged", got)
	}
}

func TestWrapHardBreaksLongWords(t *testing.T) {
	m := New(context.Background(), testDeps())
	got := m.wrap(30, "short words here and averylongunbrokenword123456 end")
	for i, ln := range strings.Split(got, "\n") {
		if w := lipgloss.Width(ln); w > 30 {
			t.Fatalf("wrap line %d overflows %d cols: %d cells: %q", i, 30, w, ln)
		}
	}
	joined := strings.Join(strings.Fields(got), " ")
	if joined != "short words here and averylongunbrokenword123456 end" {
		t.Fatalf("wrap lost words: %q", got)
	}
	if got := m.wrap(30, "para one\n\npara two"); got != "para one\n\npara two" {
		t.Fatalf("wrap paragraphs = %q, want blank line preserved", got)
	}
}

func TestSelectedTreeRowFitsBoxWidth(t *testing.T) {
	// regression: the selected row pads to the box inner width minus the
	// cursor bar and depth indent — an over-wide row makes the terminal
	// wrap it, shifting the tree and leaving a wrapped fragment behind.
	beads := []store.Bead{
		{ID: "ep", BRN: "baron-ep", Title: "Epic work", IssueType: "epic", Status: store.BeadStatusOpen},
		{ID: "c1", BRN: "baron-ep.1", Title: "Child task", IssueType: "task", Status: store.BeadStatusOpen, Parent: "baron-ep"},
	}
	m := openDetail(t, beads)
	m.width, m.height = 100, 30
	next, _ := m.Update(key("j"))
	m = asModel(next)
	for ln := range strings.SplitSeq(m.View().Content, "\n") {
		if !strings.Contains(ln, "▌") || !strings.Contains(ln, "Child task") {
			continue
		}
		// This is the full terminal row (left box + right box joined side
		// by side), so the bound is the terminal width itself, not a
		// single box's inner width — every other line in the view (header,
		// borders, footer) is exactly m.width wide too.
		if w := lipgloss.Width(ln); w > m.width {
			t.Fatalf("selected tree row %d cells wide, terminal is %d: %q", w, m.width, ln)
		}
		return
	}
	t.Fatal("selected child row not found in view")
}

func TestSectionHeadUsesAccentText(t *testing.T) {
	// lipgloss v2: Style is a plain value, Render emits SGR directly.
	s := newStyles("dark", false)
	got := s.SectionHead.Render("▎ Gates & checks")
	if !strings.Contains(got, ";36m") {
		t.Fatalf("section head %q has no accent (cyan) foreground", got)
	}
	if strings.Contains(got, "[48;") || strings.Contains(got, "46m") {
		t.Fatalf("section head %q still has a background SGR", got)
	}
}

func TestDashboardCommentStatusCancel(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, _ = m.Update(key("c"))
	m = asModel(next)
	if m.screen != screenForm || m.formKind != formKindComment {
		t.Fatalf("after c: screen=%d formKind=%d, want comment form", m.screen, m.formKind)
	}
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("esc on the comment form should return to the dashboard, screen=%d", m.screen)
	}

	next, _ = m.Update(key("s"))
	m = asModel(next)
	if m.statusTargets == nil {
		t.Fatal("statusTargets = nil after s, want the status menu")
	}
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.statusTargets != nil {
		t.Fatal("statusTargets not cleared after esc")
	}

	next, _ = m.Update(key("x"))
	m = asModel(next)
	if m.confirming != "" {
		t.Fatalf("confirming = %q after x on an open bead, want no prompt (open can't reach human_queue)", m.confirming)
	}
}

func TestSplitPaneAgentTab(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width = 120
	m.height = 36

	// Agent is the second tab (Overview → Agent).
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)
	if m.detailTab != 1 {
		t.Fatalf("detailTab = %d, want 1 (Agent)", m.detailTab)
	}
	view := m.View().Content
	if !strings.Contains(view, "no live output for baron-a") {
		t.Errorf("View() = %q, want the agent empty state", view)
	}
}

func TestDashboardSearchFilter(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "Beta", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, _ = m.Update(key("/"))
	m = asModel(next)
	if !m.searchMode {
		t.Fatal("searchMode = false, want true after /")
	}
	for _, r := range "Beta" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	view := m.View().Content
	if !strings.Contains(view, "Beta") {
		t.Errorf("View() = %q, want the matching bead visible", view)
	}
	if strings.Contains(view, "Alpha") {
		t.Errorf("View() = %q, want the non-matching bead filtered out", view)
	}

	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.searchMode || m.searchQuery != "" {
		t.Fatalf("searchMode = %v, searchQuery = %q, want cleared after esc", m.searchMode, m.searchQuery)
	}
	if !strings.Contains(m.View().Content, "Alpha") {
		t.Errorf("View() = %q, want all beads back after clearing the search", m.View().Content)
	}
}

func TestSearchRegexAndCrossTab(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-123", Title: "Login auth bug", Status: store.BeadStatusOpen},      // Backlog tab
		{BRN: "baron-456", Title: "Payment flow", Status: store.BeadStatusWorking},     // Active tab
		{BRN: "baron-789", Title: "Login UI redesign", Status: store.BeadStatusMerged}, // Done tab
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	// Press '/' to enter search mode
	next, _ = m.Update(key("/"))
	m = asModel(next)
	if !m.searchMode {
		t.Fatal("searchMode = false, want true after /")
	}

	// Type regex "Login.*"
	for _, r := range "Login.*" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	view := m.View().Content

	// Should match baron-123 (Backlog) and baron-789 (Done) across tabs
	if !strings.Contains(view, "Login auth bug") {
		t.Errorf("View() = %q, want 'Login auth bug' visible in cross-tab search", view)
	}
	if !strings.Contains(view, "Login UI redesign") {
		t.Errorf("View() = %q, want 'Login UI redesign' visible in cross-tab search", view)
	}
	if strings.Contains(view, "Payment flow") {
		t.Errorf("View() = %q, want 'Payment flow' filtered out", view)
	}

	// Tab counts should reflect matches per tab: Backlog (1), Active (0), Needs You (0), Done (1)
	if !strings.Contains(view, "Backlog (1)") {
		t.Errorf("View() = %q, want 'Backlog (1)' in tab header", view)
	}
	if !strings.Contains(view, "Active (0)") {
		t.Errorf("View() = %q, want 'Active (0)' in tab header", view)
	}
	if !strings.Contains(view, "Done (1)") {
		t.Errorf("View() = %q, want 'Done (1)' in tab header", view)
	}

	// Escape cancels and resets search
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.searchMode || m.searchQuery != "" || m.searchRegex != nil {
		t.Fatalf("search state not cleared: mode=%v query=%q regex=%v", m.searchMode, m.searchQuery, m.searchRegex)
	}
}

// TestSearchFilterUpdatesSubTabCounts: the Active tab's sub-tab pills
// (Working/Validating/Retry) must narrow along with the tab bar's own counts
// while a search is active. viewSubTabBar used to call bucketRows directly,
// ignoring m.searchQuery entirely, so a sub-tab pill like "Retry" kept
// showing the unfiltered count even as every other count on screen changed.
func TestSearchFilterUpdatesSubTabCounts(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-1", Title: "flaky login retry", Status: store.BeadStatusRetry},
		{BRN: "baron-2", Title: "unrelated payment retry", Status: store.BeadStatusRetry},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	if m.listTab != 1 {
		t.Fatalf("listTab = %d, want 1 (Active) as the default", m.listTab)
	}

	// Unfiltered: both Retry beads count.
	view := m.View().Content
	if !strings.Contains(view, "Retry 2") {
		t.Errorf("View() = %q, want 'Retry 2' before search", view)
	}

	// Filter down to just the "login" bead.
	next, _ = m.Update(key("/"))
	m = asModel(next)
	for _, r := range "login" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	view = m.View().Content
	if !strings.Contains(view, "Retry 1") {
		t.Errorf("View() = %q, want 'Retry 1' once search narrows to one match", view)
	}
	if strings.Contains(view, "Retry 2") {
		t.Errorf("View() = %q, want stale 'Retry 2' gone once search is active", view)
	}
}

// TestHumanQueueIsATabNotAScreen: eliminates the dedicated human queue
// screen — split-pane's "Needs You" list tab (index 2) is the same
// underlying data, and the same per-row actions (x close, a assign, R run)
// already work there, so there is no separate screen or key (old: H) to
// reach it.
func TestHumanQueueIsATabNotAScreen(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusHumanQueue},
		{BRN: "baron-c", Title: "C", Status: store.BeadStatusHumanQueue},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, _ = m.Update(key("3")) // jump straight to the Needs You tab
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want screenDashboard (human queue is a tab)", m.screen)
	}
	if m.listTab != 2 {
		t.Fatalf("listTab = %d, want 2 (Needs You)", m.listTab)
	}
	rows := m.splitRows(m.listTab)
	if len(rows) != 2 {
		t.Fatalf("Needs You tab rows = %d, want 2 (baron-b, baron-c)", len(rows))
	}

	next, _ = m.Update(key("j"))
	m = asModel(next)
	next, _ = m.Update(key("x"))
	m = asModel(next)
	if m.confirming != "" {
		t.Fatalf("confirming = %q, want no prompt — the bead is already in human_queue", m.confirming)
	}
}

func TestLiveAgentAttachIsNoOp(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}})
	next, cmd := m.Update(key("i"))
	m = asModel(next)
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil (i was removed — run now watches live in the right pane)", cmd)
	}
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want screenDashboard", m.screen)
	}
}

func TestEmptyStates(t *testing.T) {
	m := New(context.Background(), testDeps())
	view := m.View().Content
	if !strings.Contains(view, "nothing here") {
		t.Errorf("View() = %q, want an empty-board state", view)
	}

	m.listTab = 2 // Needs You tab
	if !strings.Contains(m.View().Content, "nothing here") {
		t.Errorf("View() = %q, want an empty Needs You tab state", m.View().Content)
	}

	m = openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}})
	if !strings.Contains(m.View().Content, "no comments") {
		t.Errorf("View() = %q, want the empty comments state", m.View().Content)
	}
}

// TestFooterCappedAtWidth: the footer must never exceed the pane width, even
// when all hints cannot fit. Fails today: left hints + ":: command" overflow.
func TestFooterCappedAtWidth(t *testing.T) {
	m := New(context.Background(), testDeps())
	m.width = 50 // narrow pane: not every hint fits
	for i, ln := range strings.Split(m.View().Content, "\n") {
		// lipgloss.Width is ANSI-aware: escape sequences are zero-width on a
		// real terminal, but runewidth.StringWidth counts their bytes.
		if w := lipgloss.Width(ln); w > m.width {
			t.Errorf("line %d overflows %d cols: %d cells: %q", i, m.width, w, ln)
		}
	}
}

// TestHelpEscReturnsToPrevScreen: esc on the help screen must close help
// back to the screen it was opened from, not always the dashboard.
func TestHelpEscReturnsToPrevScreen(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key("P")) // Prompt Mode — any non-dashboard screen works
	m = asModel(next)
	if m.screen != screenCrew {
		t.Fatalf("screen = %v, want screenCrew after P", m.screen)
	}
	next, _ = m.Update(key("?")) // help
	m = asModel(next)
	if m.screen != screenHelp {
		t.Fatalf("screen = %v, want screenHelp after ?", m.screen)
	}
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.screen != screenCrew {
		t.Fatalf("screen = %v after esc on help, want back to screenCrew (prevScreen)", m.screen)
	}
}
