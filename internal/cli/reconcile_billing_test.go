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

// assertAssignedTo fails unless fr recorded an "assign" call for brn to want.
func assertAssignedTo(t *testing.T, fr *fakeRunner, brn, want string) {
	t.Helper()
	if !hasCall(fr, "assign", brn, want) {
		t.Errorf("calls = %v, want assign to %s", fr.calls, want)
	}
}

// assertNotAssignedTo fails if fr recorded an "assign" call for the fixed
// test bead "baron-x" to avoid.
func assertNotAssignedTo(t *testing.T, fr *fakeRunner, avoid string) {
	t.Helper()
	if hasCall(fr, "assign", "baron-x", avoid) {
		t.Errorf("calls = %v, want no assign to %s", fr.calls, avoid)
	}
}

// assertTierDowngrade checks both signals a tier-free downgrade leaves
// behind — the bd metadata update and the tier_downgrade audit event — and
// fails unless their presence matches want.
func assertTierDowngrade(t *testing.T, a *app, fr *fakeRunner, brn string, want bool) {
	t.Helper()
	hasDowngrade := hasCall(fr, "update", brn, "--set-metadata", "tier=free")
	if want && !hasDowngrade {
		t.Errorf("calls = %v, want tier downgrade to free", fr.calls)
	}
	if !want && hasDowngrade {
		t.Errorf("calls = %v, want no tier downgrade", fr.calls)
	}
	found := false
	for _, e := range auditEvents(t, a) {
		if e.Target == brn && e.Action == "tier_downgrade" {
			found = true
		}
	}
	if want && !found {
		t.Errorf("audit events = %+v, want tier_downgrade for %s", auditEvents(t, a), brn)
	}
	if !want && found {
		t.Errorf("audit events = %+v, want no tier_downgrade for %s", auditEvents(t, a), brn)
	}
}

// assertTransitionToOpen scans report's actions for brn transitioning to
// open, failing unless its presence matches want.
func assertTransitionToOpen(t *testing.T, report ReconcileReport, brn string, want bool) {
	t.Helper()
	found := false
	for _, act := range report.Actions {
		if act.BRN == domain.BRN(brn) && act.To == domain.BeadStateOpen {
			found = true
		}
	}
	if want && !found {
		t.Errorf("Actions = %+v, want a transition to open for %s", report.Actions, brn)
	}
	if !want && found {
		t.Errorf("Actions = %+v, want no transition to open for %s", report.Actions, brn)
	}
}

// assertActionReasonContains fails unless report has an action for brn whose
// Reason contains substr.
func assertActionReasonContains(t *testing.T, report ReconcileReport, brn, substr string) {
	t.Helper()
	for _, act := range report.Actions {
		if act.BRN == domain.BRN(brn) && strings.Contains(act.Reason, substr) {
			return
		}
	}
	t.Errorf("Actions = %+v, want an action for %s with reason containing %q", report.Actions, brn, substr)
}

// assertDowngradedToFree is TestReconcileTransientStillCyclesFast's positive
// branch: brn must have been downgraded to the free tier (and, once in
// human_queue, moved back to open).
func assertDowngradedToFree(t *testing.T, a *app, fr *fakeRunner, report ReconcileReport, brn string, wasHumanQueue bool) {
	t.Helper()
	assertTierDowngrade(t, a, fr, brn, true)
	if wasHumanQueue {
		assertTransitionToOpen(t, report, brn, true)
	}
}

// assertCycledFastSibling is TestReconcileTransientStillCyclesFast's
// negative branch: brn must have cycled to a fast-tier sibling instead of
// being downgraded to free.
func assertCycledFastSibling(t *testing.T, a *app, fr *fakeRunner, report ReconcileReport, brn string) {
	t.Helper()
	assertTierDowngrade(t, a, fr, brn, false)
	assertActionReasonContains(t, report, brn, "transient failure")
}

// TestReconcileBillingDirectFree: S1 happy billing->free direct. A fast/retry
// bead whose last retry reason is a billing failure (Insufficient balance 402)
// with a catalog containing fast×2 (claude/sonnet, opencode/kimi) and free×1
// (zen) must be reassigned directly to the free agent, NOT to the fast
// sibling, with a tier_downgrade audit and metadata tier free.
func TestReconcileBillingDirectFree(t *testing.T) {
	cases := []struct {
		name   string
		reason string
	}{
		{"insufficient balance 402", "Insufficient balance (402 Payment Required)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beads := []store.Bead{{
				ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusRetry,
				Assignee: "claude", Metadata: map[string]string{"tier": string(agent.TierFast), "model": "sonnet"},
			}}
			fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
			a := newTestApp(t, fr)
			zen := agent.Agent{Name: "zen", Command: "zen", Status: agent.StatusActive, Backend: "subprocess", Args: []string{"{{prompt}}"}, ModelFlag: "--model"}
			writeAgentRegistry(t, testClaude(), testOpencode(), zen)
			writeCatalog(
				t,
				agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierFast},
				agent.Model{ID: "opencode-go/kimi", Agent: "opencode", Name: "kimi", Tier: agent.TierFast},
				agent.Model{ID: "zen", Agent: "zen", Name: "zen", Tier: agent.TierFree},
			)
			if err := a.audit.Append(store.AuditEvent{
				Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "retry",
				Target: "baron-x", Detail: tc.reason,
			}); err != nil {
				t.Fatalf("audit.Append() error: %v", err)
			}
			report, err := a.Reconcile(context.Background(), nil)
			if err != nil {
				t.Fatalf("Reconcile() error: %v", err)
			}
			assertAssignedTo(t, fr, "baron-x", "zen")
			assertNotAssignedTo(t, fr, "opencode")
			assertTierDowngrade(t, a, fr, "baron-x", true)
			assertActionReasonContains(t, report, "baron-x", "billing failure")
		})
	}
}

