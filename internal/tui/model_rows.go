package tui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

type splitRow struct {
	bead  store.Bead
	epic  bool
	depth int // cells of leading indent, 0 = top level
}

// parentBRN returns the parent BRN of a child bead: the bead's explicit
// parent field, else its dotted BRN suffix (e.g. "baron-tso.1" → "baron-tso"),
// else "" when the bead has no parent.
func parentBRN(b store.Bead) string {
	if b.Parent != "" {
		return b.Parent
	}
	i := strings.LastIndex(string(b.BRN), ".")
	if i <= 0 {
		return ""
	}
	return string(b.BRN)[:i]
}

// brnNum returns the numeric suffix of a bead BRN (e.g. "baron-tso.12" → 12)
// for natural ordering of siblings under the same parent.
func brnNum(b store.Bead) int {
	i := strings.LastIndex(string(b.BRN), ".")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(string(b.BRN)[i+1:])
	return n
}

// effectiveStatus is b's status for board bucketing and display: bd has no
// literal "assigned" status (see store.Bead.DomainState) — it only ever
// sets the assignee field and leaves status "open" — so an open bead with a
// model assigned would otherwise sit in the Open bucket forever, never
// moving to Assigned when you assign it. Reading the derived domain state
// instead of the raw field fixes that; store.BeadStatus and domain.BeadState
// share the same string vocabulary by construction, so the conversion is
// exact, not a guess.
func effectiveStatus(b store.Bead) store.BeadStatus {
	return store.BeadStatus(b.DomainState())
}

// mergeBaseFor returns the branch b's own branch merges into: its parent's
// branch for a hierarchical child (bd's dotted-id or explicit Parent link —
// see store.ParentOf), so a subtree integrates into its parent before that
// parent ever reaches the configured base branch; "" for anything without a
// parent — deliberately not the configured base branch itself, since this
// return value also feeds Worktrees.Create's own parentBranch param, whose
// "" case has smarter fallback logic (startPoint) than just hardcoding the
// base branch's name would give it (e.g. the very first bead, before that
// branch exists locally at all). A caller that instead needs a concrete
// branch name to actually diff or display against wants mergeBaseBranch.
func mergeBaseFor(b store.Bead, all []store.Bead) string {
	if parent, ok := store.ParentOf(b, all); ok {
		return domain.BranchName(parent.IssueType, parent.ID)
	}
	return ""
}

// mergeBaseBranch resolves the concrete branch b's own branch merges into —
// mergeBaseFor's parent branch for a hierarchical child, else the project's
// own configured base branch. Unlike mergeBaseFor alone, this is always a
// real branch name, suitable for display (mergeTargetForBead) or as an
// actual diff target (spawnDiffTerminal), never "".
func mergeBaseBranch(b store.Bead, all []store.Bead, projectBase string) string {
	if base := mergeBaseFor(b, all); base != "" {
		return base
	}
	return projectBase
}

// mergeTargetForBead derives a bead's merge target — branch name and base
// (always computable locally, no fetch needed) — from the bead itself, its
// parent chain, and the configured base branch (see mergeBaseBranch).
func (m Model) mergeTargetForBead(b store.Bead) mergeTarget {
	return mergeTarget{
		BRN:    string(b.BRN),
		Title:  b.Title,
		Branch: domain.BranchName(b.IssueType, b.ID),
		Base:   mergeBaseBranch(b, m.beads, m.deps.BaseBranch),
	}
}

// splitRows builds the tree rows for one tab of the split-pane list. A bead
// with a parent renders under that parent wherever the parent lands — the
// parent's status decides the tab, so an epic always shows its full child
// tree (children keep their own status glyph but never vanish into another
// column). Unparented beads keep their own status-based column; beads whose
// parent is missing (orphans) fall back to top level.
func (m Model) splitRows(tab int) []splitRow {
	statuses := boardColumns[tab].statuses
	children := map[string][]store.Bead{}
	var roots []store.Bead
	for _, b := range m.beads {
		if p := parentBRN(b); p != "" {
			if _, ok := m.findBeadByBRN(domain.BRN(p)); ok {
				children[p] = append(children[p], b)
				continue
			}
		}
		roots = append(roots, b)
	}
	for p, kids := range children {
		sort.SliceStable(kids, func(i, j int) bool { return brnNum(kids[i]) < brnNum(kids[j]) })
		children[p] = kids
	}
	var rows []splitRow
	var walk func(b store.Bead, depth int)
	walk = func(b store.Bead, depth int) {
		rows = append(rows, splitRow{bead: b, epic: b.IssueType == "epic", depth: depth})
		for _, kid := range children[string(b.BRN)] {
			walk(kid, depth+1)
		}
	}
	// Roots are grouped by state in tab order so each tab's state buckets
	// (open / assigned / working / ...) stay contiguous; the view renders
	// a muted header whenever the status changes.
	for _, st := range statuses {
		for _, b := range roots {
			if effectiveStatus(b) != st {
				continue
			}
			walk(b, 0)
		}
	}
	return rows
}

