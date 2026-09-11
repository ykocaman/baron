package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// handlePendingRunAfterAssign spawns the freshly-assigned agent once its
// reload lands — see pendingRunAfterAssign's own doc comment: this is the
// reload the assign command itself queued, so msg.beads here is what
// finally carries the new assignee, spawned now rather than from
// commandRanMsg.
func (m *Model) handlePendingRunAfterAssign(msg beadsLoadedMsg) []tea.Cmd {
	if m.pendingRunAfterAssign == "" {
		return nil
	}
	brn := m.pendingRunAfterAssign
	m.pendingRunAfterAssign = ""
	for _, b := range msg.beads {
		if string(b.BRN) == brn && b.Assignee != "" {
			return []tea.Cmd{m.embeddedSpawnCmd(brn, kindAgent)}
		}
	}
	return nil
}

// handlePendingFocusBRN jumps the dashboard to a freshly created bead's tab
// — overriding landFirstNonEmptyTab/landFirstNonEmptySubTab's own fallback,
// which would otherwise keep the dashboard on whatever tab was active.
// Gated to screenDashboard: this reload can be slow (real bd/git I/O), and
// both of pendingFocusBRN's sources — a new bead, or Prompt Mode's 'o'
// hand-off — fire it from an action the user can immediately navigate away
// from (e.g. 'o' then 'P' back into Prompt Mode before the reload lands).
// Applying it unconditionally would set m.detail.BRN alone (Title still
// whatever it was) against m.detail — a field Prompt Mode's thread pane
// reads too — stamping a title-less bead stub into a screen the user has
// since moved to. Left pending (not cleared) when off the dashboard, so it
// still lands correctly next time this fires while the dashboard is
// actually showing.
func (m *Model) handlePendingFocusBRN() {
	if m.pendingFocusBRN == "" || m.screen != screenDashboard {
		return
	}
	brn := domain.BRN(m.pendingFocusBRN)
	m.pendingFocusBRN = ""
	if tab, sub, ok := m.tabContaining(brn); ok {
		m.listTab = tab
		m.listSubTab = sub
		m.detail.BRN = brn
	}
}

// landFirstNonEmptyTab lands the dashboard on the first tab that has beads,
// so it never opens on an empty list ("select a bead"). The dashboard
// starts on the Active tab (listTab 1); if nothing is working, fall back to
// the first tab that has beads.
func (m *Model) landFirstNonEmptyTab() {
	if m.listTab != 1 || m.screen != screenDashboard || len(m.splitRows(1)) != 0 {
		return
	}
	for t := range boardColumns {
		if len(m.splitRows(t)) > 0 {
			m.listTab = t
			return
		}
	}
}

// landFirstNonEmptySubTab lands on the first non-empty sub-tab, once, on
// the first load, so the dashboard never opens on an empty list. After that
// the user's sub-tab choice — including a deliberately empty bucket — must
// survive reloads: reverting to a filled bucket on every refresh blocks
// clicking through a 0-count category.
func (m *Model) landFirstNonEmptySubTab() {
	if m.screen != screenDashboard || m.subTabLanded || len(m.splitRows(m.listTab)) == 0 {
		return
	}
	if m.listSubTab < 0 || len(m.bucketRows(m.listTab, m.listSubTab)) == 0 {
		for i := range boardColumns[m.listTab].statuses {
			if len(m.bucketRows(m.listTab, i)) > 0 {
				m.listSubTab = i
				break
			}
		}
	}
	m.subTabLanded = true
}

// restoreDashboardSelection keeps the dashboard's cursor following the same
// bead across a reload (by BRN, falling back to liveBRN when nothing was
// selected yet) rather than snapping back to row 0 every time beads load —
// or, when that bead is gone from the current view, clamps the cursor and
// reloads the newly-selected bead's comments/audit trail.
func (m *Model) restoreDashboardSelection() []tea.Cmd {
	rows := m.currentRows()
	if m.screen != screenDashboard {
		return nil
	}
	if len(rows) == 0 {
		m.selectListRow()
		return nil
	}
	foundIdx := -1
	for i, r := range rows {
		if r.bead.BRN == m.detail.BRN || (m.detail.BRN == "" && string(r.bead.BRN) == m.liveBRN) {
			foundIdx = i
			break
		}
	}
	if foundIdx >= 0 {
		m.listCursor = foundIdx
		m.detail = rows[foundIdx].bead
		m.liveBRN = string(m.detail.BRN)
		return nil
	}
	if m.listCursor >= len(rows) {
		m.listCursor = max(0, len(rows)-1)
	}
	m.selectListRow()
	return []tea.Cmd{
		loadComments(m.ctx, m.deps, string(m.detail.BRN)),
		loadAuditEvents(m.deps, string(m.detail.BRN)),
	}
}

