package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

func makeBeadList(beads []store.Bead) string {
	b, _ := json.Marshal(beads)
	return string(b)
}

func testBead(id string, status store.BeadStatus) store.Bead {
	return store.Bead{ID: id, Status: status}
}

func TestStoreActorConvertsManager(t *testing.T) {
	got := storeActor(domain.Actor{Type: domain.ActorManager, Name: "baron"})
	want := store.Actor{Type: store.ActorManager, Name: "baron"}
	if got != want {
		t.Errorf("storeActor(manager) = %+v, want %+v", got, want)
	}
}

func TestManagerActorIsNotSystemActor(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	if a.managerActor().Type != domain.ActorManager {
		t.Errorf("managerActor().Type = %q, want %q", a.managerActor().Type, domain.ActorManager)
	}
	if a.systemActor().Type != domain.ActorUser {
		t.Errorf("systemActor().Type = %q, want %q", a.systemActor().Type, domain.ActorUser)
	}
}

func TestToDomainStateIdentity(t *testing.T) {
	pairs := []struct {
		store store.BeadStatus
		want  domain.BeadState
	}{
		{store.BeadStatusOpen, domain.BeadStateOpen},
		{store.BeadStatusWorking, domain.BeadStateWorking},
		{store.BeadStatusValidating, domain.BeadStateValidating},
		{store.BeadStatusRetry, domain.BeadStateRetry},
		{store.BeadStatusBlocked, domain.BeadStateBlocked},
		{store.BeadStatusMergable, domain.BeadStateMergable},
		{store.BeadStatusHumanQueue, domain.BeadStateHumanQueue},
		{store.BeadStatusMerged, domain.BeadStateMerged},
		{store.BeadStatusClosed, domain.BeadStateClosed},
		{store.BeadStatusCancelled, domain.BeadStateCancelled},
	}
	for _, p := range pairs {
		if got := toDomainState(p.store); got != p.want {
			t.Errorf("toDomainState(%q) = %q, want %q", p.store, got, p.want)
		}
	}
}

func TestToStoreStatusRoundTrip(t *testing.T) {
	if got := toStoreStatus(domain.BeadStateWorking); got != store.BeadStatusWorking {
		t.Errorf("toStoreStatus(Working) = %q, want %q", got, store.BeadStatusWorking)
	}
	if got := toStoreStatus(domain.BeadStateValidating); got != store.BeadStatusValidating {
		t.Errorf("toStoreStatus(Validating) = %q, want %q", got, store.BeadStatusValidating)
	}
}

func TestResolveDomainStateAssignedViaAssignee(t *testing.T) {
	b := store.Bead{Status: store.BeadStatusOpen, Assignee: "claude"}
	if got := resolveDomainState(b); got != domain.BeadStateAssigned {
		t.Errorf("resolveDomainState(open+assignee) = %q, want assigned (bd assign never changes status)", got)
	}
}

func TestResolveDomainStateOpenWithoutAssignee(t *testing.T) {
	b := store.Bead{Status: store.BeadStatusOpen}
	if got := resolveDomainState(b); got != domain.BeadStateOpen {
		t.Errorf("resolveDomainState(open, no assignee) = %q, want open", got)
	}
}

func TestTransitionStatusNoOpSameState(t *testing.T) {
	beads := []store.Bead{testBead("baron-x", store.BeadStatusWorking)}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	if err := a.transitionStatus(context.Background(), "baron-x", domain.BeadStateWorking, domain.BeadStateWorking, a.systemActor()); err != nil {
		t.Fatalf("transitionStatus() error on a no-op: %v", err)
	}
}

func TestTransitionStatusAllowsWorkingToRetry(t *testing.T) {
	beads := []store.Bead{testBead("baron-x", store.BeadStatusWorking)}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	if err := a.transitionStatus(t.Context(), "baron-x", domain.BeadStateWorking, domain.BeadStateRetry, a.systemActor()); err != nil {
		t.Fatalf("transitionStatus(working -> retry) error: %v", err)
	}
	if !hasCall(fr, "update", "baron-x", "--status", "retry") {
		t.Errorf("calls = %v, want bd update --status retry", fr.calls)
	}
}

func TestTransitionStatusRejectsInvalidJump(t *testing.T) {
	beads := []store.Bead{testBead("baron-x", store.BeadStatusWorking)}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	err := a.transitionStatus(t.Context(), "baron-x", domain.BeadStateWorking, domain.BeadStateMerged, a.systemActor())
	if err == nil {
		t.Fatal("transitionStatus(working -> merged): want an error, that edge isn't in the graph")
	}
	if !strings.Contains(err.Error(), "working -> merged") {
		t.Errorf("error = %v, want it to name the rejected transition", err)
	}
}

