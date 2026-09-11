package cli

import (
	"context"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// TestReconcileIgnoresRestAndTerminalStates: open, assigned, human_queue,
// merged, closed and cancelled beads are never touched — they're either
// rest states with nothing to detect, or genuinely need a human decision.
func TestReconcileIgnoresRestAndTerminalStates(t *testing.T) {
	beads := []store.Bead{
		testBead("baron-1", store.BeadStatusOpen),
		testBead("baron-2", store.BeadStatusHumanQueue),
		testBead("baron-3", store.BeadStatusMerged),
		testBead("baron-4", store.BeadStatusClosed),
		testBead("baron-5", store.BeadStatusCancelled),
	}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(report.Actions) != 0 {
		t.Errorf("Actions = %+v, want none", report.Actions)
	}
}

// TestLastStatusChangeFindsMostRecent: lastStatusChange returns the latest
// matching "status" audit event, ignoring unrelated actions and other
// target beads.
func TestLastStatusChangeFindsMostRecent(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	older := time.Now().Add(-time.Hour)
	newer := time.Now().Add(-time.Minute)
	events := []store.AuditEvent{
		{Time: older, Actor: store.Actor{Type: store.ActorUser, Name: "x"}, Action: "status", Target: "baron-x", Detail: "retry -> validating"},
		{Time: newer, Actor: store.Actor{Type: store.ActorManager, Name: "baron"}, Action: "status", Target: "baron-x", Detail: "retry -> validating"},
		{Time: time.Now(), Actor: store.Actor{Type: store.ActorUser, Name: "x"}, Action: "status", Target: "baron-x", Detail: "validating -> mergable"},
		{Time: time.Now(), Actor: store.Actor{Type: store.ActorUser, Name: "x"}, Action: "status", Target: "baron-other", Detail: "retry -> validating"},
	}
	for _, e := range events {
		if err := a.audit.Append(e); err != nil {
			t.Fatalf("audit.Append() error: %v", err)
		}
	}
	got, ok := a.lastStatusChange("baron-x", "validating")
	if !ok {
		t.Fatal("lastStatusChange() ok = false, want true")
	}
	if !got.Equal(newer) {
		t.Errorf("lastStatusChange() = %v, want the newer of the two matching events (%v)", got, newer)
	}
}

// TestLastStatusChangeNoMatch: no matching event means ok=false, not a
// zero-value time treated as "just happened".
func TestLastStatusChangeNoMatch(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	if _, ok := a.lastStatusChange("baron-x", "validating"); ok {
		t.Error("lastStatusChange() ok = true, want false (no audit history at all)")
	}
}

// TestRetryAttemptCountCountsRetryActions: retryAttemptCount counts only
// "retry" audit entries for the given BRN.
func TestRetryAttemptCountCountsRetryActions(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	events := []store.AuditEvent{
		{Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "retry", Target: "baron-x"},
		{Time: time.Now(), Actor: store.Actor{Type: store.ActorManager}, Action: "retry", Target: "baron-x"},
		{Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "status", Target: "baron-x"},
		{Time: time.Now(), Actor: store.Actor{Type: store.ActorUser}, Action: "retry", Target: "baron-other"},
	}
	for _, e := range events {
		if err := a.audit.Append(e); err != nil {
			t.Fatalf("audit.Append() error: %v", err)
		}
	}
	if got := a.retryAttemptCount("baron-x"); got != 2 {
		t.Errorf("retryAttemptCount() = %d, want 2", got)
	}
}

// TestRetryAttemptCountResetsAfterMergeReady: a bead that reached
// mergable proved its retry cycle actually worked, so a later, unrelated
// retry starts counting from zero again instead of picking up where an
// already-resolved incident left off.
func TestRetryAttemptCountResetsAfterMergeReady(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	now := time.Now()
	events := []store.AuditEvent{
		// First incident: two retries, then it recovered on its own and
		// reached mergable.
		{Time: now, Actor: store.Actor{Type: store.ActorManager}, Action: "retry", Target: "baron-x", Detail: "retry 1/3: silent death"},
		{Time: now.Add(time.Minute), Actor: store.Actor{Type: store.ActorManager}, Action: "retry", Target: "baron-x", Detail: "retry 2/3: silent death"},
		{Time: now.Add(2 * time.Minute), Actor: store.Actor{Type: store.ActorManager}, Action: "status", Target: "baron-x", Detail: "working -> mergable"},
		// A brand new, unrelated failure well after that success.
		{Time: now.Add(time.Hour), Actor: store.Actor{Type: store.ActorManager}, Action: "retry", Target: "baron-x", Detail: "retry 1/3: silent death"},
	}
	for _, e := range events {
		if err := a.audit.Append(e); err != nil {
			t.Fatalf("audit.Append() error: %v", err)
		}
	}
	if got := a.retryAttemptCount("baron-x"); got != 1 {
		t.Errorf("retryAttemptCount() = %d, want 1 (only the retry after the last mergable counts)", got)
	}
}

// TestReconcileStateDriftReverts: a bead BARON last recorded as "working"
// that bd now reports as "closed" — as if something ran `bd close`
// directly, bypassing baron entirely — gets reverted back to "working" and
// audited as state_drift. This is the "authority drift" check
// (reconcileStateDrift): the independent second layer behind
// internal/domain.TaskAgentEnv's BEADS_DB isolation (see
// docs/PRD/harness-hardening.md §3), in case that ever fails or gets
// bypassed.
func TestReconcileStateDriftReverts(t *testing.T) {
	beads := []store.Bead{
		{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusClosed},
	}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	a.recordStateSnapshot("baron-x", domain.BeadStateWorking) // BARON's last known-good state

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}

	var drift *ReconcileAction
	for i := range report.Actions {
		if report.Actions[i].BRN == "baron-x" {
			drift = &report.Actions[i]
		}
	}
	if drift == nil {
		t.Fatalf("Actions = %+v, want a state_drift revert for baron-x", report.Actions)
	}
	if drift.From != domain.BeadStateClosed || drift.To != domain.BeadStateWorking {
		t.Errorf("drift action = %+v, want closed -> working", drift)
	}
	if !hasCall(fr, "update", "baron-x", "--status", "in_progress") {
		t.Errorf("calls = %v, want bd reverting baron-x's status to working (bd's own \"in_progress\")", fr.calls)
	}

	found := false
	for _, e := range auditEvents(t, a) {
		if e.Target == "baron-x" && e.Action == "state_drift" {
			found = true
			if e.Actor.Type != store.ActorManager {
				t.Errorf("state_drift event actor = %q, want %q", e.Actor.Type, store.ActorManager)
			}
		}
	}
	if !found {
		t.Errorf("audit events = %+v, want a state_drift event for baron-x", auditEvents(t, a))
	}
}

