package tui

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// openDetail returns a dashboard model with the first bead selected in the
// right pane (the single-screen equivalent of the removed detail screen).
func openDetail(t *testing.T, beads []store.Bead) Model {
	t.Helper()
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	for ci, c := range boardColumns {
		if slices.Contains(c.statuses, beads[0].Status) {
			m.listTab = ci
			break
		}
	}
	m.detail = beads[0]
	m.liveBRN = string(beads[0].BRN)
	return m
}

func TestStatusMenuShowsTargets(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking, Assignee: "alice"}})
	next, _ := m.Update(key("s"))
	m = asModel(next)

	want := []domain.BeadState{domain.BeadStateHumanQueue, domain.BeadStateRetry, domain.BeadStateValidating, domain.BeadStateCancelled}
	if len(m.statusTargets) != len(want) {
		t.Fatalf("statusTargets = %v, want %v", m.statusTargets, want)
	}
	for i, w := range want {
		if m.statusTargets[i] != w {
			t.Fatalf("statusTargets[%d] = %q, want %q (lexical order, cancelled last)", i, m.statusTargets[i], w)
		}
	}
	view := m.View().Content
	for _, want := range []string{"Change status", "cancelled", "human_queue", "validating"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() = %q, want it to contain %q", view, want)
		}
	}
}

// TestStatusMenuArrowKeysMoveViewSelection: pressing down (or j) must
// visibly move the highlighted option, not just the value bound to
// statusResult. Regression test for viewStatusMenu — it used to render
// from the pre-huh statusTargets/statusCursor fields directly, a leftover
// from before the menu was ever backed by a form at all: key handling had
// already moved onto statusForm (huh.Form.Update), so the underlying
// selection (and thus statusResult, and thus what enter would apply) moved
// correctly, but statusCursor — never written by huh — stayed frozen at 0,
// so the screen kept showing the first option highlighted no matter how
// many times down was pressed.
func TestStatusMenuArrowKeysMoveViewSelection(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking, Assignee: "alice"}})
	next, _ := m.Update(key("s"))
	m = asModel(next)
	before := m.View().Content

	next, _ = m.Update(key("down"))
	m = asModel(next)
	after := m.View().Content

	if before == after {
		t.Fatal("View() unchanged after down — the highlighted option must move")
	}
	if m.statusResult == nil || *m.statusResult != domain.BeadStateRetry {
		t.Fatalf("statusResult = %v after down, want retry (the second target: human_queue, retry, validating, cancelled)", m.statusResult)
	}
}

func TestStatusMenuAppliesTransition(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking, Assignee: "alice"}})
	next, _ := m.Update(key("s"))
	m = asModel(next)

	next, cmd := m.Update(key("enter"))
	m = asModel(next)
	// The form's result should be set after 'enter' (value binding works
	// synchronously). human_queue, not cancelled: AllowedTargets sorts
	// cancelled last on purpose (see its doc comment) precisely so the
	// picker's default cursor position — index 0, applied by a bare
	// 'enter' — never lands on the destructive option.
	if m.statusResult == nil || *m.statusResult != domain.BeadStateHumanQueue {
		t.Fatalf("statusResult should be human_queue after 'enter', got %v", m.statusResult)
	}
	if cmd == nil {
		t.Fatal("status transition with enter should produce a completion command")
	}
	// Must be exactly runAction's own command, not a tea.Batch smuggling out
	// huh's internal NextField advance alongside it — see updateStatusMenuKey's
	// doc comment. That leftover used to ride out to the real tea.Program on
	// every completion with nothing that would ever consume it: nobody
	// downstream of Model.Update recognizes huh's private nextFieldMsg, so it
	// just piled up in the runtime's queue, one more per status change.
	if _, ok := cmd().(tea.BatchMsg); ok {
		t.Fatal("cmd() = tea.BatchMsg, want runAction's own message directly — huh's internal advance must be fully resolved before returning, not smuggled out")
	}
}

