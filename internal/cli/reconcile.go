package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tui"
)

// toTUIReconcileSummary converts this package's ReconcileReport into the
// tui package's own, TUI-local type — internal/tui can't import
// internal/cli (which imports tui already, for Deps/Run), so the two
// packages each keep their own copy of this shape.
func toTUIReconcileSummary(r ReconcileReport) tui.ReconcileSummary {
	actions := make([]tui.ReconcileActionSummary, len(r.Actions))
	for i, a := range r.Actions {
		actions[i] = tui.ReconcileActionSummary{BRN: a.BRN, From: a.From, To: a.To, Reason: a.Reason}
	}
	return tui.ReconcileSummary{Actions: actions}
}

// ReconcileAction records one change the reconciler made (or, for the
// off-by-default retry case, one it declined to make so a caller can
// surface it instead).
type ReconcileAction struct {
	BRN    domain.BRN
	From   domain.BeadState
	To     domain.BeadState
	Reason string
}

// ReconcileReport is the outcome of one Reconcile pass.
type ReconcileReport struct {
	Actions []ReconcileAction
}

// reconcileValidatingMargin scales domain.GateProfileTimeout: a bead sitting
// in "validating" longer than this multiple of its own profile's step
// timeouts, with no live session watching it, is treated as stuck rather
// than merely running a slow profile.
const reconcileValidatingMargin = 2.0