// landing spot for the startup blocked-reconcile: open-without-assignee
// stays the literal open status (bd assign never touches status).
func TestTransitionStatusAllowsBlockedToOpen(t *testing.T) {
	beads := []store.Bead{testBead("baron-x", store.BeadStatusBlocked)}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	if err := a.transitionStatus(t.Context(), "baron-x", domain.BeadStateBlocked, domain.BeadStateOpen, a.systemActor()); err != nil {
		t.Fatalf("transitionStatus(blocked -> open) error: %v", err)
	}
	if !hasCall(fr, "update", "baron-x", "--status", "open") {
		t.Errorf("calls = %v, want bd update --status open", fr.calls)
	}
}

func TestTransitionStatusAllowsValidJump(t *testing.T) {
	beads := []store.Bead{testBead("baron-x", store.BeadStatusAssigned)}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	if err := a.transitionStatus(t.Context(), "baron-x", domain.BeadStateAssigned, domain.BeadStateWorking, a.systemActor()); err != nil {
		t.Fatalf("transitionStatus(assigned -> working) error: %v", err)
	}
	if !hasCall(fr, "update", "baron-x", "--status", "in_progress") {
		t.Errorf("calls = %v, want bd update --status in_progress", fr.calls)
	}
}

// TestTransitionStatusRejectsAgentActor: — actor:agent can
// never change bead state, even for an otherwise-valid transition. This is
// the structural enforcement point the mutation-ownership rule depends on.
func TestTransitionStatusRejectsAgentActor(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	agent := domain.Actor{Type: domain.ActorAgent, Name: "claude-3.5"}
	err := a.transitionStatus(t.Context(), "baron-x", domain.BeadStateAssigned, domain.BeadStateWorking, agent)
	if err == nil {
		t.Fatal("transitionStatus(assigned -> working, actor:agent): want an error, agents cannot change bead state")
	}
	if hasCall(fr, "update", "baron-x", "--status", "in_progress") {
		t.Error("recordFailure wrote a status update for a rejected actor:agent transition")
	}
}

func TestRecordFailureFromWorkingStaysWorking(t *testing.T) {
	beads := []store.Bead{testBead("baron-x", store.BeadStatusWorking)}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	attempt := 0
	exhausted, newState, err := a.recordFailure(context.Background(), "baron-x", domain.BeadStateWorking, &attempt, 3, "silent death", a.systemActor())
	if err != nil {
		t.Fatalf("recordFailure() error: %v", err)
	}
	if exhausted {
		t.Fatal("exhausted = true on the first attempt, want false (budget 3)")
	}
	// has no working->retry edge; a silent-death retry
	// must not attempt that invalid transition and must stay at working.
	if newState != domain.BeadStateWorking {
		t.Errorf("newState = %q, want working (no retry edge from working)", newState)
	}
	if hasCall(fr, "update", "baron-x", "--status", "retry") {
		t.Error("recordFailure wrote an invalid working->retry status")
	}
}

func TestRecordFailureFromValidatingGoesToRetry(t *testing.T) {
	beads := []store.Bead{testBead("baron-x", store.BeadStatusValidating)}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	attempt := 0
	exhausted, newState, err := a.recordFailure(context.Background(), "baron-x", domain.BeadStateValidating, &attempt, 3, "gate failed", a.systemActor())
	if err != nil {
		t.Fatalf("recordFailure() error: %v", err)
	}
	if exhausted {
		t.Fatal("exhausted = true on the first attempt, want false")
	}
	if newState != domain.BeadStateRetry {
		t.Errorf("newState = %q, want retry (validating->retry is a real edge)", newState)
	}
	// bd has no built-in "retry" status; it's registered as a custom status
	// (bd config set status.custom) so retry is written literally and reads
	// back as retry, not working.
	if !hasCall(fr, "update", "baron-x", "--status", "retry") {
		t.Errorf("calls = %v, want bd update --status retry (retry is a custom bd status)", fr.calls)
	}
}

func TestRecordFailureExhaustedFromWorking(t *testing.T) {
	beads := []store.Bead{testBead("baron-x", store.BeadStatusWorking)}
	fr := &fakeRunner{out: func(args []string) string { return makeBeadList(beads) }}
	a := newTestApp(t, fr)
	attempt := 3 // already at budget
	exhausted, newState, err := a.recordFailure(context.Background(), "baron-x", domain.BeadStateWorking, &attempt, 3, "silent death", a.systemActor())
	if err != nil {
		t.Fatalf("recordFailure() error: %v", err)
	}
	if !exhausted {
		t.Fatal("exhausted = false, want true at budget")
	}
	if newState != domain.BeadStateHumanQueue {
		t.Errorf("newState = %q, want human_queue (working->human_queue is a real edge)", newState)
	}
}
