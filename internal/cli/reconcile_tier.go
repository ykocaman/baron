package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

func (a *app) reconcileTierAssignments(ctx context.Context, cat *agent.Catalog, allBeads []store.Bead, skipBRNs map[string]bool, report *ReconcileReport) {
	for i := range allBeads {
		bead := &allBeads[i]
		if skipBRNs[string(bead.BRN)] || bead.Assignee != "" {
			continue
		}
		// bead.Tier() never returns "" — a bead with no/invalid tier
		// metadata (e.g. created via bare `bd`, bypassing baron's
		// mandatory field) defaults to agent.TierFast, so it resolves here
		// like any other fast-tier bead instead of sitting unassigned
		// forever (see store.Bead.Tier's doc comment).
		tier := bead.Tier()
		m, ok := a.pickModelForTier(cat, tier)
		if !ok {
			continue // nothing installed satisfies this tier yet — try again next pass
		}
		reason := fmt.Sprintf("tier %s resolved to %s (%s)", tier, m.ID, m.Agent)
		if err := a.resolveTierAssignment(ctx, bead, m, reason); err != nil {
			a.warn("reconcile tier-assign %s: %v", bead.BRN, err)
			continue
		}
		report.Actions = append(report.Actions, ReconcileAction{BRN: bead.BRN, From: domain.BeadStateOpen, To: domain.BeadStateAssigned, Reason: reason})
	}
}

// resolveTierAssignment assigns bead to m's real agent and records m as
// its model metadata, auditing the change as actor:manager with the given
// reason — the same two bd calls a human tier-to-model resolution would
// make (store.BeadStore.Assign + SetModelMetadata), just automated.
func (a *app) resolveTierAssignment(ctx context.Context, bead *store.Bead, m agent.Model, reason string) error {
	id := a.idOf(bead.BRN)
	if err := a.beads.Assign(ctx, id, m.Agent); err != nil {
		return err
	}
	var modelP *string
	if m.Name != "" {
		modelP = &m.Name
	}
	if err := a.beads.SetModelMetadata(ctx, id, modelP, nil); err != nil {
		return err
	}
	a.auditLogAs(storeActor(a.managerActor()), "assign", string(bead.BRN), reason)
	_ = agent.LoadCatalog().Record(m.ID).Save()
	return nil
}

// pickModelForTier returns the first catalog entry for tier whose agent CLI
// is actually installed and active on this machine, in catalog order
// (stable — alphabetical by ID) for a deterministic, low-surprise pick.
// "First installed" over any fancier ranking (least-recently-used,
// load-balanced) is a deliberate simplification for this resolver's first
// cut, not a final policy.
func (a *app) pickModelForTier(cat *agent.Catalog, tier agent.Tier) (agent.Model, bool) {
	a.loadAgents()
	for _, m := range cat.ForTier(tier) {
		if ag, ok := a.agents.Get(m.Agent); ok && ag.Status == agent.StatusActive {
			return m, true
		}
	}
	for _, m := range cat.Models {
		if ag, ok := a.agents.Get(m.Agent); ok && ag.Status == agent.StatusActive {
			return m, true
		}
	}
	for _, ag := range a.agents.Active() {
		return agent.Model{ID: ag.Name, Agent: ag.Name, Tier: tier}, true
	}
	return agent.Model{}, false
}