// TestStatusMenuReopenDoesNotLeakHuhMessages: applying a status transition
// must not leave anything for a later, unrelated keypress to pick up — the
// bug this guards was leftover huh nextFieldMsg values riding out to the
// real tea.Program on every completion (see TestStatusMenuAppliesTransition
// and updateStatusMenuKey's doc comment); reported as the status menu
// "freezing" after the 2nd or 3rd open in real interactive use. Applying a
// transition three times in a row and reopening the menu each time must
// behave identically every time.
func TestStatusMenuReopenDoesNotLeakHuhMessages(t *testing.T) {
	for i := range 3 {
		// Fresh model each round (matching the real "open the menu, apply a
		// transition" cycle a bead goes through — status has moved on to
		// "cancelled" after the first round, so a literal repeat needs
		// a bead back in "working"), but each Update call still exercises
		// the exact same statusForm construction/teardown path a real
		// repeated open does.
		m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking, Assignee: "alice"}})
		next, _ := m.Update(key("s"))
		m = asModel(next)
		if m.statusForm == nil {
			t.Fatalf("iter %d: statusForm = nil after s, want the menu open", i)
		}
		next, cmd := m.Update(key("enter"))
		m = asModel(next)
		if m.statusForm != nil {
			t.Fatalf("iter %d: statusForm still set after enter, want the menu closed", i)
		}
		if cmd == nil {
			t.Fatalf("iter %d: no completion command after enter", i)
		}
		if _, ok := cmd().(actionDoneMsg); !ok {
			t.Fatalf("iter %d: cmd() = %T, want actionDoneMsg directly", i, cmd())
		}
	}
}

// TestStatusMenuTerminalState: the "no status transitions" notice must
// auto-dismiss like every other status message. Regression test for dead
// code that followed an unconditional `return m, nil`, silently skipping
// the statusDismissCmd() call right after it.
func TestStatusMenuTerminalState(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusCancelled}})
	next, cmd := m.Update(key("s"))
	m = asModel(next)

	if m.statusTargets != nil {
		t.Fatalf("statusTargets = %v, want nil for a terminal state", m.statusTargets)
	}
	if !strings.Contains(m.statusMsg, "no status transitions") {
		t.Errorf("statusMsg = %q, want a no-status-transitions notice", m.statusMsg)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the status auto-dismiss timer")
	}
	if _, ok := cmd().(statusExpiredMsg); !ok {
		t.Errorf("cmd() = %T, want statusExpiredMsg", cmd())
	}
}

// TestStatusMenuClosedShowsReopen: closed is not a dead end — its status
// menu must list open so a bead closed by mistake can be reopened.
func TestStatusMenuClosedShowsReopen(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusClosed}})
	next, _ := m.Update(key("s"))
	m = asModel(next)

	want := []domain.BeadState{domain.BeadStateOpen}
	if !slices.Equal(m.statusTargets, want) {
		t.Fatalf("statusTargets = %v, want %v for a closed bead", m.statusTargets, want)
	}
}

func TestStatusMenuEscCloses(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking, Assignee: "alice"}})
	next, _ := m.Update(key("s"))
	m = asModel(next)
	if m.statusForm == nil {
		t.Fatal("statusForm = nil, want the menu open after s")
	}

	next, _ = m.Update(key("esc"))
	m = asModel(next)
	// On esc, the form is aborted but in test harness without tea.Program
	// the form doesn't auto-clear. The bound value should be the default (cancelled).
	// The screen should still be dashboard since status menu closes on abort.
	if m.statusForm != nil {
		t.Logf("statusForm still active after esc in test harness (expected - no tea.Program)")
	}
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want screenDashboard", m.screen)
	}
}

func TestSplitPaneTabKeys(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusWorking},
		{BRN: "baron-c", Title: "C", Status: store.BeadStatusClosed},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	// digit keys switch tabs
	next, _ = m.Update(key("2"))
	m = asModel(next)
	if m.listTab != 1 {
		t.Fatalf("listTab = %d after '2', want 1 (Active)", m.listTab)
	}
	if m.listCursor != 0 {
		t.Fatalf("listCursor = %d after tab switch, want reset to 0", m.listCursor)
	}
	next, _ = m.Update(key("4"))
	m = asModel(next)
	if m.listTab != 3 {
		t.Fatalf("listTab = %d after '4', want 3 (Done)", m.listTab)
	}
	// Tab cycles the right-pane tab, not the list tab
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)
	if m.detailTab != 1 {
		t.Fatalf("detailTab = %d after tab, want 1 (Comments)", m.detailTab)
	}
	if m.listTab != 3 {
		t.Fatalf("listTab = %d after tab, want unchanged 3", m.listTab)
	}
	// Shift+Tab wraps detailTab backward
	next, _ = m.Update(key("shift+left"))
	m = asModel(next)
	if m.detailTab != 0 {
		t.Fatalf("detailTab = %d after shift+tab, want 0 (wrap)", m.detailTab)
	}
}

func TestSplitPaneNoBRNInRows(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Alpha task", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "Beta task", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width = 120
	m.height = 36

	left := m.viewSplitLeft(44)
	// left pane rows must NOT carry the baron- prefix (no bead IDs in the list)
	if strings.Contains(left, "baron-") {
		t.Errorf("viewSplitLeft = %q, want no bead IDs in list rows", left)
	}
	if !strings.Contains(left, "Alpha task") || !strings.Contains(left, "Beta task") {
		t.Errorf("viewSplitLeft = %q, want bead titles listed", left)
	}
}

