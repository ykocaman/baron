package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// TestReconcileBlockedUnblocks: a bead recorded as blocked with no live
// blocker left gets moved to open, tagged actor:manager so it's
// distinguishable from a human action in the audit log.
func TestReconcileBlockedUnblocks(t *testing.T) {
	beads := []store.Bead{
		testBead("baron-blocker", store.BeadStatusClosed),
		{
			ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusBlocked,
			Dependencies: []store.DepLink{{IssueID: "baron-x", DependsOnID: "baron-blocker", Type: "blocks"}},
		},
	}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(report.Actions) != 1 {
		t.Fatalf("Actions = %+v, want 1 (baron-x unblocked)", report.Actions)
	}
	action := report.Actions[0]
	if action.BRN != "baron-x" || action.To != domain.BeadStateOpen {
		t.Errorf("action = %+v, want baron-x -> open", action)
	}
	if !hasCall(fr, "update", "baron-x", "--status", "open") {
		t.Errorf("calls = %v, want bd update baron-x --status open", fr.calls)
	}
	events := auditEvents(t, a)
	found := false
	for _, e := range events {
		if e.Target == "baron-x" && e.Action == "reconcile" {
			found = true
			if e.Actor.Type != store.ActorManager {
				t.Errorf("reconcile event actor = %q, want %q", e.Actor.Type, store.ActorManager)
			}
		}
	}
	if !found {
		t.Errorf("audit events = %+v, want a reconcile event for baron-x", events)
	}
}

// TestReconcileSkipsLiveSession: a bead the caller flags as having a live
// TUI session must be left alone regardless of its recorded status —
// a live session is authoritative over any staleness heuristic.
func TestReconcileSkipsLiveSession(t *testing.T) {
	beads := []store.Bead{
		{
			ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusBlocked,
			Dependencies: []store.DepLink{{IssueID: "baron-x", DependsOnID: "baron-blocker", Type: "blocks"}},
		},
		testBead("baron-blocker", store.BeadStatusClosed),
	}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)

	report, err := a.Reconcile(context.Background(), map[string]bool{"baron-x": true})
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(report.Actions) != 0 {
		t.Errorf("Actions = %+v, want none (baron-x is in skipBRNs)", report.Actions)
	}
	if hasCall(fr, "update", "baron-x", "--status", "open") {
		t.Errorf("calls = %v, want no write to a skipped bead", fr.calls)
	}
}

// TestReconcileRetryDefaultReportsNeedsRelaunch: with gate.auto_retry left
// at its default (false), a "retry" bead is reported as needing a relaunch
// but nothing is actually spawned — the safe default per the design's
// runaway-risk analysis (no persisted attempt count survives a process
// boundary, so silent auto-relaunch ships opt-in only).
func TestReconcileRetryDefaultReportsNeedsRelaunch(t *testing.T) {
	beads := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusRetry}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(report.Actions) != 1 || report.Actions[0].To != domain.BeadStateRetry {
		t.Fatalf("Actions = %+v, want baron-x reported as staying in retry", report.Actions)
	}
	if hasCall(fr, "worktree", "add") {
		t.Errorf("calls = %v, want no relaunch (auto_retry defaults to false)", fr.calls)
	}
}

// TestReconcileRetryAutoRelaunches: with gate.auto_retry enabled, a "retry"
// bead is relaunched the same way `baron run` would launch it.
func TestReconcileRetryAutoRelaunches(t *testing.T) {
	runner := &runGateRunner{beadJSON: `[{"id":"baron-a1b2c3","title":"Implement auth","status":"retry","priority":2,"issue_type":"task","assignee":"claude"}]`, modelCmd: "claude"}
	a := newTestApp(t, runner)
	a.loadConfig = func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.Gate.AutoRetry = true
		return cfg, nil
	}
	writeAgentRegistry(t, testClaude())

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(runner.modelCall) == 0 {
		t.Fatalf("modelCall = %v, want the model launched (auto_retry enabled)", runner.modelCall)
	}
	found := false
	for _, a := range report.Actions {
		if a.BRN == "baron-a1b2c3" && a.Reason == "auto-relaunched" {
			found = true
		}
	}
	if !found {
		t.Errorf("Actions = %+v, want baron-a1b2c3 reported as auto-relaunched", report.Actions)
	}
}

