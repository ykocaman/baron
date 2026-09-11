package cli

import (
	"context"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

func (a *app) reconcileMergeReady(ctx context.Context, bead *store.Bead, state domain.BeadState, autoMergeEnabled bool) {
	if !autoMergeEnabled {
		return
	}
	dir, _, err := a.mergeDir(ctx, *bead)
	if err != nil {
		a.warn("reconcile mergable %s: %v", bead.BRN, err)
		return
	}
	wt := domain.Worktree{BRN: string(bead.BRN), Branch: domain.BranchName(bead.IssueType, a.idOf(bead.BRN)), Path: dir}
	if err := a.tryAutoMerge(ctx, bead.BRN, state, wt); err != nil {
		a.warn("reconcile mergable %s: %v", bead.BRN, err)
	}
}

func (a *app) reconcileHumanQueue(ctx context.Context, cat *agent.Catalog, bead *store.Bead, state domain.BeadState, report *ReconcileReport) {
	if bead.Tier() != agent.TierFast {
		return
	}
	reason := a.humanQueueFailureReason(bead)
	lowerReason := strings.ToLower(reason)
	isIdle := strings.Contains(lowerReason, "idle")
	isBilling := isBillingFailure(reason) // ponytail: billing mirrors idle — direct free downgrade, skip fast retry loop
	if !isTransientFailureReason(reason) && !isIdle && !isBilling {
		return
	}
	// ponytail: billing direct free like idle — avoid transient reassign fast
	// loop: both go straight to a free-tier downgrade instead of first trying
	// another Fast-tier sibling the way an ordinary transient failure does
	// (reassignOnTransientFailure's own step 1) — a billing block or a
	// stalled-idle session is exactly as likely to recur on any other
	// Fast-tier model, so trying siblings first would just burn the retry
	// budget on the same outcome.
	switch {
	case isBilling:
		if agentName, ok := a.downgradeToFreeTier(ctx, cat, bead, "after billing", "after billing"); ok {
			a.openAfterReassign(ctx, bead, state, report, agentName, reason)
		}
	case isIdle:
		if agentName, ok := a.downgradeToFreeTier(ctx, cat, bead, "after idle", "after idle"); ok {
			a.openAfterReassign(ctx, bead, state, report, agentName, reason)
		}
	default:
		if agentName, ok := a.reassignOnTransientFailure(ctx, cat, bead, reason); ok {
			a.openAfterReassign(ctx, bead, state, report, agentName, reason)
		}
	}
}

// humanQueueFailureReason recovers why bead landed in human_queue:
// lastRetryReason first, falling back to the most recent "idle" audit
// event's detail when no retry reason was recorded (a bead parked by
// checkIdleAgents rather than a gate/silent-death failure never gets a
// "retry" audit entry), then — unless the reason is already known-transient
// — enriching it with the agent's own captured log tail so a generic reason
// carries more than one line.
func (a *app) humanQueueFailureReason(bead *store.Bead) string {
	reason, _ := a.lastRetryReason(bead.BRN)
	if reason == "" {
		reason = a.latestIdleReason(bead.BRN)
	}
	if !isTransientFailureReason(reason) {
		reason = a.enrichWithLogTail(bead.BRN, reason)
	}
	return reason
}

func (a *app) latestIdleReason(brn domain.BRN) string {
	if a.audit == nil {
		return ""
	}
	events, err := a.audit.Query(string(brn))
	if err != nil {
		return ""
	}
	var reason string
	var latest time.Time
	for _, event := range events {
		if event.Action == "idle" && (reason == "" || event.Time.After(latest)) {
			latest, reason = event.Time, event.Detail
		}
	}
	return reason
}

// openAfterReassign is reconcileHumanQueue's shared "reassignment
// succeeded" tail: reopen the bead so the next reconcile pass picks it up
// under its new assignment, and record the recovery in report.
func (a *app) openAfterReassign(ctx context.Context, bead *store.Bead, state domain.BeadState, report *ReconcileReport, agentName, reason string) {
	if err := a.applyReconcile(ctx, bead.BRN, state, domain.BeadStateOpen, "reassigned to "+agentName+" after transient failure in human_queue: "+reason); err != nil {
		a.warn("reconcile human_queue %s: %v", bead.BRN, err)
		return
	}
	report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: state, To: domain.BeadStateOpen, Reason: "auto-recovered from human_queue to free tier after transient failure"})
}

// agentHostChecker is the one method Reconcile needs from tui.AgentHost —
// declared locally so this package doesn't import internal/tui just for a
// liveness check.
type agentHostChecker interface {
	Alive(ctx context.Context, window string) (bool, error)
}

// lastStatusChange returns the time of the most recent "status" audit
// event moving brn into toState (detail "... -> toState"), and whether one
// was found at all.
func (a *app) lastStatusChange(brn domain.BRN, toState string) (time.Time, bool) {
	if a.audit == nil {
		return time.Time{}, false
	}
	events, err := a.audit.Query(string(brn))
	if err != nil {
		return time.Time{}, false
	}
	suffix := "-> " + toState
	var latest time.Time
	found := false
	for _, e := range events {
		if e.Action != "status" {
			continue
		}
		if !strings.HasSuffix(e.Detail, suffix) {
			continue
		}
		if !found || e.Time.After(latest) {
			latest = e.Time
			found = true
		}
	}
	return latest, found
}

// retryAttemptCount counts "retry" audit actions recorded for brn since its
// most recent "-> mergable" transition, or every one ever recorded if
// it's never reached mergable. Reaching mergable is unambiguous proof
// a retry cycle actually worked — the gate passed, the commit landed — so a
// later, unrelated failure gets a fresh budget instead of being counted
// against attempts from an incident that's already resolved. A bead that's
// never once succeeded keeps the original conservative behavior: every
// retry still counts, including ones from a cycle that ended in
// human_queue and was manually restarted, which errs toward landing a
// genuinely stalled bead in human_queue sooner rather than relaunching it
// more times than a human would expect. audit.Query is the only signal
// that survives a process boundary — store.Run carries no attempt field,
// and the in-memory *int recordFailure normally uses is local to one baron
// run invocation.
func (a *app) retryAttemptCount(brn domain.BRN) int {
	if a.audit == nil {
		return 0
	}
	events, err := a.audit.Query(string(brn))
	if err != nil {
		return 0
	}
	lastSuccess, hasSuccess := a.lastStatusChange(brn, "mergable")
	n := 0
	for _, e := range events {
		if e.Action != "retry" {
			continue
		}
		if hasSuccess && !e.Time.After(lastSuccess) {
			continue
		}
		n++
	}
	return n
}
