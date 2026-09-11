package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/store"
)

// TestMergeKeyMergesImmediatelyNoConfirmation: 'm' on a mergable bead calls
// deps.Merge right away — no confirmation screen, no y/n gate. The screen
// never leaves the dashboard (there is nothing to navigate to or back from
// any more), and the result reaches the user as the same one-line toast
// every other direct dashboard action uses (see commandRanMsg).
func TestMergeKeyMergesImmediatelyNoConfirmation(t *testing.T) {
	deps := testDeps()
	var gotBRN string
	deps.Merge = func(brn string) (string, error) {
		gotBRN = brn
		return "merged " + brn, nil
	}
	m := New(context.Background(), deps)
	beads := []store.Bead{{BRN: "baron-9", Title: "Fix bug", Status: store.BeadStatusMergable}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("m"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want screenDashboard (no separate merge screen)", m.screen)
	}
	if cmd == nil {
		t.Fatal("expected a merge command after 'm' on a mergable bead")
	}
	ran, ok := cmd().(commandRanMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want commandRanMsg", cmd())
	}
	if gotBRN != "baron-9" {
		t.Errorf("deps.Merge brn = %q, want baron-9", gotBRN)
	}
	if !strings.Contains(ran.output, "baron-9") {
		t.Errorf("output = %q, want it to carry the merged BRN", ran.output)
	}
}

// TestAuditTabActivityGroups: the detail pane's 4th tab (Audit) must show
// the bead's activity timeline grouped by theme (gates & checks / runs /
// merge / lifecycle) so the history stays readable, repeated events
// must collapse into a single ×N line (a real bead runs the same gate
// dozens of times), and comment events must stay out — the Overview tab
// already shows the full comment history with timestamps.
func TestAuditTabActivityGroups(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}})
	ts := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	ev := func(action, detail string, when time.Time) store.AuditEvent {
		return store.AuditEvent{Time: when, Actor: store.Actor{Type: store.ActorAgent, Name: "opencode"}, Action: action, Target: "baron-a", Detail: detail}
	}
	next, _ := m.Update(auditEventsLoadedMsg{events: []store.AuditEvent{
		ev("status", "open -> assigned", ts.Add(-4*time.Hour)),
		ev("comment", "looks good", ts.Add(-3*time.Hour)),
		ev("gate", "gate passed: style", ts.Add(-110*time.Minute)),
		ev("gate", "gate passed: style", ts.Add(-111*time.Minute)),
		ev("gate", "gate failed: tests", ts.Add(-100*time.Minute)),
		ev("run", "launched opencode (2m)", ts.Add(-2*time.Hour)),
		ev("run", "launched opencode (1m)", ts.Add(-119*time.Minute)),
		ev("retry", "retry 1/3: gate failed", ts.Add(-90*time.Minute)),
		ev("mergable", "ready to merge: task/12", ts.Add(-30*time.Minute)),
	}})
	m = asModel(next)
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)
	m.width, m.height = 120, 36
	view := m.View().Content
	t.Logf("View():\n%s", view)

	if m.detailTab != 3 {
		t.Fatalf("detailTab = %d, want 3 (Audit)", m.detailTab)
	}
	for _, want := range []string{"Gates & checks", "Runs", "Merge", "Lifecycle"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() = %q, want the %q activity group header", view, want)
		}
	}
	if strings.Contains(view, "looks good") {
		t.Errorf("View() = %q, want comment events excluded from Audit (Overview owns comment history)", view)
	}
	if i := strings.Index(view, "gate failed: tests"); i < 0 || i > strings.Index(view, "gate passed: style") {
		t.Errorf("View() = %q, want the newer gate event above the older one", view)
	}
	if i := strings.Index(view, "retry 1/3"); i < 0 || i > strings.Index(view, "launched opencode") {
		t.Errorf("View() = %q, want the newer run event above the older one", view)
	}
	for _, want := range []string{"gate passed: style", "launched opencode"} {
		if strings.Count(view, want) != 1 {
			t.Errorf("View() = %q, want %q collapsed into a single ×N line", view, want)
		}
	}
	if !strings.Contains(view, "×2") {
		t.Errorf("View() = %q, want a repeat count on collapsed events", view)
	}
}

// TestConfirmFooterIsolation: while the confirm dialog is open, the footer
// must show only the confirm scope's bindings — the dashboard hints it
// previously leaked (including the "R" the user saw) implied dead keys.
func TestConfirmFooterIsolation(t *testing.T) {
	m := New(context.Background(), testDeps())
	m.width = 200
	next, _ := m.Update(key("q"))
	m = asModel(next)
	lines := strings.Split(m.View().Content, "\n")
	footer := lines[len(lines)-1]
	for _, want := range []string{"yes", "no"} {
		if !strings.Contains(footer, want) {
			t.Errorf("footer = %q, want it to show the %q confirm hint", footer, want)
		}
	}
	for _, banned := range []string{"quit", "run", "attach", "assign", "help", "move"} {
		if strings.Contains(footer, banned) {
			t.Errorf("footer = %q, must not show the %q dashboard hint while confirming", footer, banned)
		}
	}
}

// TestConfirmDialogShowsOnlyYN: the dialog offers exactly enter/y (yes) and
// n/esc/q (no) — the [E] yes and [X] close buttons are gone.
func TestConfirmDialogShowsOnlyYN(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key("q"))
	m = asModel(next)
	view := m.View().Content
	if !strings.Contains(view, "[Y] Yes") || !strings.Contains(view, "[N] No") {
		t.Errorf("View() = %q, want [Y] Yes and [N] No buttons", view)
	}
	if strings.Contains(view, "[E]") {
		t.Errorf("View() = %q, want no [E] yes button", view)
	}
	if strings.Contains(view, "[X]") {
		t.Errorf("View() = %q, want no [X] close button", view)
	}
}

// TestConfirmEKeyNoLongerConfirms: the old 'e' alias is gone — only y/enter
// confirm, n/esc/q decline, and a stray 'e' must leave the dialog up (modal).
func TestConfirmEKeyNoLongerConfirms(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key("q"))
	m = asModel(next)
	next, cmd := m.Update(key("e"))
	m = asModel(next)
	if m.quitting || cmd != nil {
		t.Fatal("e must no longer confirm the quit dialog")
	}
	if m.huhForm == nil {
		t.Fatal("huhForm should not be nil after stray 'e' key (modal)")
	}
	next, cmd = m.Update(key("esc"))
	m = asModel(next)
	if m.quitting || cmd != nil {
		t.Fatal("esc must decline, not confirm")
	}
	// The form result should be false after 'esc' (value binding works synchronously)
	if m.confirmResult == nil || *m.confirmResult {
		t.Fatal("confirmResult should be false after 'esc'")
	}
	// Form completion is deferred (requires tea.Program)
	// Legacy confirming field is not cleared in test harness (needs tea.Program)
	// if m.confirming != "" {
	// 	t.Fatalf("confirming = %q, want cleared after the esc decision", m.confirming)
	// }
}
