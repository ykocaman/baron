package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"

	"github.com/baron-cli/baron/internal/store"
)

func (m Model) panesFor(kind termKind) map[string]*agentPane {
	if kind == kindDiff {
		return m.diffPanes
	}
	return m.panes
}

// paneFor returns brn's pane for kind, creating an empty one on first use.
func (m Model) paneFor(brn string, kind termKind) *agentPane {
	panes := m.panesFor(kind)
	p := panes[brn]
	if p == nil {
		p = &agentPane{vp: viewport.New()}
		panes[brn] = p
	}
	return p
}

// agentPaneFor is paneFor for the Agent tab specifically — a shorthand
// tests use repeatedly; production code passes kindAgent to paneFor
// directly since it also has msg.kind/kind in scope.
func (m Model) agentPaneFor(brn string) *agentPane {
	return m.paneFor(brn, kindAgent)
}

// linesLen returns kind's live-session frame height for brn, falling back to
// the persisted pane's line count when no session is live.
func (m Model) linesLen(brn string, kind termKind) int {
	if t := m.sessionsFor(kind)[brn]; t != nil {
		return len(strings.Split(strings.TrimRight(t.emu.Render(), "\n"), "\n"))
	}
	return len(m.contentFor(brn, kind))
}

// contentFor returns brn's persisted run summary as renderable lines for
// kind (agent only in practice — see its doc note below). A nil/empty pane
// yields nil, which the views treat as "no live output yet".
//
// A Diff pane never gets a summary populated (diff sessions aren't
// persisted — see spawnDiffTerminal), so this is always nil for kindDiff
// today; kept kind-generic anyway so a future persisted kind doesn't need a
// second copy.
func (m Model) contentFor(brn string, kind termKind) []string {
	p := m.panesFor(kind)[brn]
	if p == nil || len(p.summary) == 0 {
		return nil
	}
	return p.summary
}

// syncPaneContent feeds lines into p.vp, defaulting the very first
// population to the bottom (matching agentPane.offset's old "0 = bottom"
// default — the tail is what you want to see first for a finished run)
// without disturbing the user's position on every later call: View() runs
// every frame, and SetContentLines alone only touches scroll position when
// the current offset is now out of range (content shrank under it), so
// calling this repeatedly with unchanged content is a no-op scroll-wise.
func syncPaneContent(p *agentPane, lines []string) {
	hadContent := len(p.vp.GetContent()) > 0
	p.vp.SetContentLines(lines)
	if !hadContent && len(lines) > 0 {
		p.vp.GotoBottom()
	}
}

// syncDetailVP refreshes m.detailVP's dimensions and content from the
// active tab's detailContent, so scroll clamping (PageUp/PageDown/
// ScrollUp/Down, driven from Update before the next View runs) always sees
// up-to-date bounds even if the tab's data changed since the last render —
// the same on-demand recompute detailMaxOffset used to do. Overview/Audit
// only: the Terminal/Diff tabs never call this (see detailVP's doc
// comment).
func (m *Model) syncDetailVP() {
	_, right := splitPaneWidths(m.width)
	if m.width <= 0 {
		// Unknown size (as in tests): splitPaneWidths now correctly collapses
		// toward 0 for absurd totals, but this path must keep the historical
		// unsized wrap budget — vpSize widens past paneW here anyway, so the
		// value only feeds wrap, never clips.
		right = 20
	}
	lines := strings.Split(m.detailContent(max(10, right-4)), "\n")
	w, h := vpSize(max(10, right-4), m.detailPaneHeight(), m.height <= 0, lines)
	m.detailVP.SetWidth(w)
	m.detailVP.SetHeight(h)
	m.detailVP.SetContentLines(lines)
}

// boardColumns defines the split-pane list's tabs, left to right — grouped
// by what the tab tells you to do about a bead, not by raw status:
//
//   - Backlog: not started, for any reason — open, assigned, or blocked
//     (waiting on another bead's dependency, not on a human: it unblocks
//     itself once that bead closes, so it belongs with "hasn't started"
//     rather than "needs you").
//   - Active: something is happening — an agent is working, its result is
//     being validated, or it's about to retry. Nothing for you to do yet.
//   - Needs You: stalled on a human specifically — human_queue (the agent
//     asked a question) or mergable (gate passed, awaiting approval). Two different
//     reasons, one tab: everything that will NOT move forward without a
//     person lives here, so it's the one tab worth checking regularly.
//   - Done: closed, merged, cancelled.
//
// An earlier version filed blocked/mergable under "Working", which is
// misleading (nothing is actively running for either), and gave human_queue
// its own tab even though it's the same "needs a human" kind of stall as
// mergable — splitting one concern across two places while merging two
// unrelated ones into another. Every status still shows up somewhere,
// grouped by sub-header within its tab (see bucketLabel) — nothing here
// trims what's visible, only how it's organized.
var boardColumns = []struct {
	name     string
	statuses []store.BeadStatus
}{
	{"Backlog", []store.BeadStatus{store.BeadStatusOpen, store.BeadStatusAssigned, store.BeadStatusBlocked}},
	{"Active", []store.BeadStatus{store.BeadStatusWorking, store.BeadStatusValidating, store.BeadStatusRetry}},
	{"Needs You", []store.BeadStatus{store.BeadStatusHumanQueue, store.BeadStatusMergable}},
	{"Done", []store.BeadStatus{store.BeadStatusMerged, store.BeadStatusClosed, store.BeadStatusCancelled}},
}

// searchMatches reports whether a bead passes the active / search query.
// When the query is a valid regexp (compiled into m.searchRegex) it is tested
// case-insensitively against the BRN and title; otherwise plain
// case-insensitive substring matching is used so simple queries always work.
