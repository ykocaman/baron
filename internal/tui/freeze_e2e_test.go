package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

func TestFreezeE2EDashboardFrequencyOrder(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 160, 40
	now := time.Now()
	b1 := store.Bead{BRN: domain.BRN("baron-1"), Title: "oldest", Status: store.BeadStatusOpen, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-3 * time.Hour)}
	b2 := store.Bead{BRN: domain.BRN("baron-2"), Title: "newest", Status: store.BeadStatusOpen, CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour)}
	b3 := store.Bead{BRN: domain.BRN("baron-3"), Title: "middle", Status: store.BeadStatusOpen, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)}
	m.beads = []store.Bead{b1, b2, b3}
	m.listTab = 0
	rows := m.splitRows(0)
	if len(rows) < 3 {
		t.Fatalf("rows=%d want 3", len(rows))
	}
	got := string(rows[0].bead.BRN) + " " + string(rows[1].bead.BRN) + " " + string(rows[2].bead.BRN)
	if got != "baron-1 baron-2 baron-3" {
		t.Fatalf("order=%q want baron-1 baron-2 baron-3 (reverted: BRN asc, no UpdatedAt)", got)
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "BARON") {
		t.Fatalf("dashboard view missing BARON")
	}
	if !strings.Contains(view, "Auto:") {
		t.Fatalf("dashboard header missing Auto: %q", view[:500])
	}
}

func TestFreezeE2EPromptTriPane(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 160, 40
	bead := store.Bead{BRN: domain.BRN("baron-f1"), Title: "caching layer", Status: store.BeadStatusWorking, IssueType: "task"}
	m.beads = []store.Bead{bead}
	m.detail = bead
	next, _ := m.Update(key("P"))
	m = asModel(next)
	if m.screen != screenCrew {
		t.Fatalf("P: screen=%v want crew", m.screen)
	}
	next, _ = m.Update(key("tab"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)
	next, _ = m.Update(key("shift+left"))
	m = asModel(next)
	next, _ = m.Update(key("shift+down"))
	m = asModel(next)
	next, _ = m.Update(key("shift+up"))
	m = asModel(next)
	next, _ = m.Update(key("P"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("P back: screen=%v want dashboard", m.screen)
	}
}

func TestFreezeE2EStatusPickerAndRun(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 160, 40
	bead := store.Bead{BRN: domain.BRN("baron-r1"), Title: "run me", Status: store.BeadStatusOpen, IssueType: "task"}
	m.beads = []store.Bead{bead}
	m.detail = bead
	m.listCursor = 0
	m.listTab = 0
	next, _ := m.Update(key("s"))
	m = asModel(next)
	if m.statusForm == nil {
		t.Fatalf("s: statusForm nil, want picker")
	}
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.statusForm != nil {
		t.Fatalf("esc: statusForm still open")
	}
	next, _ = m.Update(key("r"))
	m = asModel(next)
	hasPicker := m.modelForm != nil || m.modelChoices != nil || m.runAfterAssign != ""
	if !hasPicker {
		t.Fatalf("r on unassigned: no picker (modelForm=%v modelChoices=%v runAfter=%q)", m.modelForm != nil, m.modelChoices != nil, m.runAfterAssign)
	}
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	bead2 := store.Bead{BRN: domain.BRN("baron-r2"), Title: "assigned", Status: store.BeadStatusAssigned, IssueType: "task", Assignee: "claude"}
	m.beads = []store.Bead{bead2}
	m.detail = bead2
	next, _ = m.Update(key("r"))
	m = asModel(next)
	next, _ = m.Update(key("t"))
	m = asModel(next)
	next, _ = m.Update(key("shift+esc"))
	m = asModel(next)
	if m.termFocus {
		t.Fatalf("shift+esc: termFocus still true")
	}
}

func TestFreezeE2EHelpAndAutoHeader(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 160, 40
	next, _ := m.Update(key("?"))
	m = asModel(next)
	if m.screen != screenHelp {
		t.Fatalf("?: screen=%v want help", m.screen)
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "+/-") && !strings.Contains(view, "Auto") {
		t.Fatalf("help missing +/- Auto: %q", view[:800])
	}
	next, _ = m.Update(key("esc"))
	m = asModel(next)

	// No bead selected yet (nothing loaded): +/- is a no-op, and the header
	// defaults to ON — auto-run is opt-out per bead (see m.autoOffBRNs),
	// matching general.auto_start's own "on unless told otherwise" default.
	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "Auto: ON") {
		t.Fatalf("header Auto ON (default, nothing selected) missing: %q", view[:800])
	}
	next, _ = m.Update(key("-"))
	m = asModel(next)
	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "Auto: ON") {
		t.Fatalf("-: header should stay Auto: ON with no bead selected: %q", view[:800])
	}

	next, _ = m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	// A real bead is selected now: '-' opts it out, '+' opts it back in.
	next, _ = m.Update(key("-"))
	m = asModel(next)
	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "Auto: OFF") {
		t.Fatalf("-: header missing Auto: OFF for selected bead: %q", view[:800])
	}
	if !m.autoOffBRNs["baron-a"] {
		t.Fatalf("-: baron-a not recorded in autoOffBRNs")
	}
	next, _ = m.Update(key("+"))
	m = asModel(next)
	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "Auto: ON") {
		t.Fatalf("+: header missing Auto: ON for selected bead: %q", view[:800])
	}
	if m.autoOffBRNs["baron-a"] {
		t.Fatalf("+: baron-a still recorded in autoOffBRNs")
	}
}

func TestFreezeE2ETickSingleChain(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 160, 40
	m.tickScheduled = true
	next, _ := m.Update(tickMsg{})
	m = asModel(next)
	for range 5 {
		next, _ := m.Update(key("j"))
		m = asModel(next)
		next, _ = m.Update(key("k"))
		m = asModel(next)
	}
	if m.tickScheduled && !m.tickScheduled {
		t.Fatalf("tickScheduled broken")
	}
}

func TestFreezeE2EHappyOpenAssignedWorking(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 160, 40
	bead := store.Bead{BRN: domain.BRN("baron-h1"), Title: "happy path", Status: store.BeadStatusOpen, IssueType: "task"}
	m.beads = []store.Bead{bead}
	m.detail = bead
	m.listCursor = 0
	m.listTab = 0
	next, _ := m.Update(key("p"))
	m = asModel(next)
	if !m.quickPromptMode {
		t.Fatalf("p: quickPromptMode false")
	}
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.quickPromptMode {
		t.Fatalf("esc: quickPromptMode still true")
	}
}
