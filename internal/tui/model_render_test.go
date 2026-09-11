package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/baron-cli/baron/internal/store"
)

// TestHeaderAtRow0AllWidths: the header must always be the first rendered
// line, and once the pane has a real width the whole view must fit its
// height. The beads mirror production data (an epic with long-titled
// children): indented rows used to overflow the left box's inner width,
// lipgloss wrapped them, and the inflated box pushed the header off the top
// of the alt screen.
func TestHeaderAtRow0AllWidths(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-ep", Title: "Epic with a deliberately long title that wraps the tab bar", Status: store.BeadStatusOpen, IssueType: "epic"},
		{BRN: "baron-ep.1", Title: "A child task with a title long enough to overflow the left pane", Status: store.BeadStatusOpen},
		{BRN: "baron-ep.2", Title: "Another child task whose title is also quite long indeed", Status: store.BeadStatusOpen},
		{BRN: "baron-flat", Title: "A flat task with an equally long and overflowing title", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	for _, w := range []int{60, 80, 120} {
		m.width, m.height = w, 36
		lines := strings.Split(m.View().Content, "\n")
		if !strings.Contains(lines[0], "BARON") {
			t.Errorf("width=%d: line 0 = %q, want the header first", w, lines[0])
		}
		if len(lines) > m.height {
			t.Errorf("width=%d: View() has %d lines on a %d-tall pane, want <= %d", w, len(lines), m.height, m.height)
		}
	}
}

// TestToastFloatsAboveFooter: an error/status notice renders as its own
// floating box (see viewWithToast) rather than replacing the footer's hint
// row outright — the hints must stay readable underneath/beside it.
func TestToastFloatsAboveFooter(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 100, 30
	m.statusMsg = "terminal unfocused"

	view := m.View().Content
	if !strings.Contains(view, "terminal unfocused") {
		t.Fatalf("View() = %q, want the notice text", view)
	}
	if !strings.Contains(view, "q quit") {
		t.Errorf("View() = %q, want the footer hints still visible under the toast", view)
	}
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Errorf("View() has %d lines on a %d-tall pane, want <= %d", len(lines), m.height, m.height)
	}
}

// TestToastNeverExceedsHeight: a long error message wraps into a multi-line
// box instead of pushing the composited view past the pane's height —
// viewWithToast anchors off the base's own rendered height, not the
// nominal m.height, since sparse content renders shorter than the pane.
func TestToastNeverExceedsHeight(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.err = errors.New("agent failed to start: opencode: exit status 1 (missing model flag, check your assign step and try again)")
	for _, w := range []int{40, 60, 80, 120} {
		m.width, m.height = w, 20
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > m.height {
			t.Errorf("width=%d: View() has %d lines on a %d-tall pane, want <= %d", w, len(lines), m.height, m.height)
		}
		if !strings.Contains(m.View().Content, "✗") {
			t.Errorf("width=%d: View() = %q, want the error toast", w, m.View().Content)
		}
	}
}

// TestFooterShowsSingleColon: the command-bar affordance is ":shell", not
// the old "::shell" (the ":" prompt plus the hint double-colon).
func TestFooterShowsSingleColon(t *testing.T) {
	m := New(context.Background(), testDeps())
	m.width = 120
	view := m.View().Content
	if !strings.Contains(view, ":shell") {
		t.Errorf("View() = %q, want the ':shell' affordance", view)
	}
	if strings.Contains(view, "::shell") {
		t.Errorf("View() = %q, want no double-colon '::shell'", view)
	}
}

// TestFooterCmdLeftHintsRight: the ":shell" affordance sits at the start of
// the footer line and the keyboard hints are right-aligned after it — a
// reversal of the old layout (hints left, ":shell" right).
func TestFooterCmdLeftHintsRight(t *testing.T) {
	m := New(context.Background(), testDeps())
	m.width = 120
	view := m.View().Content
	cmdIdx := strings.Index(view, ":shell")
	hintIdx := strings.Index(view, "q quit")
	if cmdIdx < 0 || hintIdx < 0 {
		t.Fatalf("View() = %q, want both ':shell' and the hints present", view)
	}
	if cmdIdx > hintIdx {
		t.Errorf("':shell' at %d, hints at %d — want ':shell' first (left), hints after (right)", cmdIdx, hintIdx)
	}
}