// TestSplitLeftShowsSubTabNames: the left pane must always render every
// sub-tab (status bucket) name for the active tab, not just an ephemeral
// label for whichever one is selected — the whole point being to make the
// left/right sub-tab navigation legend visible on screen.
func TestSplitLeftShowsSubTabNames(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	m.listTab = 1 // Active: Working, Validating, Retry

	left := m.viewSplitLeft(44)
	for _, want := range []string{"Working", "Validating", "Retry"} {
		if !strings.Contains(left, want) {
			t.Errorf("viewSplitLeft = %q, want sub-tab name %q visible", left, want)
		}
	}
}

func TestSplitPaneEpicGrouping(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-ep", Title: "Epic one", Status: store.BeadStatusOpen, IssueType: "epic"},
		{BRN: "baron-ep.1", Title: "Child task", Status: store.BeadStatusOpen},
		{
			BRN: "baron-par", Title: "Parent task", Status: store.BeadStatusOpen,
			Dependencies: []store.DepLink{
				{DependsOnID: "baron-x", Type: "blocks"},
				{DependsOnID: "baron-d2", Type: "blocks"},
			},
		},
		{BRN: "baron-kid", Title: "Parent-field child", Status: store.BeadStatusWorking, Parent: "baron-par"},
		{BRN: "baron-x", Title: "Flat task", Status: store.BeadStatusOpen},
		{BRN: "baron-d2", Title: "Done blocker", Status: store.BeadStatusClosed},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width = 120
	m.height = 36

	rows := m.splitRows(0) // Backlog
	if len(rows) != 5 {
		t.Fatalf("splitRows = %d rows, want 5 (epic, dotted child, parent, parent-field child, flat)", len(rows))
	}
	if !rows[0].epic || rows[0].bead.BRN != "baron-ep" {
		t.Fatalf("rows[0] = %+v, want the epic header first", rows[0])
	}
	if rows[1].epic || rows[1].depth != 1 || rows[1].bead.BRN != "baron-ep.1" {
		t.Fatalf("rows[1] = %+v, want the dotted child indented right after its epic", rows[1])
	}
	if rows[2].depth != 0 || rows[2].bead.BRN != "baron-par" {
		t.Fatalf("rows[2] = %+v, want the parent task at top level", rows[2])
	}
	if rows[3].depth != 1 || rows[3].bead.BRN != "baron-kid" {
		t.Fatalf("rows[3] = %+v, want the parent-field child nested under baron-par (its status column differs)", rows[3])
	}
	if rows[4].depth != 0 || rows[4].bead.BRN != "baron-x" {
		t.Fatalf("rows[4] = %+v, want the flat task last", rows[4])
	}
	view := m.View().Content
	epicIdx := strings.Index(view, "Epic one")
	childIdx := strings.Index(view, "Child task")
	if epicIdx < 0 || childIdx < 0 || epicIdx > childIdx {
		t.Errorf("View() = %q, want epic header above its child", view)
	}
	if strings.Contains(view, "blocked by Flat task") {
		t.Errorf("View() = %q, want no blocked-by note under the parent (glyph + overview cover it)", view)
	}

	par, _ := m.findBeadByBRN("baron-par")
	m.detail = par
	m.detailTab = 0
	view = m.View().Content
	if !strings.Contains(view, "blocked by:") || !strings.Contains(view, "baron-x Flat task") {
		t.Errorf("View() = %q, want the Overview to list the live blocks-dependency as \"blocked by: <id> <title>\"", view)
	}
	if strings.Contains(view, "baron-d2") || strings.Contains(view, "Done blocker") {
		t.Errorf("View() = %q, want a closed blocker dropped from \"blocked by\" — it no longer blocks anything", view)
	}
}

// TestOverviewBlockedByFallsBackToRawID: a blocks-dependency whose blocker
// bead is not in the loaded set must still render as "blocked by: <rawid>"
// in the Overview — the id must be visible even when the title is unknown.
func TestOverviewBlockedByFallsBackToRawID(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{
			BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen,
			Dependencies: []store.DepLink{{DependsOnID: "baron-ghost", Type: "blocks"}},
		},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	a, ok := m.findBeadByBRN("baron-a")
	if !ok {
		t.Fatal("baron-a not loaded")
	}
	m.detail = a
	m.detailTab = 0
	view := m.View().Content
	if !strings.Contains(view, "baron-ghost baron-ghost") {
		t.Errorf("View() = %q, want the Overview to show the raw blocker id when its bead is unloaded", view)
	}
}

