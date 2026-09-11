package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/store"
)

// reverseOn reports whether s enables reverse video anywhere. Checked by SGR
// parameter rather than by matching a literal escape: the attribute is
// rendered through lipgloss, which merges a cell's whole style into one
// sequence and closes it with a full reset, so neither the opening nor the
// closing bytes have a fixed form worth pinning a test to.
func reverseOn(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(s) && s[j] != 'm' && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
			j++
		}
		if j >= len(s) || s[j] != 'm' {
			continue
		}
		if slices.Contains(strings.Split(s[i+2:j], ";"), "7") {
			return true
		}
	}
	return false
}

// TestCaretOverlayInvertsTheCellAtTheColumn: the caret is drawn by inverting
// one cell, and it has to be the cell the cursor is actually on.
func TestCaretOverlayInvertsTheCellAtTheColumn(t *testing.T) {
	got := caretOverlay("hello", 2, 20)
	if !reverseOn(got) {
		t.Fatalf("caretOverlay = %q, no reverse video applied", got)
	}
	if plain := stripSGR(got); plain != "hello" {
		t.Errorf("caretOverlay = %q (plain %q), want the text unchanged", got, plain)
	}
	// The inverted run must start after exactly two visible characters, i.e.
	// on the 'l' the cursor is sitting on.
	before, rest, _ := strings.Cut(got, "\x1b[")
	if before != "he" {
		t.Errorf("caret opened after %q, want after %q", before, "he")
	}
	if _, after, _ := strings.Cut(rest, "m"); !strings.HasPrefix(after, "l") {
		t.Errorf("caret wraps %q, want the single cell \"l\"", after)
	}
}

// TestCaretOverlayPastEndOfText: an insertion point at the end of what you are
// typing is the single most important place for it to be visible, and there is
// no character there to invert.
func TestCaretOverlayPastEndOfText(t *testing.T) {
	got := caretOverlay("hi", 5, 20)
	if !reverseOn(got) {
		t.Fatalf("caretOverlay = %q, want an inverted cell even where there is no character", got)
	}
	if plain := stripSGR(got); plain != "hi    " {
		t.Errorf("caretOverlay plain = %q, want the caret padded out to column 5 as a space", plain)
	}
}

// TestCaretOverlayPreservesStyling: slicing a styled line by byte offset cuts
// through escape sequences and corrupts everything after the cut, so the
// column arithmetic has to be ANSI-aware.
func TestCaretOverlayPreservesStyling(t *testing.T) {
	line := "\x1b[32mgreen\x1b[0m text"
	got := caretOverlay(line, 7, 20)
	if !reverseOn(got) {
		t.Fatalf("caretOverlay = %q, no caret drawn", got)
	}
	// The visible text must survive intact — no bytes eaten out of the middle
	// of an escape sequence.
	if plain := stripSGR(got); !strings.Contains(plain, "green text") {
		t.Errorf("caretOverlay = %q (plain %q), want the original text intact", got, plain)
	}
}

// TestCaretOverlayOutOfRangeIsANoOp: a cursor column past the pane's width has
// nowhere to draw, and inventing a position would be worse than none.
func TestCaretOverlayOutOfRangeIsANoOp(t *testing.T) {
	for _, x := range []int{-1, 20, 99} {
		if got := caretOverlay("hello", x, 20); got != "hello" {
			t.Errorf("caretOverlay(x=%d) = %q, want the line untouched", x, got)
		}
	}
}

// caretModel returns a Model showing a live focused session, sized, with the
// child having written text and left its cursor after it.
func caretModel(t *testing.T, focus bool) Model {
	t.Helper()
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	m.detailTab = 1
	m.detail = beads[0]
	m.liveBRN = "baron-a"
	m.termFocus = focus

	cols, rows := m.termDims()
	emu := vt.NewSafeEmulator(cols, rows)
	_, _ = emu.WriteString("\x1b[?1049h\x1b[2J\x1b[H> typing here")
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindAgent, emu: emu, focus: focus, cols: cols, rows: rows}
	return m
}

// TestFocusedTerminalShowsACaret is the user-facing bug: connecting to a bead's
// terminal with 't' gave no insertion point, so you could type with no idea
// where the characters were landing.
func TestFocusedTerminalShowsACaret(t *testing.T) {
	m := caretModel(t, true)
	out := m.viewEmbeddedTerminal(m.sessions["baron-a"], 80, 24, 0)
	if !reverseOn(out) {
		t.Errorf("a focused terminal rendered no caret:\n%q", out)
	}
}

// TestUnfocusedTerminalShowsNoCaret: an unfocused pane is not taking your
// keystrokes, so showing an insertion point there says the opposite of what is
// true — and with several beads' panes around, several caret would be worse.
func TestUnfocusedTerminalShowsNoCaret(t *testing.T) {
	m := caretModel(t, false)
	out := m.viewEmbeddedTerminal(m.sessions["baron-a"], 80, 24, 0)
	if reverseOn(out) {
		t.Errorf("an unfocused terminal drew a caret:\n%q", out)
	}
}

// TestHiddenCursorSuppressesTheCaret: a TUI hides the cursor while repainting
// and shows it when it wants input. Drawing one regardless invents UI the app
// deliberately turned off.
func TestHiddenCursorSuppressesTheCaret(t *testing.T) {
	m := caretModel(t, true)
	tm := m.sessions["baron-a"]
	tm.feed([]byte("\x1b[?25l")) // DECTCEM off, through the pump's own path
	if out := m.viewEmbeddedTerminal(tm, 80, 24, 0); reverseOn(out) {
		t.Errorf("caret drawn while the child had the cursor hidden:\n%q", out)
	}
	tm.feed([]byte("\x1b[?25h"))
	if out := m.viewEmbeddedTerminal(tm, 80, 24, 0); !reverseOn(out) {
		t.Errorf("caret did not come back when the child showed the cursor again:\n%q", out)
	}
}

// stripSGR removes SGR sequences so the visible characters can be compared.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