// TestReconcileMergeReadySkippedWhenAutoMergeDisabled: with merge.auto not
// enabled, a mergable bead is left untouched — re-attempting an
// unconfigured policy would be pointless and tryAutoMerge already no-ops on
// this, but reconcileMergeReady should not even shell out to check.
func TestReconcileMergeReadySkippedWhenAutoMergeDisabled(t *testing.T) {
	beads := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusMergable}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)

	if _, err := a.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if hasCall(fr, "merge") {
		t.Errorf("calls = %v, want no merge attempt (merge.auto.enabled is false)", fr.calls)
	}
}

// TestReconcileResolvesTierAssignment: a bead with a tier set but no
// assignee yet gets resolved to the first catalog entry for that tier
// whose agent is actually installed — Assignee set to the real agent,
// model recorded as metadata, and audited as actor:manager.
func TestReconcileResolvesTierAssignment(t *testing.T) {
	beads := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusOpen, Metadata: map[string]string{"tier": "standard"}}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	writeAgentRegistry(t, testClaude())
	writeCatalog(t, agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierStandard})

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(report.Actions) != 1 {
		t.Fatalf("Actions = %+v, want 1 (tier resolved)", report.Actions)
	}
	if !hasCall(fr, "assign", "baron-x", "claude") {
		t.Errorf("calls = %v, want bd assign baron-x claude", fr.calls)
	}
	if !hasCall(fr, "update", "baron-x", "--set-metadata", "model=sonnet", "--unset-metadata", "llm") {
		t.Errorf("calls = %v, want the model recorded as metadata", fr.calls)
	}
	events := auditEvents(t, a)
	found := false
	for _, e := range events {
		if e.Target == "baron-x" && e.Action == "assign" {
			found = true
			if e.Actor.Type != store.ActorManager {
				t.Errorf("assign event actor = %q, want %q", e.Actor.Type, store.ActorManager)
			}
		}
	}
	if !found {
		t.Errorf("audit events = %+v, want an assign event for baron-x", events)
	}
}

// TestReconcileTierAssignmentWaitsForActiveAgent: a tier with catalog
// entries but none whose agent is currently active must be left alone —
// tried again on the next pass, not resolved to an uninstalled CLI.
func TestReconcileTierAssignmentWaitsForActiveAgent(t *testing.T) {
	beads := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusOpen, Metadata: map[string]string{"tier": "standard"}}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	// No writeAgentRegistry: claude is in the catalog but not registered as
	// an active agent on this machine.
	writeCatalog(t, agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierStandard})

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(report.Actions) != 0 {
		t.Errorf("Actions = %+v, want none (no active agent satisfies the tier)", report.Actions)
	}
	if hasCall(fr, "assign", "baron-x", "claude") {
		t.Errorf("calls = %v, want no assign to an uninstalled agent", fr.calls)
	}
}

// TestReconcileRetryReassignsOnRateLimit: a "retry" bead whose most recent
// retry reason names a rate limit gets swapped to a different model in the
// same tier before the relaunch decision, rather than retrying the exact
// model that just got rate-limited.
func TestReconcileRetryReassignsOnRateLimit(t *testing.T) {
	beads := []store.Bead{{
		ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusRetry,
		Assignee: "claude", Metadata: map[string]string{"tier": "standard", "model": "sonnet"},
	}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	writeAgentRegistry(t, testClaude(), testOpencode())
	writeCatalog(
		t,
		agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierStandard},
		agent.Model{ID: "opencode-go/kimi-k2", Agent: "opencode", Name: "opencode-go/kimi-k2", Tier: agent.TierStandard},
	)
	if err := a.audit.Append(store.AuditEvent{
		Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "retry",
		Target: "baron-x", Detail: "retry 1/3: agent failed [opencode: Rate limit exceeded]",
	}); err != nil {
		t.Fatalf("audit.Append() error: %v", err)
	}

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if !hasCall(fr, "assign", "baron-x", "opencode") {
		t.Errorf("calls = %v, want reassignment to the other same-tier model (opencode)", fr.calls)
	}
	if hasCall(fr, "assign", "baron-x", "claude") {
		t.Errorf("calls = %v, want no reassignment back to the model that just failed", fr.calls)
	}
	found := false
	for _, act := range report.Actions {
		if act.BRN == "baron-x" && strings.Contains(act.Reason, "transient failure") {
			found = true
		}
	}
	if !found {
		t.Errorf("Actions = %+v, want one mentioning the transient-failure reassignment", report.Actions)
	}
}