// promptBeadTreeRows builds Prompt Mode's Beads-tab list: the same tree
// shape splitRows builds for Board Mode (epic headers, children indented,
// via the same parentBRN/brnNum helpers and splitRow type, rendered with
// the same splitRowLine — pixel-identical tree presentation), but over the
// FULL bead set with no status-tab/bucket filtering, since the Beads tab
// has no tabs of its own (the redesign's "compact bead list" scope).
// Roots sort by BRN for a stable order; this is call-time only (never
// mutates m.beads), matching splitRows' own read-only contract.
func (m Model) promptBeadTreeRows() []splitRow {
	children := map[string][]store.Bead{}
	var roots []store.Bead
	for _, b := range m.beads {
		if p := parentBRN(b); p != "" {
			if _, ok := m.findBeadByBRN(domain.BRN(p)); ok {
				children[p] = append(children[p], b)
				continue
			}
		}
		roots = append(roots, b)
	}
	for p, kids := range children {
		sort.SliceStable(kids, func(i, j int) bool { return brnNum(kids[i]) < brnNum(kids[j]) })
		children[p] = kids
	}
	// Terminal-status roots (done/closed/merged/cancelled) sort after every
	// still-live one, each group by BRN — a flat BRN-only sort interleaved
	// closed work throughout the list, and unlike Board Mode's own tabs
	// this list has no status filter of its own to hide it. Caught by
	// hand: a real project with 126 done beads out of ~180 total buried
	// the 3 that actually needed attention somewhere in the middle of the
	// scroll. Terminal-ness only decides root ORDER; a child keeps sitting
	// under its own parent either way (same as splitRows' own "a child
	// never vanishes into another column" rule), so an epic already near
	// the top with a couple of merged children doesn't get split apart.
	sort.SliceStable(roots, func(i, j int) bool {
		ti, tj := store.IsTerminalStatus(roots[i].Status), store.IsTerminalStatus(roots[j].Status)
		if ti != tj {
			return !ti
		}
		return roots[i].BRN < roots[j].BRN
	})

	var rows []splitRow
	var walk func(b store.Bead, depth int)
	walk = func(b store.Bead, depth int) {
		rows = append(rows, splitRow{bead: b, epic: b.IssueType == "epic", depth: depth})
		for _, kid := range children[string(b.BRN)] {
			walk(kid, depth+1)
		}
	}
	for _, b := range roots {
		walk(b, 0)
	}
	return rows
}

// promptPersonaRow is one row in the Personas-tab's flattened, navigable
// list: either a persona itself (eventIdx -1) or one of its own open
// accordion's cached work-output events (eventIdx >= 0, indexing
// m.promptPersonaActivity[persona.ID]). An open accordion with no cached
// activity yet (or none at all) contributes no rows here — its "loading…"
// / "no work outputs yet" hint is rendered directly under the persona's own
// row (viewPromptPersonaList) without being a distinct, cursor-navigable
// entry — see promptPersonaFlatRows.
type promptPersonaRow struct {
	personaIdx int
	eventIdx   int
}

// promptPersonaFlatRows flattens m.personas with any open accordion's own
// rows inlined directly under it, into the single sequence shift+up/down
// and j/k walk over — so browsing reaches individual work-output rows too,
// not just personas (the redesign's "üzerinde ok ile gezilebilsin" ask).
// A persona's own row always lands at the same flat index as its plain
// position in m.personas: only rows belonging to ITS OWN open accordion
// are inserted directly after it, and at most one persona's accordion is
// ever open at once, so no earlier persona's rows can push a later one
// around — togglePersonaAccordion's close path relies on this invariant to
// jump the cursor back without re-walking the flattened list.
func (m Model) promptPersonaFlatRows() []promptPersonaRow {
	rows := make([]promptPersonaRow, 0, len(m.personas))
	for i, p := range m.personas {
		rows = append(rows, promptPersonaRow{personaIdx: i, eventIdx: -1})
		if p.ID != m.promptPersonaOpenID {
			continue
		}
		events, cached := m.promptPersonaActivity[p.ID]
		if !cached || len(events) == 0 {
			continue
		}
		for j := range events {
			rows = append(rows, promptPersonaRow{personaIdx: i, eventIdx: j})
		}
	}
	return rows
}

