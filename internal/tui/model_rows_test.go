package tui

import (
	"context"
	"testing"

	"github.com/baron-cli/baron/internal/store"
)

// TestPromptPersonaVisibleLinesAccountsForPlaceholder is a regression test:
// viewPromptPersonaList and the Personas-tab mouse click handler used to
// each independently assume one screen line per promptPersonaFlatRows()
// entry. That's false the moment the single open accordion has no cached
// activity yet — its "loading…"/"no work outputs yet" hint is an extra
// rendered line with no flat-row of its own, so it silently overflowed the
// list's row budget by one line, and every flat row after it landed one
// screen line lower than a naive start+offset click calculation expected
// (a click on a real, visible row either selected the wrong persona or, as
// this test's own math shows, fell outside the old bounds check entirely
// and did nothing).
func TestPromptPersonaVisibleLinesAccountsForPlaceholder(t *testing.T) {
	m := New(context.Background(), testDeps())
	m.personas = testPersonas() // clean-code (idx 0), qa-chromium (idx 1)
	m.promptPersonaOpenID = "clean-code"
	m.promptPersonaActivity = map[string][]store.AuditEvent{
		"clean-code": {}, // cached, empty -> "no work outputs yet" placeholder
	}

	flat := m.promptPersonaFlatRows()
	if len(flat) != 2 {
		t.Fatalf("promptPersonaFlatRows() = %d rows, want 2 (only real persona rows — the placeholder is not one)", len(flat))
	}

	// Ample budget: all 3 screen lines (clean-code, placeholder, qa-chromium) fit.
	lines := m.promptPersonaVisibleLines(0, 10)
	if len(lines) != 3 {
		t.Fatalf("promptPersonaVisibleLines(0, 10) = %+v, want 3 lines (row, placeholder, row)", lines)
	}
	if lines[0].FlatIdx != 0 {
		t.Errorf("lines[0].FlatIdx = %d, want 0 (clean-code's own row)", lines[0].FlatIdx)
	}
	if lines[1].FlatIdx != -1 || lines[1].Placeholder != "no work outputs yet" {
		t.Errorf("lines[1] = %+v, want the non-selectable placeholder", lines[1])
	}
	// This is the crux of the bug: qa-chromium (flat index 1) renders on
	// screen line 2, not flat index 2 — there is no flat index 2, len(flat)
	// is only 2. A click handler doing `idx := start + (mouse.Y-4)` would
	// compute idx=2 here, fail the `idx < len(flat)` bounds check, and
	// silently do nothing despite qa-chromium's row being right there on
	// screen.
	if lines[2].FlatIdx != 1 {
		t.Errorf("lines[2].FlatIdx = %d, want 1 (qa-chromium, the row actually drawn on this screen line)", lines[2].FlatIdx)
	}

	// Tight budget: the row-count cap must count the placeholder line
	// itself, or the list overflows its allotted box height by one line.
	capped := m.promptPersonaVisibleLines(0, 2)
	if len(capped) != 2 {
		t.Fatalf("promptPersonaVisibleLines(0, 2) = %+v, want exactly 2 lines (budget respected)", capped)
	}
	if capped[1].FlatIdx != -1 {
		t.Errorf("capped[1] = %+v, want the placeholder still shown, not qa-chromium's row skipping ahead of it", capped[1])
	}
}