// TestReconcileRetryReassignsOnOverloadedError: the same reassignment
// mechanism as TestReconcileRetryReassignsOnRateLimit, but for the failure
// shape this keyword set was broadened for — a provider-overloaded 5xx
// (Anthropic's "API Error: 529 Overloaded"), not a 429. A same-tier sibling
// still shouldn't get hammered against an endpoint that just failed this
// way either.
func TestReconcileRetryReassignsOnOverloadedError(t *testing.T) {
	beads := []store.Bead{{
		ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusRetry,
		Assignee: "claude", Metadata: map[string]string{"tier": "standard", "model": "sonnet"},
	}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	writeAgentRegistry(t, testClaude(), testOpencode())
	writeCatalog(
		t,
		agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierStandard},
		agent.Model{ID: "opencode-go/kimi-k2", Agent: "opencode", Name: "opencode-go/kimi-k2", Tier: agent.TierStandard},
	)
	if err := a.audit.Append(store.AuditEvent{
		Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "retry",
		Target: "baron-x", Detail: "retry 1/3: agent failed [claude: API Error: 529 Overloaded]",
	}); err != nil {
		t.Fatalf("audit.Append() error: %v", err)
	}

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if !hasCall(fr, "assign", "baron-x", "opencode") {
		t.Errorf("calls = %v, want reassignment to the other same-tier model (opencode)", fr.calls)
	}
	if hasCall(fr, "assign", "baron-x", "claude") {
		t.Errorf("calls = %v, want no reassignment back to the model that just failed", fr.calls)
	}
	found := false
	for _, act := range report.Actions {
		if act.BRN == "baron-x" && strings.Contains(act.Reason, "transient failure") {
			found = true
		}
	}
	if !found {
		t.Errorf("Actions = %+v, want one mentioning the transient-failure reassignment", report.Actions)
	}
}

// TestReconcileRetryReassignsOnNonTransientFailure: an ordinary
// (non-rate-limit) retry reason — agent crash, silent death — must also
// trigger reassignment to a different model. Relaunching the exact model
// that just crashed is no better than relaunching one that got rate-limited.
func TestReconcileRetryReassignsOnNonTransientFailure(t *testing.T) {
	beads := []store.Bead{{
		ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusRetry,
		Assignee: "claude", Metadata: map[string]string{"tier": "standard", "model": "sonnet"},
	}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	writeAgentRegistry(t, testClaude(), testOpencode())
	writeCatalog(
		t,
		agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierStandard},
		agent.Model{ID: "opencode-go/kimi-k2", Agent: "opencode", Name: "opencode-go/kimi-k2", Tier: agent.TierStandard},
	)
	if err := a.audit.Append(store.AuditEvent{
		Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "retry",
		Target: "baron-x", Detail: "retry 1/3: silent death: no output for 5m0s",
	}); err != nil {
		t.Fatalf("audit.Append() error: %v", err)
	}

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if !hasCall(fr, "assign", "baron-x", "opencode") {
		t.Errorf("calls = %v, want reassignment to the other same-tier model (opencode)", fr.calls)
	}
	if hasCall(fr, "assign", "baron-x", "claude") {
		t.Errorf("calls = %v, want no reassignment back to the model that just failed", fr.calls)
	}
	found := false
	for _, act := range report.Actions {
		if act.BRN == "baron-x" && strings.Contains(act.Reason, "reassigned to opencode after a failure") {
			found = true
		}
	}
	if !found {
		t.Errorf("Actions = %+v, want one mentioning the failure reassignment", report.Actions)
	}
}

// TestReconcileRetryBillingReassignAutoRelaunches: a "retry" bead whose last
// failure was a billing error gets downgraded fast->free onto another
// active model AND relaunched immediately, even though gate.auto_retry is
// left at its (default) false — the fast->free->human_queue recovery flow
// must not wait on a human pressing 'r' just because the general "relaunch
// whatever just failed" policy is off. That policy is about blind relaunches
// of an unknown failure; this is a bounded, already-verified-available swap.
// (Billing skips fast siblings and goes straight to the free-tier fallback —
// see reassignOnTransientFailure's step 1 — so the catalog here only needs a
// free-tier model, not a second fast one.)
func TestReconcileRetryBillingReassignAutoRelaunches(t *testing.T) {
	beadJSON := `[{"id":"baron-a1b2c3","title":"Implement auth","status":"retry","priority":2,"issue_type":"task","assignee":"claude","metadata":{"tier":"fast","model":"sonnet"}}]`
	runner := &runGateRunner{beadJSON: beadJSON, modelCmd: "opencode"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude(), testOpencode())
	writeCatalog(
		t,
		agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierFast},
		agent.Model{ID: "opencode/kimi", Agent: "opencode", Name: "opencode/kimi", Tier: agent.TierFree},
	)
	if err := a.audit.Append(store.AuditEvent{
		Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "retry",
		Target: "baron-a1b2c3", Detail: "retry 1/3: agent failed [claude: insufficient credit balance]",
	}); err != nil {
		t.Fatalf("audit.Append() error: %v", err)
	}

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(runner.modelCall) == 0 {
		t.Fatalf("modelCall = %v, want opencode launched despite gate.auto_retry defaulting to false", runner.modelCall)
	}
	found := false
	for _, act := range report.Actions {
		if act.BRN == "baron-a1b2c3" && act.Reason == "auto-relaunched after reassignment" {
			found = true
		}
	}
	if !found {
		t.Errorf("Actions = %+v, want baron-a1b2c3 reported as auto-relaunched after reassignment", report.Actions)
	}
}

