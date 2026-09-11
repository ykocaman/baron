package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func vpSize(paneW, paneH int, unsized bool, lines []string) (w, h int) {
	w, h = paneW, min(paneH, len(lines))
	if unsized {
		longest := 0
		for _, ln := range lines {
			if lw := xansi.StringWidth(ln); lw > longest {
				longest = lw
			}
		}
		w = max(paneW, longest)
	}
	return w, h
}

// viewSessionPane renders kind's live embedded terminal for the active
// bead — the Agent tab (kindAgent) or the Diff tab (kindDiff) — falling
// back to a persisted summary (agent only; a diff session is never
// persisted, see spawnDiffTerminal) or an empty-state hint when nothing is
// live. This is the one rendering path both tabs share, kept in sync with
// resizeSessions'/termDims' sizing and paneFor's per-kind scroll offset.
func (m Model) viewSessionPane(kind termKind, w int) string {
	innerW := max(10, w-4)
	offset := 0
	brn := m.liveBRN
	if brn == "" {
		brn = string(m.detail.BRN)
	}
	if p := m.panesFor(kind)[brn]; p != nil {
		offset = p.offset
	}
	if t := m.sessionsFor(kind)[brn]; t != nil {
		return m.viewEmbeddedTerminal(t, innerW, m.detailPaneHeight(), offset)
	}
	// No live session: show the persisted run summary (restored from disk,
	// agent kind only) via a viewport (agentPane.vp) when one exists, else
	// the empty-state hint.
	var inner strings.Builder
	content := m.contentFor(brn, kind)
	p := m.panesFor(kind)[brn]
	if len(content) == 0 {
		inner.WriteString(m.styles.EmptyState.Render(xansi.Truncate(sessionEmptyHint(kind, brn), innerW, "…")))
	} else {
		inner.WriteString(m.renderPersistedPane(p, content, innerW))
	}
	if p != nil && p.err != "" {
		if inner.Len() > 0 {
			inner.WriteString("\n")
		}
		inner.WriteString(m.styles.Muted.Render(p.err))
	}
	return strings.TrimRight(inner.String(), "\n")
}

// sessionEmptyHint is viewSessionPane's empty-state hint: shown when brn has
// no live session and nothing persisted for kind yet.
func sessionEmptyHint(kind termKind, brn string) string {
	verb, generic := "no live output for %s yet — press r to run", "no live output yet — press r to run"
	if kind == kindDiff {
		verb, generic = "no diff view for %s yet — press t to open it", "no diff view yet — press t to open it"
	}
	if brn == "" {
		return generic
	}
	return fmt.Sprintf(verb, brn)
}

// renderPersistedPane renders content's persisted viewport — a replay
// banner first when p.replay — sized to innerW and the detail pane height.
func (m Model) renderPersistedPane(p *agentPane, content []string, innerW int) string {
	var out strings.Builder
	if p != nil && p.replay {
		out.WriteString(m.styles.Muted.Render("■ replay — pane gone, showing persisted log"))
		out.WriteString("\n")
	}
	if p != nil {
		w, h := vpSize(innerW, m.detailPaneHeight(), m.height <= 0, content)
		p.vp.SetWidth(w)
		p.vp.SetHeight(h)
		syncPaneContent(p, content)
		out.WriteString(p.vp.View())
	}
	return out.String()
}