// TestReconcileBillingFreeExhausted: S2 edge free exhausted -> human_queue.
// Same billing signal but catalog only fast×1 and zero free active. Must not
// assign, must stay retry/human_queue, must not emit tier_downgrade.
func TestReconcileBillingFreeExhausted(t *testing.T) {
	cases := []struct {
		name   string
		status store.BeadStatus
	}{
		{"retry stays retry", store.BeadStatusRetry},
		{"human_queue stays human_queue", store.BeadStatusHumanQueue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beads := []store.Bead{{
				ID: "baron-x", BRN: "baron-x", Status: tc.status,
				Assignee: "claude", Metadata: map[string]string{"tier": string(agent.TierFast), "model": "sonnet"},
			}}
			fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
			a := newTestApp(t, fr)
			writeAgentRegistry(t, testClaude())
			writeCatalog(
				t,
				agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierFast},
			)
			// Same "retry" audit action for both cases: for human_queue, the
			// reconciler's own path reads lastRetryReason first (then idle),
			// so this is what makes isBilling trigger direct-free, which then
			// fails due to no free model.
			reason := "Insufficient balance (402 Payment Required)"
			if err := a.audit.Append(store.AuditEvent{
				Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "retry",
				Target: "baron-x", Detail: reason,
			}); err != nil {
				t.Fatalf("audit.Append() error: %v", err)
			}
			report, err := a.Reconcile(context.Background(), nil)
			if err != nil {
				t.Fatalf("Reconcile() error: %v", err)
			}
			assertNotAssignedTo(t, fr, "claude")
			assertNotAssignedTo(t, fr, "opencode")
			assertNotAssignedTo(t, fr, "zen")
			assertTierDowngrade(t, a, fr, "baron-x", false)
			assertTransitionToOpen(t, report, "baron-x", false)
		})
	}
}

// TestReconcileTransientStillCyclesFast: S3 adjacent transient still cycles
// fast. A 429 Rate limit with a fast sibling available and a free pool must
// reassign to the fast sibling, NOT to free. Also covers S3b idle still free
// regression: idle in human_queue still downgrades to free.
func TestReconcileTransientStillCyclesFast(t *testing.T) {
	cases := []struct {
		name            string
		status          store.BeadStatus
		reason          string
		wantAssignee    string
		wantNotAssignee string
		expectDowngrade bool
	}{
		{
			name:            "429 rate limit cycles fast sibling not free",
			status:          store.BeadStatusRetry,
			reason:          "429 Rate limit exceeded",
			wantAssignee:    "opencode",
			wantNotAssignee: "zen",
			expectDowngrade: false,
		},
		{
			name:            "idle in human_queue still downgrades to free",
			status:          store.BeadStatusHumanQueue,
			reason:          "idle: no output for 5m0s",
			wantAssignee:    "zen",
			wantNotAssignee: "opencode",
			expectDowngrade: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beads := []store.Bead{{
				ID: "baron-x", BRN: "baron-x", Status: tc.status,
				Assignee: "claude", Metadata: map[string]string{"tier": string(agent.TierFast), "model": "sonnet"},
			}}
			fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
			a := newTestApp(t, fr)
			zen2 := agent.Agent{Name: "zen", Command: "zen", Status: agent.StatusActive, Backend: "subprocess", Args: []string{"{{prompt}}"}, ModelFlag: "--model"}
			writeAgentRegistry(t, testClaude(), testOpencode(), zen2)
			writeCatalog(
				t,
				agent.Model{ID: "claude/sonnet", Agent: "claude", Name: "sonnet", Tier: agent.TierFast},
				agent.Model{ID: "opencode-go/kimi", Agent: "opencode", Name: "kimi", Tier: agent.TierFast},
				agent.Model{ID: "zen", Agent: "zen", Name: "zen", Tier: agent.TierFree},
			)
			var auditAction string
			if tc.status == store.BeadStatusHumanQueue && strings.Contains(tc.reason, "idle") {
				auditAction = "idle"
			} else {
				auditAction = "retry"
			}
			if err := a.audit.Append(store.AuditEvent{
				Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: auditAction,
				Target: "baron-x", Detail: tc.reason,
			}); err != nil {
				t.Fatalf("audit.Append() error: %v", err)
			}
			report, err := a.Reconcile(context.Background(), nil)
			if err != nil {
				t.Fatalf("Reconcile() error: %v", err)
			}
			assertAssignedTo(t, fr, "baron-x", tc.wantAssignee)
			assertNotAssignedTo(t, fr, tc.wantNotAssignee)
			if tc.expectDowngrade {
				assertDowngradedToFree(t, a, fr, report, "baron-x", tc.status == store.BeadStatusHumanQueue)
			} else {
				assertCycledFastSibling(t, a, fr, report, "baron-x")
			}
		})
	}
}