// promptPersonaLine is one on-screen line of the Personas-tab list: either
// a real, cursor-navigable flat row (FlatIdx indexing promptPersonaFlatRows,
// Placeholder empty) or a non-selectable "loading…"/"no work outputs yet"
// hint (FlatIdx -1, Placeholder set) rendered directly under an open
// persona that has no cached activity yet.
type promptPersonaLine struct {
	FlatIdx     int
	Placeholder string
}

// promptPersonaVisibleLines walks promptPersonaFlatRows starting at flat
// row start, in the exact shape viewPromptPersonaList renders (a persona's
// own row immediately followed by its placeholder line when applicable —
// see promptPersonaFlatRows' own doc comment), and returns one entry per
// on-screen line, up to maxLines of them. This is the single source both
// viewPromptPersonaList and the Personas-tab mouse click handler
// (update_mouse.go) walk, so what's rendered at a given screen row and
// what a click on that row resolves to can never drift apart — a
// placeholder line consumes a line of the row budget same as a real row,
// and shifts every later flat row down by one screen line, on both sides.
func (m Model) promptPersonaVisibleLines(start, maxLines int) []promptPersonaLine {
	flat := m.promptPersonaFlatRows()
	lines := make([]promptPersonaLine, 0, maxLines)
	for i := start; i < len(flat) && len(lines) < maxLines; i++ {
		lines = append(lines, promptPersonaLine{FlatIdx: i})
		if len(lines) >= maxLines {
			break
		}
		row := flat[i]
		if row.eventIdx != -1 {
			continue
		}
		p := m.personas[row.personaIdx]
		if p.ID != m.promptPersonaOpenID {
			continue
		}
		switch events, cached := m.promptPersonaActivity[p.ID]; {
		case !cached:
			lines = append(lines, promptPersonaLine{FlatIdx: -1, Placeholder: "loading…"})
		case len(events) == 0:
			lines = append(lines, promptPersonaLine{FlatIdx: -1, Placeholder: "no work outputs yet"})
		}
	}
	return lines
}

// bucketRows returns tab's rows narrowed to status bucket subTab (every
// bucket when subTab < 0) — the same filtering currentRows applies to the
// active tab, generalized so tab/sub-tab counts can report exactly what
// will be shown rather than a raw per-status bead count that can include
// children invisible in that tab (e.g. a closed child whose epic is still
// working stays under the Active tab, never the Done tab, per splitRows).
// Only root (depth==0) rows are filtered by status; once a root matches,
// its whole child subtree stays visible regardless of the children's own
// status.
func (m Model) bucketRows(tab, subTab int) []splitRow {
	rows := m.splitRows(tab)
	statuses := boardColumns[tab].statuses
	if subTab < 0 || subTab >= len(statuses) {
		return rows
	}
	want := statuses[subTab]
	var out []splitRow
	keep := false
	for _, r := range rows {
		if r.depth == 0 {
			keep = effectiveStatus(r.bead) == want
		}
		if keep {
			out = append(out, r)
		}
	}
	return out
}

// currentRows returns the rows the list actually shows: bucketRows for the
// active tab, narrowed to listSubTab's single status when a sub-tab filter
// is active (see subTabNext/subTabPrev) — or, while a search is active,
// searchAllRows (matches across every tab), exactly mirroring viewSplitLeft's
// own search/no-search branch.
//
// The two must stay in lockstep: this is what m.listCursor indexes into for
// every keyboard action (listSelection, selectListRow, j/k, and every
// action key that reads listSelection — r/a/m/x/s/c/y…), and it used to
// ignore m.searchQuery entirely while the view rendered searchAllRows
// instead. That divergence meant the row highlighted on screen during a
// search and the row m.listCursor actually pointed at could be two
// different beads outright — caught for real: searched for a bead, saw it
// highlighted, and the detail pane (and 'r') kept acting on a completely
// unrelated bead left over from before the search started, because
// currentRows was still indexing the unfiltered bucket underneath.
func (m Model) currentRows() []splitRow {
	if m.searchQuery != "" {
		return m.searchAllRows()
	}
	return m.bucketRows(m.listTab, m.listSubTab)
}

// tabContaining finds the tab and sub-tab whose rows hold brn, so a freshly
// created bead can be surfaced on the dashboard regardless of its status or
// parent epic's status.
func (m Model) tabContaining(brn domain.BRN) (tab, sub int, ok bool) {
	for t := range boardColumns {
		for s := range boardColumns[t].statuses {
			for _, r := range m.bucketRows(t, s) {
				if r.bead.BRN == brn {
					return t, s, true
				}
			}
		}
	}
	return 0, 0, false
}

