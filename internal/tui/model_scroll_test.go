package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/store"
)

// TestMouseWheelScrollsDetailPane: wheel over the right pane scrolls the
// detail pane instead of falling through to the terminal (mouse capture).
// liveOffset is from-bottom: wheel up enters history, wheel down returns.
func TestMouseWheelScrollsDetailPane(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	// The Agent tab is the one that genuinely overflows (live output lines).
	m.detailTab = 1
	p := m.agentPaneFor("baron-a")
	for i := range 80 {
		p.summary = append(p.summary, fmt.Sprintf("line %d", i))
	}

	// The Agent tab's own scroll offset (p.offset, driven by linesLen) is
	// what mouse-wheel actually clamps against on this tab — not
	// detailMaxOffset/m.detailScroll, which only apply to tabs 0/3 (see
	// viewSplitDetailPane/viewSessionPane).
	if m.linesLen("baron-a", kindAgent) <= m.detailPaneHeight() {
		t.Fatalf("linesLen = %d, detailPaneHeight = %d, want linesLen > detailPaneHeight (detail must overflow)",
			m.linesLen("baron-a", kindAgent), m.detailPaneHeight())
	}
	// No live session for baron-a, so this is the persisted-summary
	// fallback: scroll state lives in p.vp (a viewport.Model), not the
	// plain p.offset int a live session's own emulator frame uses — see
	// agentPane's doc comment and scrollSessionPane.
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 80, Y: 10})
	m = asModel(next)
	if p.vp.AtBottom() {
		t.Error("pane.vp still at the bottom after wheel up into history")
	}
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 80, Y: 10})
	if !p.vp.AtBottom() {
		t.Errorf("pane.vp.YOffset() = %d, want back at the bottom (%d) after wheel down", p.vp.YOffset(), p.vp.TotalLineCount()-p.vp.Height())
	}
}

// TestPersistedSummaryDefaultsToBottom: a finished run's persisted summary
// (no live session) must open showing its tail — the end of the log is
// what you want to see first for a run that's already over — matching
// agentPane.offset's old "0 = bottom" zero-value default. Rendering it
// (View, not a scroll key) must never leave the viewport at the top.
func TestPersistedSummaryDefaultsToBottom(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	m.detailTab = 1
	p := m.agentPaneFor("baron-a")
	for i := range 80 {
		p.summary = append(p.summary, fmt.Sprintf("line %d", i))
	}

	_ = m.View() // first render — must default to the bottom, no scroll key pressed
	if !p.vp.AtBottom() {
		t.Errorf("fresh persisted-summary pane: p.vp.YOffset() = %d, want at the bottom by default", p.vp.YOffset())
	}
	if got := m.View().Content; !strings.Contains(got, "line 79") {
		t.Errorf("View() = %q, want the last line (tail) visible by default", got)
	}
}

// TestPersistedSummaryScrollSurvivesRerender: View() runs on every single
// Update, including ones unrelated to scrolling — a naive SetContentLines
// call on every render would silently reset the user's position each time
// (see syncPaneContent's doc comment). Scrolling once, then rendering
// several more times with unchanged content, must not drift the position.
func TestPersistedSummaryScrollSurvivesRerender(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	m.detailTab = 1
	p := m.agentPaneFor("baron-a")
	for i := range 80 {
		p.summary = append(p.summary, fmt.Sprintf("line %d", i))
	}

	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 80, Y: 10})
	m = asModel(next)
	scrolledTo := p.vp.YOffset()
	if p.vp.AtBottom() {
		t.Fatal("wheel up must move off the bottom before this test can check anything")
	}
	for range 5 {
		_ = m.View()
		if p.vp.YOffset() != scrolledTo {
			t.Fatalf("p.vp.YOffset() drifted to %d after a content-unchanged re-render, want it to stay at %d", p.vp.YOffset(), scrolledTo)
		}
	}
}