// caretOverlay draws the text cursor at visible column x of an
// already-ANSI-encoded line, by inverting that one cell (SGR 7, reverse
// video).
//
// The emulator tracks where the child put its cursor, but Render() paints
// cells only — the caret is terminal state, not screen content, so it never
// appears in the frame. That left the embedded terminal with no insertion
// point at all: you could type into a focused agent and have no idea where the
// characters were going. The fix has to be drawn on rather than delegated:
// handing the position to bubbletea's own cursor was tried first and it
// fragmented the real rendering (caught by the e2e suite), because there is
// one hardware cursor and the TUI around this pane is already using it.
//
// Reverse video is the right primitive here precisely because it does not pick
// a colour: it swaps whatever foreground and background that cell already has,
// so it stays visible on any theme and under NO_COLOR.
//
// Column arithmetic goes through ansi.Cut, which counts visible cells rather
// than bytes — slicing a styled line by byte offset would cut through an
// escape sequence and corrupt every cell after it.
func caretOverlay(line string, x, width int) string {
	if x < 0 || x >= width {
		return line
	}
	left := xansi.Cut(line, 0, x)
	cell := xansi.Cut(line, x, x+1)
	right := xansi.Cut(line, x+1, width)
	if xansi.StringWidth(cell) == 0 {
		// Past the end of the painted text: the caret sits on empty space,
		// which still has to be shown or the insertion point disappears
		// exactly where it matters most — at the end of what you are typing.
		cell = " "
		// Everything between the line's end and the caret needs padding, or
		// the inverted cell lands at the wrong column.
		if pad := x - xansi.StringWidth(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
	}
	return left + caretStyle.Render(cell) + right
}

// caretStyle is the caret's reverse-video attribute, expressed as a lipgloss
// style rather than raw SGR bytes. The renderer parses the view into cells and
// re-emits each cell's style as one merged sequence; hand-written escapes do
// not survive that round trip intact, while a lipgloss style does — it is the
// same mechanism the bead list's own selection highlight goes through.
var caretStyle = lipgloss.NewStyle().Reverse(true)

// viewCapturedHistory renders a window of the host's captured pane scrollback,
// with a marker so it is obvious the pane is showing history rather than the
// live agent — without it, a scrolled-back pane that has stopped following the
// agent looks exactly like an agent that has stopped producing output.
func (m Model) viewCapturedHistory(t *agentTerminal, innerW, innerH, offset int) string {
	lines := t.history
	end := clampInt(len(lines)-offset, len(lines))
	start := max(0, end-historyWindow(innerH))
	var b strings.Builder
	marker := fmt.Sprintf("↑ scrollback %d/%d — scroll down to follow the agent", max(0, len(lines)-end), len(lines))
	if xansi.StringWidth(marker) > innerW {
		marker = fmt.Sprintf("↑ scrollback %d/%d", max(0, len(lines)-end), len(lines))
	}
	b.WriteString(m.styles.Muted.Render(m.trunc(innerW, marker)))
	for _, ln := range lines[start:end] {
		b.WriteString("\n")
		b.WriteString(m.trunc(innerW, ln))
		// Close any styling the captured line left open. tmux's -e output
		// carries the text's real colours (without it the pane went
		// monochrome the moment you scrolled back), but a line that ends
		// mid-attribute would otherwise bleed into everything drawn after it.
		b.WriteString("\x1b[m")
	}
	return b.String()
}

// viewEmbeddedTerminal renders one session's emulator frame inside innerW x
// innerH (the emulator itself is sized to match — see termDims — so the
// frame is only trimmed defensively). ANSI styling is kept. A focus change
// surfaces as a one-shot notice via the toast (see focusNoticeText and
// updateSplitKey's "t"/"z" cases), never a static line here — a permanent
// banner ate a row for as long as focus lasted (and shifted the frame by
// one line) for no benefit once the toast already says it. A finished
// session shows its exit result line. Callers must pass the budget actually
// available in their context (the zoomed full-screen frame and the
// split-pane's cramped detail pane are not the same number) — an earlier
// version read m.detailPaneHeight() directly regardless of caller, which
// silently clipped the zoomed frame's bottom rows (where an agent's own
// prompt/input box usually lives) down to the split pane's much smaller
// budget.
func (m Model) viewEmbeddedTerminal(t *agentTerminal, innerW, innerH, offset int) string {
	// Scrolled back into a tmux-backed session's captured pane history: that
	// text is the backlog (see paneHistoryCmd), and the emulator — a mirror of
	// the *current* pane — cannot show it. At offset 0 the live frame below
	// takes over again, so the pane returns to following the agent.
	if offset > 0 && t.tmuxWindow != "" && len(t.history) > 0 {
		return m.viewCapturedHistory(t, innerW, innerH, offset)
	}
	// Only the emulator's current frame is drawn — its scrollback is not
	// consulted at all. Nothing scrolls it any more: a session's backlog comes
	// from tmux (see paneHistoryCmd) or from the child itself, so the live
	// view is always the newest screen. That also keeps this off the render
	// hot path, where ANSI-encoding a long scrollback 24 times a second used
	// to make typing lag grow with session age.
	lines := strings.Split(strings.TrimRight(t.emu.Render(), "\n"), "\n")
	start, end := 0, len(lines)
	if m.height > 0 && len(lines) > innerH {
		start = len(lines) - innerH
	}

	// The caret's absolute row, so the loop below can tell which rendered
	// line (if any) it falls on. Only meaningful on the current screen —
	// scrollback is history, and history has no cursor.
	cur := t.emu.CursorPosition()
	curRow, showCaret := cur.Y, t.focus && !t.cursorHidden.Load()

	var b strings.Builder
	for i := start; i < end; i++ {
		ln := m.trunc(innerW, lines[i])
		if showCaret && i == curRow {
			ln = caretOverlay(ln, cur.X, innerW)
		}
		b.WriteString(ln)
		b.WriteString("\n")
	}
	out := strings.TrimRight(b.String(), "\n")
	if t.done {
		msg := "agent exited"
		if t.exitErr != nil {
			msg += ": " + t.exitErr.Error()
		}
		out += "\n" + m.styles.Muted.Render(msg)
	}
	return out
}

// viewHelpV2 renders the shortcut reference, scrollable on tall screens.
// Generated from the bindings registry (bindings.go) — hand-written help
// drifts from the actual keymap, generated help can't.
// helpIndent is the key column's width in viewHelpV2 ("  " + 14-wide key +
// " "): wrapped continuation lines re-indent to this so a long help string
// (e.g. the manual-drive binding's) reads as one aligned paragraph instead
// of running off the pane or dropping its tail.
const helpIndent = 17

// viewPromptMode renders Prompt Mode (uppercase P, screenCrew) —
// docs/PRD/crew-mode.md's v7 redesign: a 3-pane, full-height mode swap.
// Left: a tabbed, live tmux-attached agent-CLI chat, one tab per active
// agent, running bd-scoped in the main checkout at its current branch (not
// a bead worktree) — the "edit via prompt" half of the redesign. Right,
// stacked: a list (Personas or Beads, m.promptRightTab, switched with
// shift+left/right) over that list selection's detail — the "edit via
// manual intervention" half. Unlike v6, this never mirrors Board Mode's
// own split pane or list/cursor state; the Beads-tab list/detail are
// Prompt-Mode-owned (own cursor/scroll), and the detail pane's Overview
// rendering reuses detailContent(w) itself (see viewPromptBeadDetail).
