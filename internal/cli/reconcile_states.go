package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

func (a *app) reconcileWorking(ctx context.Context, host agentHostChecker, bead *store.Bead, state domain.BeadState, report *ReconcileReport) {
	if host == nil {
		return
	}
	alive, err := host.Alive(ctx, domain.TmuxWindowName(string(bead.BRN)))
	if err != nil || alive {
		return
	}
	if since, ok := a.lastStatusChange(bead.BRN, "working"); ok && time.Since(since) < reconcileWorkingGrace {
		return
	}
	reason := "agent process is gone"
	if err := a.applyReconcile(ctx, bead.BRN, state, domain.BeadStateRetry, reason); err != nil {
		a.warn("reconcile %s: %v", bead.BRN, err)
		return
	}
	report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: domain.BeadStateRetry, Reason: reason})
}

// reconcileValidating lands a stalled "validating" bead via recordFailure's
// existing retry/human_queue policy, once it's been longer than
// maxAge since the bead last entered validating with no gate/status audit
// activity since. lastStatusChange returning !ok (no reliable audit
// timestamp — e.g. an older bead from before this transition started being
// audited) means leave it alone rather than guess.
func (a *app) reconcileValidating(ctx context.Context, bead *store.Bead, state domain.BeadState, maxAge time.Duration, budget int, report *ReconcileReport) {
	since, ok := a.lastStatusChange(bead.BRN, "validating")
	if !ok || time.Since(since) < maxAge {
		return
	}
	attempt := a.retryAttemptCount(bead.BRN)
	reason := fmt.Sprintf("validating stalled: no activity for over %s", maxAge)
	exhausted, newState, err := a.recordFailure(ctx, bead.BRN, state, &attempt, budget, reason, a.managerActor())
	if err != nil {
		a.warn("reconcile %s: %v", bead.BRN, err)
		return
	}
	if exhausted {
		reason = "validating stalled, retry budget exhausted"
	}
	report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: newState, Reason: reason})
}

// reconcileRetry relaunches a "retry" bead when autoRetry is enabled;
// otherwise it just reports the bead as needing one, so a caller (the TUI)
// can surface a "relaunch?" affordance instead of BARON silently spawning
// an agent process on a timer. Before any of that, a failed bead is
// reassigned to a different model within the same tier
// (reassignOnTransientFailure) — no point relaunching against the exact
// model that just failed, regardless of whether the failure was transient
// (rate limit, 5xx) or permanent (agent crash, silent death).
//
// A billing or transient failure (rate limit, quota, an overloaded/5xx
// provider) is treated differently from autoRetry's own "blind relaunch of
// whatever just failed" policy: once reassignOnTransientFailure has swapped
// the bead onto a different, actually-available model (or, for TierFast,
// downgraded it to TierFree), that's no longer the same launch that just
// failed — it relaunches unconditionally, regardless of gate.auto_retry, so
// fast -> free recovery never waits on a human pressing 'r'. If no model is
// left anywhere (fast and free both exhausted), there is nothing left to
// retry automatically, so it goes straight to human_queue instead of
// looping on the exact setup that just failed. An ordinary, non-billing,
// non-transient reason (a real gate failure, a plain crash) keeps the
// original autoRetry-gated behavior — that failure class still deserves a
// human's judgment before a blind relaunch.
func (a *app) reconcileRetry(ctx context.Context, cat *agent.Catalog, bead *store.Bead, state domain.BeadState, autoRetry bool, report *ReconcileReport) {
	reason, hasReason := a.lastRetryReason(bead.BRN)
	if hasReason && a.reconcileRetryWithReason(ctx, cat, bead, state, reason, report) {
		return
	}
	if !autoRetry {
		report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: state, Reason: "needs relaunch (gate.auto_retry is disabled)"})
		return
	}
	if err := a.StartRun(ctx, bead.BRN); err != nil {
		a.warn("reconcile relaunch %s: %v", bead.BRN, err)
		return
	}
	report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: domain.BeadStateWorking, Reason: "auto-relaunched"})
}