// TestOverviewTabSwitchResetsScroll: scrolling the Audit tab and switching
// back to Overview must land at the top, not carry over an unrelated
// scroll position from different content — the single shared m.detailVP's
// whole reason for existing is that both tabs already reset on every
// switch (see detailVP's doc comment in model.go).
func TestOverviewTabSwitchResetsScroll(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}})
	var events []store.AuditEvent
	for i := range 60 {
		// action "gate" (not "run") with a unique detail per event: auditKey
		// strips a run event's trailing "(duration)" before deduping, which
		// would collapse 60 "launched opencode (Nm)" entries into a single
		// "×60" line — nowhere near enough to overflow the pane.
		events = append(events, store.AuditEvent{
			Time:  time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute),
			Actor: store.Actor{Type: store.ActorAgent, Name: "opencode"}, Action: "gate",
			Target: "baron-a", Detail: fmt.Sprintf("gate passed: check-%d", i),
		})
	}
	next, _ := m.Update(auditEventsLoadedMsg{events: events})
	m = asModel(next)
	m.width, m.height = 120, 36
	m.detailTab = 3 // Audit

	next, _ = m.Update(key("pgdown"))
	m = asModel(next)
	if m.detailVP.AtTop() {
		t.Fatal("pgdown on an overflowing Audit tab must move off the top before this test can check anything")
	}

	next, _ = m.Update(key("shift+right")) // Audit -> Overview (wraps past detailTabCount)
	m = asModel(next)
	if m.detailTab != 0 {
		t.Fatalf("detailTab = %d, want 0 (Overview) after wrapping past Audit", m.detailTab)
	}
	if !m.detailVP.AtTop() {
		t.Errorf("m.detailVP.YOffset() = %d, want 0 (top) after switching tabs", m.detailVP.YOffset())
	}
}

// TestOverviewScrollSurvivesContentReload: unlike the persisted-summary
// pane's tail-follow default, the Overview/Audit viewport must NOT snap
// back to any fixed position when its data reloads mid-view (comments
// arriving async) — a still-in-range scroll position is left exactly
// where the user put it.
func TestOverviewScrollSurvivesContentReload(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen, Description: strings.Repeat("word ", 400)}})
	m.width, m.height = 120, 36
	m.detailTab = 0

	next, _ := m.Update(key("pgdown"))
	m = asModel(next)
	if m.detailVP.AtTop() {
		t.Fatal("pgdown on an overflowing Overview tab must move off the top before this test can check anything")
	}
	scrolledTo := m.detailVP.YOffset()

	next, _ = m.Update(commentsLoadedMsg{comments: []store.Comment{{Author: "bob", Text: "ship it"}}})
	m = asModel(next)
	if m.detailVP.YOffset() != scrolledTo {
		t.Errorf("m.detailVP.YOffset() = %d after comments reloaded, want it to stay at %d (position preserved, not reset)", m.detailVP.YOffset(), scrolledTo)
	}
}

// TestMouseClickSelectsListRow: a click on a left-pane row moves the cursor
// to that row (first list row sits at absolute Y=5:
// header+blank+border+tab+subtab).
func TestMouseClickSelectsListRow(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	if m.listCursor != 0 {
		t.Fatalf("listCursor = %d, want 0", m.listCursor)
	}

	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: 5})
	m = asModel(next)
	if m.listCursor != 1 {
		t.Errorf("listCursor = %d, want 1 (clicked second row)", m.listCursor)
	}
	if m.detail.BRN != "baron-b" {
		t.Errorf("detail = %+v, want baron-b selected", m.detail)
	}
}

// TestRunOnUnassignedOpensPicker: 'r' on a bead with no assignee opens the
// model picker instead of failing silently — the user picks a model, then
// the run starts automatically.
func TestRunOnUnassignedOpensPicker(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("r"))
	m = asModel(next)
	if m.modelChoices == nil {
		t.Fatal("modelChoices = nil, want the picker open after r on an unassigned bead")
	}
	if m.runAfterAssign != "baron-a" {
		t.Fatalf("runAfterAssign = %q, want baron-a", m.runAfterAssign)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the async model-load command (no run yet, picker open)")
	}
	// The 'r' case pairs the model fetch with the "why no run" notice, so the
	// fetch may sit inside a batch.
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if _, ok := c().(modelsLoadedMsg); ok {
				return
			}
		}
		t.Fatal("batch = no modelsLoadedMsg, want the async model-load command")
	}
	if _, ok := cmd().(modelsLoadedMsg); !ok {
		t.Fatalf("cmd() = %T, want modelsLoadedMsg", cmd())
	}
}

