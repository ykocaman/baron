package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// TestHumanQueueViewport: the Needs You tab's rows must be windowed to
// the pane height, not render all 40.
func TestHumanQueueViewport(t *testing.T) {
	m := New(context.Background(), testDeps())
	var beads []store.Bead
	for i := range 40 {
		beads = append(beads, store.Bead{
			BRN:    domain.BRN(fmt.Sprintf("baron-%02d", i)),
			Title:  "T",
			Status: store.BeadStatusHumanQueue,
		})
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.listTab = 2 // Needs You tab
	m.width, m.height = 120, 36

	if got := len(strings.Split(m.View().Content, "\n")); got > m.height {
		t.Errorf("human queue View() has %d lines on %d-tall pane, want <= %d", got, m.height, m.height)
	}
}

// TestSplitPaneWidthsFollowSpec: the left list should be ≈38% of the
// dashboard width (120 cols -> ~44), not an even 50/50 split.
func TestSplitPaneWidthsFollowSpec(t *testing.T) {
	left, right := splitPaneWidths(120)
	if left < 40 || left > 48 {
		t.Errorf("left = %d, want ~38%% of 120 (roughly 40-48)", left)
	}
	if left+right >= 120 {
		t.Errorf("left(%d)+right(%d) = %d, want less than total width (room for the gap/border)", left, right, left+right)
	}
}

// TestCommentRefreshesFromSplitPane: posting a comment from the split-pane
// dashboard must reload the comments list (the right pane shows the
// Comments tab after submit). Fails today: actionDoneMsg only reloads
// comments on screenBeadDetail, so the split-pane list stays stale.
func TestCommentRefreshesFromSplitPane(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("start = screen:%v, want the split-pane dashboard", m.screen)
	}

	next, _ = m.Update(key("c"))
	m = asModel(next)
	if m.screen != screenForm {
		t.Fatalf("screen = %v, want screenForm after c", m.screen)
	}
	for _, r := range "nice work" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	next, cmd := m.Update(key("enter"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want back on split-pane dashboard after submit", m.screen)
	}
	if m.detailTab != 0 {
		t.Fatalf("detailTab = %d, want 0 (Overview tab with comments)", m.detailTab)
	}
	ad := firstActionDone(t, cmd)

	_, batchCmd := m.Update(ad)
	batch, ok := batchCmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg of reload commands", batchCmd())
	}
	var gotComments bool
	for _, c := range batch {
		if _, ok := c().(commentsLoadedMsg); ok {
			gotComments = true
		}
	}
	if !gotComments {
		t.Error("reload batch has no commentsLoadedMsg, want comments reloaded from split-pane dashboard")
	}
}

// TestSplitPaneHLSwitchesTab: h/l must cycle the list tab directly on the
// split-pane dashboard, resetting any active sub-tab filter.
func TestSplitPaneHLSwitchesTab(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	next, _ = m.Update(key("l"))
	m = asModel(next)
	if m.listTab != 1 {
		t.Fatalf("listTab = %d, want 1 (Active) after l", m.listTab)
	}
	if m.listSubTab != -1 {
		t.Fatalf("listSubTab = %d, want -1 after l", m.listSubTab)
	}
	next, _ = m.Update(key("l"))
	m = asModel(next)
	if m.listTab != 2 {
		t.Fatalf("listTab = %d, want 2 (Needs You) after l", m.listTab)
	}
	next, _ = m.Update(key("l"))
	m = asModel(next)
	if m.listTab != 3 {
		t.Fatalf("listTab = %d, want 3 (Done) after l", m.listTab)
	}
	next, _ = m.Update(key("l"))
	m = asModel(next)
	if m.listTab != 0 {
		t.Fatalf("listTab = %d, want 0 (Backlog, wrapped) after l", m.listTab)
	}
	next, _ = m.Update(key("h"))
	m = asModel(next)
	if m.listTab != 3 {
		t.Fatalf("listTab = %d, want 3 (Done) after h", m.listTab)
	}
	next, _ = m.Update(key("h"))
	m = asModel(next)
	if m.listTab != 2 {
		t.Fatalf("listTab = %d, want 2 (Needs You) after h", m.listTab)
	}
}