// reconcileRetryWithReason handles reconcileRetry's "bead has a known
// failure reason" branch: reassign to a different model (or tier) first,
// then either auto-relaunch (a recoverable failure) or park in human_queue
// (all tiers exhausted). Returns handled=true when it fully resolved the
// bead — including "reassigned but not recoverable, leave it for a manual
// relaunch" — so the caller must not fall through to its own
// autoRetry-gated relaunch; handled=false only when no reassignment was
// possible and the failure wasn't billing/transient either, matching
// reconcileRetry's original fallthrough to the plain autoRetry path.
func (a *app) reconcileRetryWithReason(ctx context.Context, cat *agent.Catalog, bead *store.Bead, state domain.BeadState, reason string, report *ReconcileReport) bool {
	recoverable := isBillingFailure(reason) || isTransientFailureReason(reason)
	if newAssignee, ok := a.reassignOnTransientFailure(ctx, cat, bead, reason); ok {
		failureType := "failure"
		if isBillingFailure(reason) || isTransientFailureReason(reason) {
			failureType = billingOrTransientLabel(reason)
		}
		report.Actions = append(report.Actions, ReconcileAction{
			BRN: bead.BRN, From: state, To: state,
			Reason: fmt.Sprintf("reassigned to %s after a %s: %s", newAssignee, failureType, reason),
		})
		if !recoverable {
			return true
		}
		if err := a.StartRun(ctx, bead.BRN); err != nil {
			a.warn("reconcile relaunch %s: %v", bead.BRN, err)
			return true
		}
		report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: domain.BeadStateWorking, Reason: "auto-relaunched after reassignment"})
		return true
	}
	if !recoverable {
		return false
	}
	reasonMsg := fmt.Sprintf("all tiers exhausted after a %s: %s", billingOrTransientLabel(reason), reason)
	if err := a.applyReconcile(ctx, bead.BRN, state, domain.BeadStateHumanQueue, reasonMsg); err != nil {
		a.warn("reconcile %s: %v", bead.BRN, err)
		return true
	}
	report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: domain.BeadStateHumanQueue, Reason: reasonMsg})
	return true
}

// reconcileAssigned starts an assigned bead when autoStart is enabled;
// otherwise it just reports the bead as needing one, so a caller (the TUI)
// can surface a "start?" affordance instead of BARON silently spawning
// an agent process on a timer. This is the product.md §5.17 feature,
// gated on general.auto_start (default off), and gets the same caution
// as auto_retry because it spawns a real process.
func (a *app) reconcileAssigned(ctx context.Context, bead *store.Bead, state domain.BeadState, allBeads []*domain.Bead, autoStart bool, report *ReconcileReport) {
	sm := domain.NewBeadStateMachine()
	if sm.HasLiveBlockers(storeBeadToDomain(bead), allBeads) {
		return
	}
	if !autoStart {
		report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: state, Reason: "needs start (general.auto_start is disabled)"})
		return
	}
	a.loadAgents()
	if ag, ok := a.agents.Get(bead.Assignee); !ok || ag.Status != agent.StatusActive {
		report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: state, Reason: "agent inactive"})
		return
	}
	if err := a.StartRun(ctx, bead.BRN); err != nil {
		a.warn("reconcile auto-start %s: %v", bead.BRN, err)
		return
	}
	report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: domain.BeadStateWorking, Reason: "auto-started"})
}

// reconcileTierAssignments resolves every bead that has a mandatory tier
// (store.Bead.Tier) but no assignee yet into an actual agent+model pulled
// from cat, using pickModelForTier's policy. Deliberately not run at
// bead-creation time: assignment is its own step, so a freshly created
// bead sits untouched (still plain "open") until the next reconcile pass,
// giving a human a window to change the tier before it locks onto a real
// CLI.
//
// skipBRNs is honored the same way the rest of Reconcile honors it: a bead
// the caller already knows is live (a TUI tracking an open session) is
// left alone, though in practice an unassigned bead can never be live
// (nothing could have launched it with no agent set).