// TestPickerAssignChainsRun: picking a model in the picker opened by 'r'
// assigns the bead, and the assign's success starts the run automatically.
func TestPickerAssignChainsRun(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("r"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	if m.modelChoices == nil {
		t.Fatal("want picker open")
	}

	// Enter on the first (cached-ranked) model.
	next, cmd = m.Update(key("enter"))
	m = asModel(next)
	if m.modelChoices != nil {
		t.Fatal("modelChoices != nil, want picker closed after picking")
	}
	// The assign command is in flight, batched with huh's own completion
	// cmd (see updateModelPickerKey) — find the assign among the batch and
	// simulate its success.
	var cr commandRanMsg
	var found bool
	for _, msg := range flattenBatch(cmd()) {
		if cr, found = msg.(commandRanMsg); found {
			break
		}
	}
	if !found {
		t.Fatal("batch does not contain a commandRanMsg (assign)")
	}
	next, cmd2 := m.Update(cr)
	m = asModel(next)
	if m.pendingRunAfterAssign != "baron-a" {
		t.Fatalf("pendingRunAfterAssign = %q, want baron-a (spawn waits for the reload, not fired yet)", m.pendingRunAfterAssign)
	}
	if cmd2 == nil {
		t.Fatal("expected the beads-reload command after assign success")
	}
	// The chain doesn't actually spawn until the reload commandRanMsg itself
	// queued lands with the new assignee in it — see pendingRunAfterAssign's
	// doc comment for why spawning straight off commandRanMsg would still
	// see the bead unassigned.
	next, cmd2 = m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen, Assignee: "agent"},
	}})
	m = asModel(next)
	if m.detailTab != 1 {
		t.Fatalf("detailTab = %d, want 1 (assign success chained into the embedded terminal)", m.detailTab)
	}
	if cmd2 == nil {
		t.Fatal("expected the embedded spawn batch after the reload")
	}
	batch, ok := cmd2().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", cmd2())
	}
	// The batch must carry the embedded spawn (not a legacy run command).
	// Executing it is safe in the test env: the empty fake store makes the
	// spawn fail cleanly with an error msg, no real process is started.
	// The chain's spawn is itself a nested batch, so flattenBatch descends.
	var gotSpawn bool
	for _, sub := range batch {
		for _, msg := range flattenBatch(sub()) {
			switch msg.(type) {
			case agentSpawnedMsg:
				gotSpawn = true
			case commandRanMsg:
				t.Fatalf("legacy run command fired after assign, want the embedded spawn")
			}
		}
	}
	if !gotSpawn {
		t.Fatal("batch = no spawn, want the embedded terminal spawn")
	}
}

// TestPickerAssignFailDropsChain: if the assign fails, no run is started and
// the pending chain is cleared so a later success can't fire it.
func TestPickerAssignFailDropsChain(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("r"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	next, cmd = m.Update(key("enter"))
	m = asModel(next)
	// The assign command is batched with huh's own completion cmd (see
	// updateModelPickerKey) — find it among the batch.
	var cr commandRanMsg
	var found bool
	for _, msg := range flattenBatch(cmd()) {
		if cr, found = msg.(commandRanMsg); found {
			break
		}
	}
	if !found {
		t.Fatal("batch does not contain a commandRanMsg (assign)")
	}
	cr.err = errors.New("assign failed")
	next, cmd2 := m.Update(cr)
	m = asModel(next)
	if m.pendingRunAfterAssign != "" {
		t.Fatalf("pendingRunAfterAssign = %q, want empty (assign failed, nothing to spawn once reloaded)", m.pendingRunAfterAssign)
	}
	if m.runAfterAssign != "" {
		t.Fatalf("runAfterAssign = %q, want empty (chain dropped)", m.runAfterAssign)
	}
	if cmd2 == nil {
		t.Fatal("expected refresh commands")
	}
}

// TestPickerEscClearsChain: closing the picker with esc cancels the pending
// run-after-assign chain.
func TestPickerEscClearsChain(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, _ = m.Update(key("r"))
	m = asModel(next)
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.runAfterAssign != "" {
		t.Fatalf("runAfterAssign = %q, want empty after esc", m.runAfterAssign)
	}
	if m.modelChoices != nil {
		t.Fatal("modelChoices != nil, want picker closed")
	}
}

// TestBracketsCycleListTab: [ and ] cycle the list's column tabs (same as
// h/l/←/→), so tab stays on the detail pane's tabs.
func TestBracketsCycleListTab(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}})
	if m.listTab != 0 {
		t.Fatalf("listTab = %d, want 0", m.listTab)
	}
	next, _ := m.Update(key("]"))
	m = asModel(next)
	if m.listTab != 1 {
		t.Fatalf("listTab = %d after ], want 1", m.listTab)
	}
	if m.detailTab != 0 {
		t.Fatalf("detailTab = %d, want unchanged 0", m.detailTab)
	}
	next, _ = m.Update(key("["))
	m = asModel(next)
	if m.listTab != 0 {
		t.Fatalf("listTab = %d after [, want 0 (wrap)", m.listTab)
	}
}