// isTransientFailureReason reports whether a retry reason (see
// recordFailure, opencodeFailureReason — this is arbitrary text lifted from
// a CLI's own stderr/log, not a structured error) describes a failure the
// same model is likely to hit again immediately — a rate limit, a quota,
// or the provider's own API being overloaded/unavailable/erroring — rather
// than an ordinary silent death or crash. This is the signal reconcileRetry
// uses to decide a same-tier reassignment is worth trying before
// relaunching. Named and organized by what each keyword actually reports,
// not just "rate limit", since 429 is only one shape of "try someone else
// instead of hammering the same endpoint again" — an Anthropic-style
// overloaded_error (529), a 5xx from any provider, or a bare "timeout"/
// "unavailable" all mean the same thing in practice.
// ponytail: billing split — direct free, skip fast exhaustive; see isBillingFailure
func isBillingFailure(reason string) bool {
	lower := strings.ToLower(reason)
	for _, kw := range []string{
		"insufficient", "balance", "402", "billing", "payment required", "credit",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// billingOrTransientLabel labels reason as "billing failure" or "transient
// failure" for a message — for a reason already known to be one or the
// other (isBillingFailure or isTransientFailureReason), billing takes
// precedence when both happen to match.
func billingOrTransientLabel(reason string) string {
	if isBillingFailure(reason) {
		return "billing failure"
	}
	return "transient failure"
}

func isTransientFailureReason(reason string) bool {
	lower := strings.ToLower(reason)
	for _, kw := range []string{
		// Rate limit / quota / auth (auth expiry as
		// "Failed to authenticate: OAuth session expired" — see test-msl).
		"rate limit", "429", "quota", "too many requests",
		"authenticate", "oauth", "session expired", "not authenticated",
		// Provider overloaded (Anthropic's overloaded_error surfaces as
		// "529 Overloaded" in claude's own CLI output).
		"529", "overloaded",
		// Generic 5xx transience.
		"500", "502", "503", "504",
		"internal server error", "bad gateway", "service unavailable", "gateway timeout",
		// Network/availability wording that isn't tied to one status code.
		"temporarily unavailable", "try again later", "timed out", "timeout",
		"connection reset", "connection refused",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// lastRetryReason returns the most recent "retry" audit event's detail for
// brn, and whether one was found — the same audit-log scan lastStatusChange
// uses, reused here to recover *why* the last retry happened so the
// reconciler can tell a rate limit from an ordinary silent death.
// ponytail: audit detail is often truncated to "exit code 1" (generic);
// when it looks generic, enrich with the agent log tail so transient
// keywords like "OAuth session expired" are still detectable.
func (a *app) lastRetryReason(brn domain.BRN) (string, bool) {
	if a.audit == nil {
		return "", false
	}
	events, err := a.audit.Query(string(brn))
	if err != nil {
		return "", false
	}
	var latest time.Time
	var detail string
	found := false
	for _, e := range events {
		if e.Action != "retry" {
			continue
		}
		if !found || e.Time.After(latest) {
			latest, detail, found = e.Time, e.Detail, true
		}
	}
	if !found {
		return "", false
	}
	if !isTransientFailureReason(detail) && strings.Contains(strings.ToLower(detail), "exit code") {
		detail = a.enrichWithLogTail(brn, detail)
	}
	return detail, true
}

// enrichWithLogTail appends brn's captured agent log tail (capped at the
// newest 4096 bytes) to reason — "" becomes the tail alone, a non-empty
// reason gets " "+tail — or returns reason unchanged when the log can't be
// read. Shared by every "a generic failure reason deserves more than one
// line" site: lastRetryReason (above), classifyAgentFailure
// (run_agent.go), and reconcileHumanQueue (reconcile_merge.go), each with
// its own gate on when enrichment is worth it (a bare "exit code" text, or
// — reconcileHumanQueue — any reason that isn't already known-transient).
func (a *app) enrichWithLogTail(brn domain.BRN, reason string) string {
	data, err := os.ReadFile(domain.AgentLogPath(a.dir, string(brn)))
	if err != nil {
		return reason
	}
	tail := string(data)
	if len(tail) > 4096 {
		tail = tail[len(tail)-4096:]
	}
	if reason == "" {
		return tail
	}
	return reason + " " + tail
}

// reassignOnTransientFailure swaps bead onto a different model within its
// required tier (store.Bead.Tier — recorded at creation and untouched by
// resolution, so it's still there after Assignee/Model point at a real
// agent+model), after a transient provider failure (rate limit, quota, an
// overloaded/5xx API — see isTransientFailureReason) — no point relaunching
// against the exact model that just failed the same way. Tier() always
// returns a real tier now (a bead with no/invalid tier metadata defaults to
// agent.TierFast — see its doc comment), so there is always a tier to fall
// back within; this used to special-case "no tier at all" as "leave it
// alone," which no longer applies. Returns the new agent name and whether a
// reassignment happened.
func (a *app) reassignOnTransientFailure(ctx context.Context, cat *agent.Catalog, bead *store.Bead, reason string) (string, bool) {
	tier := bead.Tier()
	a.loadAgents()
	currentAgent, currentModel := bead.Assignee, bead.Model()

	// 1. Try a different model in the same tier (deterministic, tier-scoped).
	// ponytail: billing skips fast siblings -> direct free fallback
	if !isBillingFailure(reason) {
		if agentName, ok := a.trySameTierReassign(ctx, cat, bead, tier, currentAgent, currentModel, reason); ok {
			return agentName, true
		}
	}

	// 2. Tier exhausted -> deterministic fallback to free tier (zen). Only
	// when the bead was fast and no fast model is available.
	if tier != agent.TierFast {
		return "", false
	}
	// Free also exhausted -> human_queue (no further tier); downgradeToFreeTier
	// itself reports "", false in that case.
	return a.downgradeToFreeTier(ctx, cat, bead, "after a "+billingOrTransientLabel(reason), "after fast tier exhausted")
}

// trySameTierReassign is reassignOnTransientFailure's first attempt: swap
// bead onto a different, active model within the same tier (deterministic,
// tier-scoped) — no point relaunching against the exact model that just
// failed. Returns the new agent name and whether a reassignment happened.
func (a *app) trySameTierReassign(ctx context.Context, cat *agent.Catalog, bead *store.Bead, tier agent.Tier, currentAgent, currentModel, reason string) (string, bool) {
	for _, m := range cat.ForTier(tier) {
		if m.Agent == currentAgent && m.Name == currentModel {
			continue // this is the one that just failed
		}
		if ag, ok := a.agents.Get(m.Agent); !ok || ag.Status != agent.StatusActive {
			continue
		}
		r := fmt.Sprintf("reassigned to %s (%s) after a %s, tier %s", m.ID, m.Agent, billingOrTransientLabel(reason), tier)
		if err := a.resolveTierAssignment(ctx, bead, m, r); err != nil {
			a.warn("reconcile reassign %s: %v", bead.BRN, err)
			return "", false
		}
		bead.Assignee = m.Agent
		return m.Agent, true
	}
	return "", false
}

// downgradeToFreeTier attempts a Fast -> Free tier downgrade for bead: the
// shared fallback every "no fast option left" path uses — a billing or idle
// failure in reconcileHumanQueue (reconcile_merge.go), and a fast-tier
// exhausted from reassignOnTransientFailure's own step 2 above. Only
// meaningful for a Fast-tier bead; iterates the catalog's free-tier models
// in order and reassigns to the first one with an active agent CLI.
// reasonDetail/auditDetail are folded into the reassignment's own audit
// reason and the tier_downgrade audit event respectively, so each call site
// can say *why* it downgraded ("after billing", "after idle", "after fast
// tier exhausted") without duplicating the surrounding SetTierMetadata/
// resolveTierAssignment/audit plumbing. Returns "", false when no free
// model is available — the caller's own bead stays exactly as it was.
func (a *app) downgradeToFreeTier(ctx context.Context, cat *agent.Catalog, bead *store.Bead, reasonDetail, auditDetail string) (agentName string, ok bool) {
	a.loadAgents()
	tier := bead.Tier()
	currentAgent, currentModel := bead.Assignee, bead.Model()
	for _, m := range cat.ForTier(agent.TierFree) {
		if m.Agent == currentAgent && m.Name == currentModel {
			continue
		}
		if ag, active := a.agents.Get(m.Agent); !active || ag.Status != agent.StatusActive {
			continue
		}
		if err := a.beads.SetTierMetadata(ctx, string(bead.BRN), agent.TierFree); err != nil {
			a.warn("reconcile downgrade %s: %v", bead.BRN, err)
			return "", false
		}
		reason := fmt.Sprintf("reassigned to %s (%s) free-tier fallback %s, tier %s->%s", m.ID, m.Agent, reasonDetail, tier, agent.TierFree)
		if err := a.resolveTierAssignment(ctx, bead, m, reason); err != nil {
			a.warn("reconcile reassign %s: %v", bead.BRN, err)
			return "", false
		}
		bead.Assignee = m.Agent
		if bead.Metadata == nil {
			bead.Metadata = map[string]string{}
		}
		bead.Metadata["tier"] = string(agent.TierFree)
		a.auditLogAs(storeActor(a.managerActor()), "tier_downgrade", string(bead.BRN), fmt.Sprintf("downgraded %s from %s to %s %s", bead.BRN, tier, agent.TierFree, auditDetail))
		return m.Agent, true
	}
	return "", false
}

// reconcileMergeReady re-attempts the auto-merge policy for a bead sitting
// in mergable, in case conditions that weren't satisfiable the moment
// the gate passed (a transient preflight conflict, a tag added since) are
// now. Never calls runMerge — that blocks on stdin confirmation, which
// would deadlock a background caller.
