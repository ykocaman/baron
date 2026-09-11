package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/store"
)

// layoutModel returns a dashboard Model at the given size with a live
// tmux-backed session, so every detail tab has something real to render.
func layoutModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusWorking, Assignee: "opencode"},
		{BRN: "baron-b", Title: "Beta", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = w, h
	m.detail = beads[0]
	m.liveBRN = "baron-a"

	cols, rows := m.termDims()
	emu := vt.NewSafeEmulator(cols, rows)
	_, _ = emu.WriteString("agent output line\r\n")
	m.sessions["baron-a"] = &agentTerminal{
		brn: "baron-a", kind: kindAgent, emu: emu, tmuxWindow: "baron-a", cols: cols, rows: rows,
	}
	return m
}

// viewLines renders the dashboard and splits it into screen rows.
func viewLines(m Model) []string {
	return strings.Split(m.viewString(), "\n")
}

// TestDashboardHeightIsConstantAcrossStates is the fix for the layout jitter
// reported once scrolling started working: "sol ve sag pencere cerceveleri ve
// en alttaki shell ile kısayol kısmındakiler de 1 satır yukarı asagı oynayıp
// duruyorlar" — the panes and the footer drifted up and down by a row.
//
// The cause was each box sizing itself to its content: the left list padded to
// a fixed height, while the right pane appended padding and then stripped it
// off again with TrimRight. So anything that changed the detail content by a
// line — switching tabs, an agent printing a shorter frame, or the scrollback
// marker row added when paging back — moved the right box's bottom edge, the
// footer with it. The dashboard's geometry must depend on the terminal size
// and nothing else.
func TestDashboardHeightIsConstantAcrossStates(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {120, 40}, {80, 24}, {200, 60}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			base := layoutModel(t, w, h)
			want := len(viewLines(base))
			if want != h {
				t.Fatalf("dashboard rendered %d rows in a %d-row terminal", want, h)
			}

			states := map[string]func(m Model) Model{
				"overview tab": func(m Model) Model { m.detailTab = 0; return m },
				"terminal tab": func(m Model) Model { m.detailTab = 1; return m },
				"diff tab":     func(m Model) Model { m.detailTab = 2; return m },
				"audit tab":    func(m Model) Model { m.detailTab = 3; return m },
				"no selection": func(m Model) Model { m.detail = store.Bead{}; return m },
				"comments expanded": func(m Model) Model {
					m.detailTab, m.commentsExpanded = 0, true
					return m
				},
				"scrolled into scrollback": func(m Model) Model {
					m.detailTab = 1
					t := m.sessions["baron-a"]
					t.history = make([]string, 400)
					for i := range t.history {
						t.history[i] = fmt.Sprintf("history line %d", i)
					}
					m.paneFor("baron-a", kindAgent).offset = 50
					return m
				},
				"scrolled to the oldest line": func(m Model) Model {
					m.detailTab = 1
					t := m.sessions["baron-a"]
					t.history = []string{"only", "a", "few", "lines"}
					m.paneFor("baron-a", kindAgent).offset = 3
					return m
				},
				"long agent frame": func(m Model) Model {
					m.detailTab = 1
					tm := m.sessions["baron-a"]
					for i := range 200 {
						tm.feed(fmt.Appendf(nil, "frame line %d\r\n", i))
					}
					return m
				},
			}
			for name, mutate := range states {
				got := viewLines(mutate(layoutModel(t, w, h)))
				if len(got) != want {
					t.Errorf("%s: %d rows, want %d — the layout shifts with content", name, len(got), want)
				}
			}
		})
	}
}

// TestFooterIsAlwaysTheLastRow: the shortcut/shell row belongs on the
// terminal's final line in every state. It drifting upward is the visible half
// of the same bug.
func TestFooterIsAlwaysTheLastRow(t *testing.T) {
	cases := map[string]func(m Model) Model{
		"terminal tab": func(m Model) Model { m.detailTab = 1; return m },
		"diff tab":     func(m Model) Model { m.detailTab = 2; return m },
		"no selection": func(m Model) Model { m.detail = store.Bead{}; return m },
		"scrolled into scrollback": func(m Model) Model {
			m.detailTab = 1
			t := m.sessions["baron-a"]
			t.history = []string{"a", "b", "c"}
			m.paneFor("baron-a", kindAgent).offset = 2
			return m
		},
	}
	for name, mutate := range cases {
		m := mutate(layoutModel(t, 110, 32))
		lines := viewLines(m)
		last := lines[len(lines)-1]
		if !strings.Contains(last, "quit") {
			t.Errorf("%s: last row is %q, want the footer with its shortcuts", name, strings.TrimSpace(last))
		}
		// And nothing may sit below it.
		if strings.TrimSpace(strings.Join(lines[len(lines):], "")) != "" {
			t.Errorf("%s: content rendered below the footer", name)
		}
	}
}