// TestHeaderShowsGitBranchRightAligned: the header's right side surfaces
// info the board doesn't show anywhere else — branch name and changed-file
// count — right-aligned, with the project name and version on the left.
func TestHeaderShowsGitBranchRightAligned(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(headerStatsMsg{stats: HeaderStats{Branch: "feature/x", ChangedFiles: 3}})
	m = asModel(next)
	m.width = 140
	view := m.View().Content
	lines := strings.Split(view, "\n")
	header := lines[0]
	if !strings.HasPrefix(header, "BARON") {
		t.Fatalf("header = %q, want it to start with the project name", header)
	}
	before, _, ok := strings.Cut(header, "feature/x")
	if !ok {
		t.Fatalf("header = %q, want the git branch shown", header)
	}
	// header contains ANSI from lipgloss styles; compare visible columns, not bytes.
	if lipgloss.Width(before) < lipgloss.Width(header)/2 {
		t.Errorf("header = %q, want the branch right-aligned, not near the start", header)
	}
	if !strings.Contains(header, "3") {
		t.Errorf("header = %q, want the changed-file count shown", header)
	}
}

// TestHeaderShowsDeletedUntrackedAndAgents: the header's right side leads
// with the agent total (before the branch/change stats, since it's the one
// figure that doesn't change with bead selection) and shows the deleted and
// untracked file counts too — but no per-program breakdown, and no path
// (the working-directory path never appears in the header; see
// TestHeaderLeftIsAlwaysBrand).
func TestHeaderShowsDeletedUntrackedAndAgents(t *testing.T) {
	deps := testDeps()
	deps.Dir = "/tmp/baron-test"
	m := New(context.Background(), deps)
	next, _ := m.Update(headerStatsMsg{stats: HeaderStats{
		ShortPath:      "~/Projects/baron",
		Branch:         "feature/x",
		ChangedFiles:   5,
		DeletedFiles:   2,
		UntrackedFiles: 3,
		Agents:         3,
	}})
	m = asModel(next)
	m.width = 200
	header, _, _ := strings.Cut(m.View().Content, "\n")
	for _, want := range []string{"5 changed", "2 deleted", "3 untracked", "+0 -0", "3 agents"} {
		if !strings.Contains(header, want) {
			t.Errorf("header = %q, want %q shown", header, want)
		}
	}
	for _, notWant := range []string{"claude", "opencode", "~/Projects/baron"} {
		if strings.Contains(header, notWant) {
			t.Errorf("header = %q, want no per-program breakdown or path (%q must not appear)", header, notWant)
		}
	}
	if agentsIdx, branchIdx := strings.Index(header, "3 agents"), strings.Index(header, "feature/x"); agentsIdx < 0 || agentsIdx > branchIdx {
		t.Errorf("header = %q, want the agent count before the branch", header)
	}
}

// TestHeaderLeftIsAlwaysBrand: the header's left side is always "BARON" +
// version, regardless of Deps.Dir or the fetched ShortPath — the working
// directory is not shown anywhere in the header.
func TestHeaderLeftIsAlwaysBrand(t *testing.T) {
	deps := testDeps()
	deps.Dir = "/some/deeply/nested/project/path"
	deps.Version = "9.9.9"
	m := New(context.Background(), deps)
	next, _ := m.Update(headerStatsMsg{stats: HeaderStats{ShortPath: "~/some/deeply/nested/project/path"}})
	m = asModel(next)
	m.width = 200
	header, _, _ := strings.Cut(m.View().Content, "\n")
	if !strings.HasPrefix(header, "BARON v9.9.9") {
		t.Fatalf("header = %q, want it to start with \"BARON v9.9.9\"", header)
	}
	if strings.Contains(header, "nested") || strings.Contains(header, "project/path") {
		t.Errorf("header = %q, want no path fragment shown", header)
	}
}