// Reconcile is BARON's central state-machine manager: the one periodic pass
// that keeps every bead's recorded state in sync with observable reality,
// so nothing sits silently frozen just because no human happened to look
// and nothing runs on a raw model assignment that isn't actually available
// any more. It owns three kinds of drift:
//
//   - status drift — a bead claims to be working/validating/etc. but the
//     process behind that claim is gone, stalled, or done; see the
//     per-state policy below.
//   - assignment drift — a bead is still waiting on a capability tier to
//     resolve into a real agent+model (reconcileTierAssignments), or its
//     last attempt failed with a transient provider error (rate limit,
//     quota, an overloaded/5xx API) and needs a same-tier sibling before
//     the next retry (see reconcileRetry).
//   - authority drift — bd's status doesn't match the last state BARON
//     itself recorded setting it to, meaning something changed it without
//     going through BARON at all (reconcileStateDrift). Checked first,
//     before any of the above, so every other check in this pass acts on
//     a status it can trust.
//
// Every transition or reassignment it makes is tagged actor:manager (see
// domain.ActorManager) so it's distinguishable from a human action in the
// audit log.
//
// skipBRNs excludes beads the caller already knows are live — a TUI
// tracking an open embedded session for one of them — since a live session
// is authoritative over any staleness heuristic here.
//
// One bead list load, one already-loaded catalog, one sequential pass:
// never concurrent writes against the same beads store, and never N
// re-derivations of the same list or catalog (the failure mode the
// blocked-only reconciler already had, generalized here instead of
// repeated).
//
// Per-state policy:
//   - open: no status drift to check (nothing claims to be in progress),
//     but still scanned for assignment drift — see reconcileTierAssignments.
//   - assigned: skip — nothing to catch it lying about yet.
//   - blocked: domain.ReconcileBlockedStatus (existing, pure).
//   - working: a dead tmux window lands it on retry — generalizes the TUI's
//     existing reconcileWorkingCmd to run outside an open TUI session too.
//   - validating: no audit activity for longer than the profile's own gate
//     timeouts allow lands it via recordFailure's retry/human_queue policy,
//     with the attempt count derived from the audit log (see
//     retryAttemptCount) so the budget survives a process boundary instead
//     of resetting to zero every reconcile tick.
//   - retry: first checked for a transient-failure reason and reassigned to
//     a same-tier sibling if so (reassignOnTransientFailure); then, off by default
//     (cfg.Gate.AutoRetry), relaunches via StartRun when enabled —
//     otherwise reported as needing a relaunch so a caller (the TUI) can
//     surface that as an affordance instead of acting on its own. This is
//     the one place reconciliation spawns a real process on a timer, so it
//     gets the most caution.
//   - mergable: re-attempts tryAutoMerge's policy evaluation — already
//     gated on cfg.Merge.Auto.Enabled and confirm-free, so safe to retry;
//     never calls runMerge (it blocks on stdin confirmation, which would
//     deadlock a background caller).
//   - human_queue, merged, closed, cancelled: skip — terminal, or
//     genuinely needs a human decision. Correct by design, not a gap.
func (a *app) Reconcile(ctx context.Context, skipBRNs map[string]bool) (ReconcileReport, error) {
	var report ReconcileReport

	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return report, err
	}

	allBeads, err := a.beads.List(ctx)
	if err != nil {
		return report, err
	}

	a.reconcileStateDrift(ctx, allBeads, skipBRNs, &report)

	domainBeads := make([]*domain.Bead, len(allBeads))
	byBRN := make(map[string]*store.Bead, len(allBeads))
	for i := range allBeads {
		domainBeads[i] = storeBeadToDomain(&allBeads[i])
		byBRN[string(allBeads[i].BRN)] = &allBeads[i]
	}

	sm := domain.NewBeadStateMachine()

	for _, change := range sm.ReconcileBlockedStatus(domainBeads) {
		brn, to := change[0], change[1]
		if skipBRNs[brn] {
			continue
		}
		bead := byBRN[brn]
		if bead == nil {
			continue
		}
		from := resolveDomainState(*bead)
		reason := "dependency graph changed"
		if err := a.applyReconcile(ctx, domain.BRN(brn), from, domain.BeadState(to), reason); err != nil {
			a.warn("reconcile %s: %v", brn, err)
			continue
		}
		report.Actions = append(report.Actions, ReconcileAction{BRN: domain.BRN(brn), From: from, To: domain.BeadState(to), Reason: reason})
	}

	host := a.agentHostFor(ctx, cfg)
	prof := profileByName(cfg, cfg.General.Profile, a.dir)
	maxValidatingAge := domain.GateProfileTimeout(prof, cfg.Gate.Timeout*60, reconcileValidatingMargin)
	cat := agent.LoadCatalog()

	a.reconcileTierAssignments(ctx, cat, allBeads, skipBRNs, &report)
	a.reconcilePersonaTriggers(ctx, cfg, allBeads, cat)

	for i := range allBeads {
		bead := &allBeads[i]
		brn := string(bead.BRN)
		if skipBRNs[brn] {
			continue
		}
		state := resolveDomainState(*bead)

		switch state {
		case domain.BeadStateWorking:
			a.reconcileWorking(ctx, host, bead, state, &report)
		case domain.BeadStateValidating:
			a.reconcileValidating(ctx, bead, state, maxValidatingAge, cfg.Gate.RetryBudget, &report)
		case domain.BeadStateRetry:
			a.reconcileRetry(ctx, cat, bead, state, cfg.Gate.AutoRetry, &report)
		case domain.BeadStateAssigned:
			a.reconcileAssigned(ctx, bead, state, domainBeads, cfg.General.AutoStart, &report)
		case domain.BeadStateMergable:
			a.reconcileMergeReady(ctx, bead, state, cfg.Merge.Auto.Enabled)
		case domain.BeadStateHumanQueue:
			a.reconcileHumanQueue(ctx, cat, bead, state, &report)
		default:
			// open/blocked/merged/closed/cancelled: nothing for this pass to
			// reconcile — open has no drift to correct, blocked/unblocked
			// transitions are handled separately above (see
			// domain.ReconcileBlockedStatus), and the rest are terminal
			// states.
		}
	}

	return report, nil
}

// applyReconcile transitions brn as actor:manager and audits it.
func (a *app) applyReconcile(ctx context.Context, brn domain.BRN, from, to domain.BeadState, reason string) error {
	if err := a.transitionStatus(ctx, a.idOf(brn), from, to, a.managerActor()); err != nil {
		return err
	}
	a.auditLogAs(storeActor(a.managerActor()), "reconcile", string(brn), fmt.Sprintf("%s -> %s: %s", from, to, reason))
	return nil
}