// TestBothPanesEndOnTheSameRow: the two boxes are drawn side by side, so a
// difference in their heights shows up directly as a ragged bottom border.
func TestBothPanesEndOnTheSameRow(t *testing.T) {
	m := layoutModel(t, 120, 34)
	m.detailTab = 1
	left := strings.Split(m.viewSplitLeft(splitLeftW(m)), "\n")
	right := strings.Split(m.viewSplitRight(splitRightW(m)), "\n")
	if len(left) != len(right) {
		t.Errorf("left pane is %d rows, right pane is %d — their bottom borders cannot line up", len(left), len(right))
	}
}

func splitLeftW(m Model) int  { l, _ := splitPaneWidths(m.width); return l }
func splitRightW(m Model) int { _, r := splitPaneWidths(m.width); return r }

// The following tests pin the narrow-terminal behavior: every pane split,
// toast, and overlay must fit inside the terminal it is rendered in, no
// matter how few columns are available.

// TestSplitPaneWidthsNoOverflow: left + right + 1 (the join gap) may never
// exceed the total width — on a 40-column terminal the dashboard used to
// render 45 columns wide.
func TestSplitPaneWidthsNoOverflow(t *testing.T) {
	for _, total := range []int{40, 45, 50, 60, 80, 100, 120} {
		left, right := splitPaneWidths(total)
		if left < 0 || right < 0 {
			t.Errorf("splitPaneWidths(%d) = %d, %d — negative widths", total, left, right)
		}
		if left+right+1 > total {
			t.Errorf("splitPaneWidths(%d) = %d + %d + 1 gap overflows by %d", total, left, right, left+right+1-total)
		}
	}
}

// TestPromptPaneWidthsNoOverflow: same invariant for Prompt Mode's two
// panes, which overflowed even worse (30 + 24 + 1 > 40).
func TestPromptPaneWidthsNoOverflow(t *testing.T) {
	for _, total := range []int{40, 45, 50, 60, 80, 100, 120} {
		left, right := promptPaneWidths(total)
		if left < 0 || right < 0 {
			t.Errorf("promptPaneWidths(%d) = %d, %d — negative widths", total, left, right)
		}
		if left+right+1 > total {
			t.Errorf("promptPaneWidths(%d) = %d + %d + 1 gap overflows by %d", total, left, right, left+right+1-total)
		}
	}
}

// TestToastFitsNarrowTerminal: a status notice wrapped for a wide terminal
// spilled past the right edge on anything under ~28 columns.
func TestToastFitsNarrowTerminal(t *testing.T) {
	m := New(context.Background(), testDeps())
	m.width, m.height = 15, 20
	m.statusMsg = strings.Repeat("x", 100)
	for i, line := range strings.Split(m.viewToast(), "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Fatalf("toast line %d is %d cols in a %d-col terminal: %q", i, w, m.width, line)
		}
	}
}

// TestOverlayBoxFitsNarrowTerminal: OverlayBox was hardcoded to Width(76),
// so every modal overflowed on terminals narrower than that.
func TestOverlayBoxFitsNarrowTerminal(t *testing.T) {
	m := New(context.Background(), testDeps())
	m.width, m.height = 30, 20
	m.statusForm = nil // force viewStatusMenu's non-form fallback
	lines := strings.Split(m.viewStatusMenu(), "\n")
	for i, line := range lines {
		if w := lipgloss.Width(line); w > m.width {
			t.Fatalf("overlay line %d is %d cols in a %d-col terminal: %q", i, w, m.width, line)
		}
	}
}

// TestWrapDoesNotNoOpOnNarrow: wrap() refused to wrap below width 20, so
// narrow panes rendered unbroken prose.
func TestWrapDoesNotNoOpOnNarrow(t *testing.T) {
	var m Model
	got := m.wrap(5, "hello world")
	if !strings.Contains(got, "\n") {
		t.Fatalf("wrap(5, 'hello world') returned a single line: %q", got)
	}
	for i, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > 5 {
			t.Fatalf("wrapped line %d is %d cells (> 5): %q", i, w, line)
		}
	}
}