// TestReconcileStateDriftSeedsUnseenBead: a bead Reconcile has never
// observed before (no snapshot entry — e.g. created via a bare `bd create`
// bypassing baron work create, a known/accepted gap, see bd remember
// baron-tier-enforcement-layer) must not be flagged as drift on first
// sight; it's only seeded into the snapshot so a *future* mismatch has
// something real to compare against.
func TestReconcileStateDriftSeedsUnseenBead(t *testing.T) {
	beads := []store.Bead{
		{ID: "baron-y", BRN: "baron-y", Status: store.BeadStatusHumanQueue},
	}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(report.Actions) != 0 {
		t.Errorf("Actions = %+v, want none (first observation only seeds the snapshot)", report.Actions)
	}
	if hasCall(fr, "update", "baron-y", "--status", "human_queue") {
		t.Errorf("calls = %v, want no revert attempt for a never-before-seen bead", fr.calls)
	}
	snap := a.loadStateSnapshot()
	if got := snap["baron-y"]; got != domain.BeadStateHumanQueue {
		t.Errorf("snapshot[baron-y] = %q, want %q (seeded from first observation)", got, domain.BeadStateHumanQueue)
	}
}

// TestReconcileAssignedReportsNeedsStartWhenDisabled: with general.auto_start at
// its default (false), an "assigned" bead is reported as needing a start but
// nothing is spawned — the safe default per the design's runaway-risk analysis.
func TestReconcileAssignedReportsNeedsStartWhenDisabled(t *testing.T) {
	beads := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusOpen, Assignee: "claude"}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	a.loadConfig = func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.General.AutoStart = false
		return cfg, nil
	}

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(report.Actions) != 1 {
		t.Fatalf("Actions = %+v, want 1 (baron-x needs start)", report.Actions)
	}
	action := report.Actions[0]
	if action.BRN != "baron-x" || action.From != domain.BeadStateAssigned || action.To != domain.BeadStateAssigned {
		t.Errorf("action = %+v, want baron-x assigned -> assigned", action)
	}
	if action.Reason != "needs start (general.auto_start is disabled)" {
		t.Errorf("reason = %q, want %q", action.Reason, "needs start (general.auto_start is disabled)")
	}
	if hasCall(fr, "worktree", "add") {
		t.Errorf("calls = %v, want no launch (auto_start defaults to false)", fr.calls)
	}
}