// reconcileStateDrift catches a bead whose bd status doesn't match the last
// state BARON's own code legitimately set it to (recordStateSnapshot) — the
// third kind of drift Reconcile owns, alongside status and assignment
// drift (see the type's doc comment): "authority drift", where bd's status
// changed without going through BARON at all. The most likely cause is a
// raw `bd update`/`bd close` run from inside a task-agent's worktree — the
// thing internal/domain.TaskAgentEnv's BEADS_DB override
// (docs/PRD/harness-hardening.md §3.2) is meant to prevent at the source;
// this is the independent second layer that catches it if that ever fails,
// gets bypassed, or a human runs bd by hand.
//
// A bead never before seen (no snapshot entry) is seeded rather than
// flagged — there's nothing to compare against, and a bead created by a
// bare `bd create` bypassing baron work create is a known, accepted gap
// (see bd remember baron-tier-enforcement-layer), not drift. Any mismatch
// after that is reverted unconditionally: unlike reconcileWorking et al,
// which weigh a live process against a stale label, there is no legitimate
// reason bd's status would move without BARON's own snapshot moving with
// it, so this never needs a grace period or a "maybe it's fine" branch.
func (a *app) reconcileStateDrift(ctx context.Context, allBeads []store.Bead, skipBRNs map[string]bool, report *ReconcileReport) {
	snapshot := a.loadStateSnapshot()
	dirty := false
	for i := range allBeads {
		bead := &allBeads[i]
		brn := string(bead.BRN)
		if skipBRNs[brn] {
			continue
		}
		id := a.idOf(bead.BRN)
		actual := resolveDomainState(*bead)
		expected, ok := snapshot[id]
		if !ok {
			snapshot[id] = actual
			dirty = true
			continue
		}
		if expected == actual {
			continue
		}
		// "assigned" is derived (status=open + an assignee), never a
		// literal bd status transitionStatus's callers set directly — see
		// store.Bead.DomainState. Reverting to it for real means reverting
		// bd's status to "open" (the assignee itself was never bd status
		// and isn't what drifted).
		revertTo := expected
		if revertTo == domain.BeadStateAssigned {
			revertTo = domain.BeadStateOpen
		}
		if err := a.beads.Status(ctx, id, toStoreStatus(revertTo)); err != nil {
			a.warn("reconcile state-drift revert %s: %v", brn, err)
			continue
		}
		bead.Status = toStoreStatus(revertTo) // keep this pass's in-memory copy consistent
		detail := fmt.Sprintf("expected=%s actual=%s reverted_to=%s", expected, actual, revertTo)
		a.auditLogAs(storeActor(a.managerActor()), "state_drift", brn, detail)
		report.Actions = append(report.Actions, ReconcileAction{
			BRN: bead.BRN, From: actual, To: revertTo,
			Reason: "bd status changed outside baron: " + detail,
		})
	}
	if dirty {
		a.saveStateSnapshot(snapshot)
	}
}

// reconcileWorkingGrace is how long a "working" bead gets, since it last
// entered that state, before an absent tmux window is trusted as "the agent
// died" rather than "the post-agent pipeline (gate, secret scan, commit,
// the eventual -> validating transition) is still finishing in-process".
// The tmux window closes the instant the agent process exits, but that
// pipeline runs afterward in the same goroutine and isn't observable via
// tmux at all — without this grace period, a reconcile tick landing in that
// gap flips the bead to retry out from under the pipeline's own in-flight
// transition, which then fails with "invalid bead transition retry ->
// validating".
const reconcileWorkingGrace = 10 * time.Second

// reconcileWorking lands a "working" bead on retry when its tmux window's
// agent is gone. host == nil (tmux disabled or unavailable) means liveness
// can't be determined at all — skip rather than guess either way.