// TestSplitPaneLeftRightStepsSubTabsThenSpillsToNextTab: left/right must
// step through the active tab's status buckets one at a time, and only
// spill into the adjacent top-level tab once the current tab's buckets are
// exhausted — a reversal of h/l's "jump straight to the next tab" behavior.
func TestSplitPaneLeftRightStepsSubTabsThenSpillsToNextTab(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	next, _ = m.Update(key("l")) // listTab=1 (Active: Working, Validating, Retry), listSubTab=-1
	m = asModel(next)
	if m.listTab != 1 || m.listSubTab != -1 {
		t.Fatalf("listTab=%d listSubTab=%d, want 1/-1 after l", m.listTab, m.listSubTab)
	}

	next, _ = m.Update(key("right")) // -> Working
	m = asModel(next)
	if m.listTab != 1 || m.listSubTab != 0 {
		t.Fatalf("listTab=%d listSubTab=%d, want 1/0 after right", m.listTab, m.listSubTab)
	}

	next, _ = m.Update(key("right")) // -> Validating
	m = asModel(next)
	if m.listTab != 1 || m.listSubTab != 1 {
		t.Fatalf("listTab=%d listSubTab=%d, want 1/1 after right", m.listTab, m.listSubTab)
	}

	next, _ = m.Update(key("right")) // -> Retry
	m = asModel(next)
	if m.listTab != 1 || m.listSubTab != 2 {
		t.Fatalf("listTab=%d listSubTab=%d, want 1/2 after right", m.listTab, m.listSubTab)
	}

	next, _ = m.Update(key("right")) // buckets exhausted -> spill into tab 2's first bucket
	m = asModel(next)
	if m.listTab != 2 || m.listSubTab != 0 {
		t.Fatalf("listTab=%d listSubTab=%d, want 2/0 after right (spillover)", m.listTab, m.listSubTab)
	}

	next, _ = m.Update(key("left")) // back into tab 1's last bucket
	m = asModel(next)
	if m.listTab != 1 || m.listSubTab != 2 {
		t.Fatalf("listTab=%d listSubTab=%d, want 1/2 after left (spillover)", m.listTab, m.listSubTab)
	}
}

// TestSubTabNavStopsAtOuterBoundaries: left/right spills across tabs in the
// middle of the board, but must stop dead at the very first bucket of the
// very first tab and the very last bucket of the very last tab — no
// wrap-around back to the other end.
func TestSubTabNavStopsAtOuterBoundaries(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	m.listTab, m.listSubTab = 0, 0 // Backlog's first bucket (Open)
	next, _ = m.Update(key("left"))
	m = asModel(next)
	if m.listTab != 0 || m.listSubTab != 0 {
		t.Fatalf("listTab=%d listSubTab=%d, want 0/0 (left must stop, not wrap to the last tab)", m.listTab, m.listSubTab)
	}

	last := len(boardColumns) - 1
	m.listTab, m.listSubTab = last, len(boardColumns[last].statuses)-1 // Done's last bucket (Cancelled)
	next, _ = m.Update(key("right"))
	m = asModel(next)
	if m.listTab != last || m.listSubTab != len(boardColumns[last].statuses)-1 {
		t.Fatalf("listTab=%d listSubTab=%d, want %d/%d (right must stop, not wrap to the first tab)",
			m.listTab, m.listSubTab, last, len(boardColumns[last].statuses)-1)
	}
}

// TestAssignedBeadMovesToAssignedSubTab: bd never persists a literal
// "assigned" status (it only ever sets the assignee field and leaves status
// "open" — see store.Bead.DomainState) — so an open bead with a model
// assigned must still land in the Backlog > Assigned bucket, not sit
// invisibly under Open forever.
func TestAssignedBeadMovesToAssignedSubTab(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusOpen, Assignee: "claude"},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.listTab = 0 // Backlog: Open, Assigned, Blocked

	if got := len(m.bucketRows(0, 0)); got != 1 {
		t.Fatalf("Open bucket rows = %d, want 1 (baron-a only)", got)
	}
	rows := m.bucketRows(0, 1)
	if len(rows) != 1 || rows[0].bead.BRN != "baron-b" {
		t.Fatalf("Assigned bucket rows = %+v, want just baron-b", rows)
	}
}

// TestSubTabCountMatchesDisplayedRows: a bucket's printed count must equal
// exactly how many rows currentRows() shows when that bucket is selected —
// previously the sub-tab bar counted every bead with a matching status
// regardless of nesting, so a closed child under a still-working epic
// inflated the Done tab's "Closed" count even though the child never
// renders there (it stays under its parent in the Active tab, per
// splitRows' "children never vanish into another column" rule).
func TestSubTabCountMatchesDisplayedRows(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-e", Title: "Epic", Status: store.BeadStatusWorking, IssueType: "epic"},
		{BRN: "baron-e.1", Title: "Child", Status: store.BeadStatusClosed, Parent: "baron-e"},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	m.listTab = 3 // Done: Closed, Merged, Cancelled
	m.listSubTab = 0
	if got := len(m.currentRows()); got != 0 {
		t.Fatalf("Done > Closed rows = %d, want 0 (the closed bead is a child under a working epic, not a root)", got)
	}
	if got := len(m.bucketRows(3, 0)); got != 0 {
		t.Fatalf("Done > Closed bucketRows count = %d, want 0 to match what's displayed", got)
	}

	m.listTab = 1 // Active: Working, Validating, Retry
	m.listSubTab = 0
	rows := m.currentRows()
	if len(rows) != 2 {
		t.Fatalf("Active > Working rows = %d, want 2 (epic + its closed child)", len(rows))
	}
	if got := len(m.bucketRows(1, 0)); got != len(rows) {
		t.Fatalf("Active > Working bucketRows count = %d, want %d to match currentRows", got, len(rows))
	}
}