// TestReconcileAssignedAutoStartsWhenEnabled: with general.auto_start enabled,
// an "assigned" bead is started the same way `baron run` would start it.
func TestReconcileAssignedAutoStartsWhenEnabled(t *testing.T) {
	runner := &runGateRunner{beadJSON: `[{"id":"baron-a1b2c3","title":"Implement auth","status":"open","priority":2,"issue_type":"task","assignee":"claude"}]`, modelCmd: "claude"}
	a := newTestApp(t, runner)
	a.loadConfig = func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.General.AutoStart = true
		return cfg, nil
	}
	writeAgentRegistry(t, testClaude())

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(runner.modelCall) == 0 {
		t.Fatalf("modelCall = %v, want the model launched (auto_start enabled)", runner.modelCall)
	}
	if len(report.Actions) != 1 {
		t.Fatalf("Actions = %+v, want 1 (baron-a1b2c3 auto-started)", report.Actions)
	}
	action := report.Actions[0]
	if action.BRN != "baron-a1b2c3" || action.To != domain.BeadStateWorking || action.Reason != "auto-started" {
		t.Errorf("action = %+v, want baron-a1b2c3 assigned -> working (auto-started)", action)
	}
}

// TestReconcileAssignedSkipsInactiveAgent: with general.auto_start enabled but
// the assigned agent CLI not present in the registry, the bead is reported as
// having an inactive agent and nothing is launched.
func TestReconcileAssignedSkipsInactiveAgent(t *testing.T) {
	beads := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusOpen, Assignee: "claude"}}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	a.loadConfig = func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.General.AutoStart = true
		return cfg, nil
	}

	report, err := a.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(report.Actions) != 1 {
		t.Fatalf("Actions = %+v, want 1 (baron-x agent inactive)", report.Actions)
	}
	action := report.Actions[0]
	if action.BRN != "baron-x" || action.Reason != "agent inactive" {
		t.Errorf("action = %+v, want baron-x agent inactive", action)
	}
	if hasCall(fr, "worktree", "add") {
		t.Errorf("calls = %v, want no launch for an inactive agent", fr.calls)
	}
}

// TestReconcileAssignedSkipsBlockedBead: a bead with live blockers must not be
// auto-started even when general.auto_start is enabled — the dependency check
// silently skips it before any launch decision.
func TestReconcileAssignedSkipsBlockedBead(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	storeBead := store.Bead{
		ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusOpen, Assignee: "claude",
		Dependencies: []store.DepLink{{IssueID: "baron-x", DependsOnID: "baron-blocker", Type: "blocks"}},
	}
	domainBeads := []*domain.Bead{
		{BRN: "baron-blocker", State: domain.BeadStateOpen},
		storeBeadToDomain(&storeBead),
	}
	var report ReconcileReport
	a.reconcileAssigned(context.Background(), &storeBead, domain.BeadStateAssigned, domainBeads, true, &report)
	if len(report.Actions) != 0 {
		t.Errorf("Actions = %+v, want none (blocked bead silently skipped)", report.Actions)
	}
}