// syncPromptBeadsDetail is Prompt Mode's Beads-tab equivalent of
// restoreDashboardSelection — caught by hand: pressing 'P' before the app's
// very first beadsLoadedMsg lands (m.beads still empty, entirely plausible
// on a fast real launch) leaves promptBeadDetailCmd's own togglePromptMode
// call with nothing to select, so the right-bottom pane is stuck on "select
// a bead to read it" — and with nothing here to retry it, stayed stuck even
// once beads DID load a moment later, since nothing else re-triggers a
// Beads-tab detail load on this path. wasEmpty gates the comments/audit
// fetch to just that first-load case — every later beadsLoadedMsg (the
// periodic reconcile refresh) still refreshes m.detail's bead data
// (status/title may have changed) without re-fetching comments on every
// tick.
func (m *Model) syncPromptBeadsDetail() []tea.Cmd {
	if m.screen != screenCrew || m.promptRightTab != promptTabBeads {
		return nil
	}
	wasEmpty := m.detail.BRN == ""
	bead, ok := m.promptSelectedBead()
	if !ok {
		return nil
	}
	m.detail = bead
	if !wasEmpty {
		return nil
	}
	return []tea.Cmd{loadComments(m.ctx, m.deps, string(bead.BRN)), loadAuditEvents(m.deps, string(bead.BRN))}
}

// restoreRunSummaries restores each bead's persisted run summary into its
// pane — but only when no pane exists yet, so a session started this
// session (already running live) is never hijacked into a done pane.
func (m *Model) restoreRunSummaries(msg beadsLoadedMsg) {
	if m.deps.ReadRunSummary == nil {
		return
	}
	for _, b := range msg.beads {
		brn := string(b.BRN)
		if m.panes[brn] != nil {
			continue
		}
		if lines, err := m.deps.ReadRunSummary(brn); err == nil && len(lines) > 0 {
			m.panes[brn] = &agentPane{summary: lines, done: true}
		}
	}
}

// reconcileWorkingBeads reconciles "working" beads with no in-process
// session — the common case right after startup (m.sessions always starts
// empty), but also a bead whose agent silently died. Without this, 't' just
// says "no session" forever even though bd still says working: there's no
// way to tell "still running in the background, unattached" from "actually
// gone" without asking AgentHost. Each bead is checked at most once per
// process (m.reconciled).
func (m *Model) reconcileWorkingBeads(msg beadsLoadedMsg) []tea.Cmd {
	if m.deps.AgentHost == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, b := range msg.beads {
		brn := string(b.BRN)
		if b.Status != store.BeadStatusWorking || m.sessions[brn] != nil || m.reconciled[brn] {
			continue
		}
		m.reconciled[brn] = true
		cmds = append(cmds, reconcileWorkingCmd(m.ctx, m.deps.AgentHost, brn))
	}
	return cmds
}

// dispatchReconcile fires general reconciliation (blocked, validating,
// retry, mergable — see internal/cli/reconcile.go for the per-state
// policy). Skips every "working" bead (reconcileWorkingBeads owns that
// state, including the reattach-a-live-view behavior Reconcile has no way
// to do), every bead with a live in-process session, and every bead a human
// opted out of via '+'/'-' (m.autoOffBRNs — see updateKey). Rate-limited to
// reconcileCooldown regardless of how often beads reload, since Reconcile
// shells out to git/tmux/bd per bead it examines — and never dispatched
// while one is already running (reconcileInFlight), since a slow pass
// (enough beads) can outlast the cooldown itself.
func (m *Model) dispatchReconcile(msg beadsLoadedMsg) []tea.Cmd {
	if m.deps.Reconcile == nil || m.reconcileInFlight || time.Since(m.lastReconcile) < reconcileCooldown {
		return nil
	}
	m.lastReconcile = time.Now()
	m.reconcileInFlight = true
	skip := make(map[string]bool, len(m.sessions)+len(msg.beads)+len(m.autoOffBRNs))
	for brn := range m.sessions {
		skip[brn] = true
	}
	for brn, off := range m.autoOffBRNs {
		if off {
			skip[brn] = true
		}
	}
	for _, b := range msg.beads {
		if b.Status == store.BeadStatusWorking {
			skip[string(b.BRN)] = true
		}
	}
	return []tea.Cmd{reconcileCmd(m.ctx, m.deps, skip)}
}
