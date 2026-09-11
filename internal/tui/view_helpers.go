package tui

import (
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

func (m Model) listPaneRows() int {
	if m.height <= 0 {
		return 1 << 30
	}
	return max(1, m.height-8)
}

// trunc shortens s to w cells with an ellipsis; w <= 0 returns s unchanged.
// trunc shortens s to w cells with an ellipsis, ANSI escape sequences kept
// intact (a wide rune that would overflow the budget is dropped whole and
// the truncated style is reset before the ellipsis) — charmbracelet/x/ansi
// already solves exactly this, so BARON no longer hand-walks escape bytes
// itself. w <= 0 returns s unchanged.
func (m Model) trunc(w int, s string) string {
	if w <= 0 {
		return s
	}
	return xansi.Truncate(s, w, "…")
}

// truncWords shortens s to w cells at a word boundary (mid-word cuts read
// as "(Wo…"); a single word longer than w is hard-cut instead.
func (m Model) truncWords(w int, s string) string {
	if w <= 0 || xansi.StringWidth(s) <= w {
		return s
	}
	var acc string
	for word := range strings.FieldsSeq(s) {
		cand := word
		if acc != "" {
			cand = acc + " " + word
		}
		if xansi.StringWidth(cand) > w-2 {
			break
		}
		acc = cand
	}
	if acc == "" {
		return xansi.Truncate(s, w-1, "…")
	}
	return xansi.Truncate(acc+" …", w, "")
}

// splitPaneRows is the row budget for the split-pane's left box body: the
// whole View is exactly m.height (header 1 + blank 1 + box + blank 1 +
// footer 1), the box is 2 borders + 1 tab bar, so the rows inside are
// m.height-4-3. Overcounting here is what made the header scroll off the
// top of the alt screen.
func (m Model) splitPaneRows() int {
	if m.height <= 0 {
		return 1 << 30
	}
	// The box holds splitBoxInnerRows rows; the tab bar and sub-tab bar take
	// the first two, so the list gets the rest. Deriving it from the box
	// rather than from height directly is what keeps the two in step — they
	// were two apart before, which left the dashboard short of the terminal's
	// last rows.
	return max(1, m.splitBoxInnerRows()-2)
}

// detailPaneHeight is the visible content zone height; unknown sizes
// (height <= 0, as in tests) render everything.
func (m Model) detailPaneHeight() int {
	if m.height <= 0 {
		return 1 << 30
	}
	return max(1, m.height-8)
}

// sectionHead renders a right-pane section header as a full-width bar so
// regions (Acceptance/Description/Comments/Audit groups) read as distinct
// blocks instead of intermingled paragraphs.
func (m Model) sectionHead(title string) string {
	return m.styles.SectionHead.Render(title)
}

// detailContent renders the active tab's full content (pre-windowing),
// wrapping prose to the pane's inner width w.