// subTabNextFor steps the sub-tab filter forward one status bucket within the
// active tab; once the buckets are exhausted it spills into the next tab's
// first bucket. At the very last bucket of the very last tab it stops — no
// wrap-around back to the first tab.
func subTabNextFor(tab *int, subTab *int) {
	if *tab < 0 || *tab >= len(boardColumns) {
		*tab = 0
	}
	statuses := boardColumns[*tab].statuses
	if *subTab+1 < len(statuses) {
		(*subTab)++
		return
	}
	if *tab+1 >= len(boardColumns) {
		return
	}
	(*tab)++
	*subTab = 0
}

// subTabPrevFor is subTabNextFor in reverse, spilling into the previous tab's
// last bucket. At the very first bucket of the very first tab it stops —
// no wrap-around back to the last tab.
func subTabPrevFor(tab *int, subTab *int) {
	if *tab < 0 || *tab >= len(boardColumns) {
		*tab = 0
	}
	if *subTab-1 >= 0 {
		(*subTab)--
		return
	}
	if *tab-1 < 0 {
		return
	}
	(*tab)--
	*subTab = len(boardColumns[*tab].statuses) - 1
}

func (m *Model) subTabNext() {
	subTabNextFor(&m.listTab, &m.listSubTab)
}

func (m *Model) subTabPrev() {
	subTabPrevFor(&m.listTab, &m.listSubTab)
}

func (m *Model) promptSubTabNext() {
	subTabNextFor(&m.promptBeadTab, &m.promptBeadSubTab)
}

func (m *Model) promptSubTabPrev() {
	subTabPrevFor(&m.promptBeadTab, &m.promptBeadSubTab)
}

// bucketLabel renders a status-group sub-header within a board tab — this
// is what actually distinguishes, say, a blocked bead from one waiting on
// merge review inside the shared "Needs You" tab, so every status gets an
// explicit, Title Case label rather than falling back to the raw lowercase
// status string.
func bucketLabel(s store.BeadStatus) string {
	switch s {
	case store.BeadStatusOpen:
		return "Open"
	case store.BeadStatusAssigned:
		return "Assigned"
	case store.BeadStatusWorking:
		return "Working"
	case store.BeadStatusValidating:
		return "Validating"
	case store.BeadStatusRetry:
		return "Retry"
	case store.BeadStatusBlocked:
		return "Blocked"
	case store.BeadStatusMergable:
		return "Mergable"
	case store.BeadStatusHumanQueue:
		return "Human Queue"
	case store.BeadStatusMerged:
		return "Merged"
	case store.BeadStatusClosed:
		return "Closed"
	case store.BeadStatusCancelled:
		return "Cancelled"
	default:
		return string(s)
	}
}

// blockers returns the declared blocks-type dependencies of b (their beads
// when loaded, else the raw dependency id) — for a bead that is itself
// finished there is nothing blocking anymore, so it reports none.
func (m Model) blockers(b store.Bead) []store.Bead {
	if store.IsTerminalStatus(b.Status) {
		return nil
	}
	var out []store.Bead
	for _, dep := range b.Dependencies {
		if dep.Type != "blocks" || dep.DependsOnID == "" {
			continue
		}
		blk, ok := m.findBeadByBRN(domain.BRN(dep.DependsOnID))
		if !ok {
			blk = store.Bead{BRN: domain.BRN(dep.DependsOnID), Title: dep.DependsOnID}
		}
		out = append(out, blk)
	}
	return out
}

// activeBlockers returns the beads that currently block b: blocks-type
// dependency links whose dependent and blocker are both still active.
func (m Model) activeBlockers(b store.Bead) []store.Bead {
	out := m.blockers(b)
	live := out[:0]
	for _, blk := range out {
		if !store.IsTerminalStatus(blk.Status) {
			live = append(live, blk)
		}
	}
	return live
}

// lastAuditReason returns the most recent audit event's detail for the
// currently loaded bead (m.auditEvents, already fetched for the Audit tab —
// no separate request needed) — the "why" behind a retry or human_queue
// status: a failure reason ("agent failed [claude: API Error: 529
// Overloaded]"), a retry-budget-exhausted message, or the plain status
// transition a human-triggered stop (see the "x" key) recorded. Empty when
// nothing's been recorded yet.
func (m Model) lastAuditReason() string {
	if len(m.auditEvents) == 0 {
		return ""
	}
	latest := m.auditEvents[0]
	for _, e := range m.auditEvents[1:] {
		if e.Time.After(latest.Time) {
			latest = e
		}
	}
	return latest.Detail
}

// listSelection returns the bead under the split-pane cursor.
func (m Model) listSelection() (store.Bead, bool) {
	rows := m.currentRows()
	if m.listCursor < 0 || m.listCursor >= len(rows) {
		return store.Bead{}, false
	}
	return rows[m.listCursor].bead, true
}

// New builds the root model. ctx bounds every data-fetch command the TUI
// issues.