// TestReconcileRetryBillingExhaustedGoesToHumanQueue: a "retry" bead whose
// last failure was billing, with no other active model left in either the
// fast or free tier, has nothing left to try automatically — it must land
// in human_queue directly rather than blindly relaunching the exact model
// that just failed billing.
func TestReconcileRetryBillingExhaustedGoesToHumanQueue(t *testing.T) {
	beads := []store.Bead{{
		ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusRetry,
		Assignee: "claude", Metadata: map[string]string{"tier": "fast", "model": "sonnet"},
	}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	writeAgentRegistry(t, testClaude())
	writeCatalog(t, agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierFast})
	if err := a.audit.Append(store.AuditEvent{
		Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "retry",
		Target: "baron-x", Detail: "retry 1/3: agent failed [claude: insufficient credit balance]",
	}); err != nil {
		t.Fatalf("audit.Append() error: %v", err)
	}

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if !hasCall(fr, "update", "baron-x", "--status", "human_queue") {
		t.Errorf("calls = %v, want baron-x parked in human_queue (no model left in any tier)", fr.calls)
	}
	found := false
	for _, act := range report.Actions {
		if act.BRN == "baron-x" && act.To == domain.BeadStateHumanQueue {
			found = true
		}
	}
	if !found {
		t.Errorf("Actions = %+v, want baron-x reported moving to human_queue", report.Actions)
	}
}

// TestIsTransientFailureReason covers the keyword set reconcileRetry uses
// to tell a transient provider failure (worth reassigning within the tier
// before the next retry) apart from an ordinary crash or silent death.
// Billing failures are explicitly not transient — they are handled by
// isBillingFailureReason and must return false here.
func TestIsTransientFailureReason(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"agent failed [opencode: Rate limit exceeded]", true},
		{"agent failed [opencode: error.error=429 Too Many Requests]", true},
		{"quota exceeded for this billing period", true},
		// The user-reported case this keyword set was broadened for:
		// Anthropic's overloaded_error surfaces in claude's own CLI output
		// as a plain "API Error: 529 Overloaded" line.
		{"agent failed [claude: API Error: 529 Overloaded]", true},
		{"agent failed [claude: 500 Internal Server Error]", true},
		{"agent failed [opencode: 502 Bad Gateway]", true},
		{"agent failed [opencode: 503 Service Unavailable]", true},
		{"agent failed [opencode: 504 Gateway Timeout]", true},
		{"agent failed: connection reset by peer", true},
		{"agent failed: connection refused", true},
		{"agent failed: request timed out after 30s", true},
		{"model temporarily unavailable, try again later", true},
		{"silent death: no output for 5m0s", false},
		{"agent failed: exit status 1", false},
		// Billing strings must be false here — they belong to isBillingFailure.
		{"Insufficient balance", false},
		{"402 Payment Required", false},
		{"billing limit exceeded", false},
		{"payment required", false},
		{"credit exhausted", false},
		{"balance insufficient", false},
	}
	for _, c := range cases {
		if got := isTransientFailureReason(c.reason); got != c.want {
			t.Errorf("isTransientFailureReason(%q) = %v, want %v", c.reason, got, c.want)
		}
	}
}

func TestIsBillingFailureReason(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		// 6 positives — billing failures.
		{"Insufficient balance", true},
		{"402 Payment Required", true},
		{"billing limit exceeded", true},
		{"payment required", true},
		{"credit exhausted", true},
		{"balance insufficient", true},
		// 5 negatives — not billing.
		{"quota exceeded", false},
		{"rate limit 429", false},
		{"529 Overloaded", false},
		{"exit code 1", false},
		{"idle", false},
	}
	for _, c := range cases {
		if got := isBillingFailure(c.reason); got != c.want {
			t.Errorf("isBillingFailure(%q) = %v, want %v", c.reason, got, c.want)
		}
	}
}