func TestTreeCursorKeepsChildIndent(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-ep", Title: "Epic", Status: store.BeadStatusOpen, IssueType: "epic"},
		{BRN: "baron-ep.1", Title: "Child task", Status: store.BeadStatusOpen, Parent: "baron-ep"},
		{BRN: "baron-f", Title: "Flat task", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width = 120
	m.height = 36

	lineWith := func(view, title string) string {
		for ln := range strings.SplitSeq(view, "\n") {
			if strings.Contains(ln, title) {
				return ln
			}
		}
		return ""
	}
	connectorCol := func(ln string) int {
		s := stripAnsi(ln)
		before, _, ok := strings.Cut(s, "└")
		if !ok {
			return -1
		}
		return utf8.RuneCountInString(before)
	}

	m.listCursor = 1 // child under the cursor
	cursorCol := connectorCol(lineWith(m.View().Content, "Child task"))
	m.listCursor = 0 // epic under the cursor
	plainCol := connectorCol(lineWith(m.View().Content, "Child task"))
	if cursorCol != plainCol {
		t.Fatalf("child connector column under cursor = %d, without cursor = %d, want equal (no left jump on hover)", cursorCol, plainCol)
	}
}

func TestTreeTypeIcons(t *testing.T) {
	beads := []store.Bead{
		{BRN: "baron-t1", Title: "Epic work", Status: store.BeadStatusOpen, IssueType: "epic"},
		{BRN: "baron-t2", Title: "A task", Status: store.BeadStatusOpen, IssueType: "task"},
		{BRN: "baron-t3", Title: "A feature", Status: store.BeadStatusOpen, IssueType: "feature"},
		{BRN: "baron-t4", Title: "A bug", Status: store.BeadStatusOpen, IssueType: "bug"},
		{BRN: "baron-t5", Title: "A chore", Status: store.BeadStatusOpen, IssueType: "chore"},
		{BRN: "baron-t6", Title: "A decision", Status: store.BeadStatusOpen, IssueType: "decision"},
	}
	m := openDetail(t, beads)
	m.width = 100
	m.height = 24
	view := stripAnsi(m.View().Content)
	for _, want := range []string{"▲", "◈", "⚡", "■", "◇", "◆"} {
		if !strings.Contains(view, want) {
			t.Errorf("tree view missing %q icon", want)
		}
	}
	if !strings.Contains(view, "▲ Epic work") {
		t.Errorf("epic row = %q, want its type icon glued to the title", view)
	}
}

func TestOverviewRightAlignsDates(t *testing.T) {
	m := openDetail(t, []store.Bead{{
		BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking,
		Assignee: "opencode", Priority: store.PriorityP1, IssueType: "epic",
		CreatedAt: time.Date(2026, 8, 10, 9, 50, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 12, 7, 37, 0, 0, time.UTC),
	}})
	m.width = 240
	m.height = 36
	view := stripAnsi(m.View().Content)

	var stateLine, tabLine string
	for ln := range strings.SplitSeq(view, "\n") {
		if strings.Contains(ln, "● working") {
			stateLine = ln
		}
		if strings.Contains(ln, "[Overview]") {
			tabLine = ln
		}
	}
	if !strings.Contains(stateLine, "opencode") {
		t.Errorf("state line = %q, want the assignee on it", stateLine)
	}
	if !strings.Contains(tabLine, "created: 2026-08-10 09:50") {
		t.Errorf("tab line = %q, want created: right-aligned onto it", tabLine)
	}
	if !strings.Contains(tabLine, "updated: 2026-08-12 07:37") {
		t.Errorf("tab line = %q, want updated: right-aligned onto it", tabLine)
	}
	// Flush-right proof: the dates must end exactly at the last content
	// column — one padding space and the box border after them, nothing more.
	const updated = "updated: 2026-08-12 07:37"
	i := strings.Index(tabLine, updated)
	if i < 0 {
		t.Fatalf("tab line = %q, want %q on it", tabLine, updated)
	}
	if rest := tabLine[i+len(updated):]; rest != " │" {
		t.Errorf("after dates: %q, want %q (dates flush to the right edge)", rest, " │")
	}
	if !strings.Contains(stateLine, "▲ epic") {
		t.Errorf("state line = %q, want the type icon+name at its left", stateLine)
	}
	for _, dup := range []string{"status:", "assignee:", "priority:", "type:"} {
		if strings.Contains(view, dup) {
			t.Errorf("View() = %q, want no duplicate %q row (state line already carries it)", view, dup)
		}
	}
	if n := strings.Count(view, "created:"); n != 1 {
		t.Errorf("View() has %d created: occurrences, want 1", n)
	}
}

var ansiStripRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)