// TestCurrentRowsFiltersToActiveSubTab: with a sub-tab filter active,
// currentRows must show only roots matching that single status — but an
// epic's children stay visible even when their own status differs, per
// splitRows' documented "children never vanish" rule.
func TestCurrentRowsFiltersToActiveSubTab(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-e", Title: "Epic", Status: store.BeadStatusWorking, IssueType: "epic"},
		{BRN: "baron-e.1", Title: "Child", Status: store.BeadStatusRetry, Parent: "baron-e"},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusValidating},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.listTab = 1 // Active: Working, Validating, Retry

	m.listSubTab = -1
	if got := len(m.currentRows()); got != 3 {
		t.Fatalf("unfiltered rows = %d, want 3", got)
	}

	m.listSubTab = 0 // Working
	rows := m.currentRows()
	if len(rows) != 2 {
		t.Fatalf("Working-filtered rows = %d, want 2 (epic + child)", len(rows))
	}
	if rows[0].bead.BRN != "baron-e" || rows[1].bead.BRN != "baron-e.1" {
		t.Fatalf("Working-filtered rows = %+v, want epic then its child", rows)
	}

	m.listSubTab = 1 // Validating
	rows = m.currentRows()
	if len(rows) != 1 || rows[0].bead.BRN != "baron-b" {
		t.Fatalf("Validating-filtered rows = %+v, want just baron-b", rows)
	}
}

// M as a direct keybinding was removed (low-frequency actions live in the
// command bar only) — `:doctor models` is the way to list
// models now, already covered generically by TestModelCommandBarRunsCommand.

// TestSplitPaneStopConfirmsKill: x on the Agent tab with a running session
// asks for confirmation, kills the session and clears its pty.
func TestSplitPaneStopConfirmsKill(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking}}})
	m = asModel(next)
	m.detailTab = 1 // Agent tab focused -> x means "stop the agent"

	// A running session: x asks before killing it.
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", emu: vt.NewSafeEmulator(10, 5)}
	next, _ = m.Update(key("x"))
	m = asModel(next)
	if m.confirming != "stop" {
		t.Fatalf("confirming = %q with a live session, want stop", m.confirming)
	}

	// Confirming kills the session and clears focus, synchronously (same
	// convention as TestModelQuitConfirmation).
	next, cmd := m.Update(key("y"))
	m = asModel(next)
	if m.huhForm != nil || m.confirming != "" {
		t.Errorf("huhForm/confirming not cleared after y: huhForm=%v confirming=%q", m.huhForm, m.confirming)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the status + reload batch after kill")
	}
	if m.sessions["baron-a"].pty != nil {
		t.Error("session pty not closed after kill")
	}
}

// TestStatusMsgAutoDismiss: a status message must clear itself when the
// dismiss timer fires, without lingering in the footer.
func TestStatusMsgAutoDismiss(t *testing.T) {
	m := New(context.Background(), testDeps())
	m.statusMsg = "hello"

	next, cmd := m.Update(statusExpiredMsg{})
	m = asModel(next)
	if m.statusMsg != "" {
		t.Errorf("statusMsg = %q, want empty after statusExpiredMsg", m.statusMsg)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil (no further commands on dismiss)", cmd)
	}
	if strings.Contains(m.View().Content, "hello") {
		t.Errorf("View() = %q, want it NOT to show the dismissed status", m.View().Content)
	}
}

// TestSplitPaneSelectReloadsComments: moving the selection (j/k) must reload
// the newly selected bead's comments, so the right pane never shows a stale
// list from the previous bead.
func TestSplitPaneSelectReloadsComments(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "Beta", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("j"))
	_ = asModel(next)
	if cmd == nil {
		t.Fatal("expected a command after moving the selection")
	}
	var gotComments bool
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if _, ok := c().(commentsLoadedMsg); ok {
				gotComments = true
			}
		}
	case commentsLoadedMsg:
		gotComments = true
	}
	if !gotComments {
		t.Error("selection move did not reload comments, want commentsLoadedMsg")
	}
}

// TestSplitPaneCommentsLoadOnOverviewSwitch: switching back to the Overview
// tab must load comments immediately, not only after posting a comment.
func TestSplitPaneCommentsLoadOnOverviewSwitch(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	// Cycle past Agent, Diff and Audit so the next tab wraps back to Overview.
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)

	next, cmd := m.Update(key("shift+right"))
	m = asModel(next)
	if m.detailTab != 0 {
		t.Fatalf("detailTab = %d, want 0 (Overview)", m.detailTab)
	}
	if cmd == nil {
		t.Fatal("expected a command after switching to the Overview tab")
	}
	var gotComments bool
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if _, ok := c().(commentsLoadedMsg); ok {
				gotComments = true
			}
		}
	case commentsLoadedMsg:
		gotComments = true
	}
	if !gotComments {
		t.Error("Overview tab switch did not load comments")
	}
}
