package domain

import (
	"slices"
	"testing"
)

func TestBeadCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from BeadState
		to   BeadState
		want bool
	}{
		{"open to assigned", BeadStateOpen, BeadStateAssigned, true},
		{"assigned to blocked", BeadStateAssigned, BeadStateBlocked, true},
		{"blocked to assigned", BeadStateBlocked, BeadStateAssigned, true},
		{"pr open to working (request changes)", BeadStateMergable, BeadStateWorking, true},
		{"human queue to assigned (reassign)", BeadStateHumanQueue, BeadStateAssigned, true},
		{"human queue to closed (cancel/complete)", BeadStateHumanQueue, BeadStateClosed, true},
		{"validating to cancelled", BeadStateValidating, BeadStateCancelled, true},
		{"retry to cancelled", BeadStateRetry, BeadStateCancelled, true},
		{"pr open to cancelled", BeadStateMergable, BeadStateCancelled, true},
		{"merged to cancelled", BeadStateMerged, BeadStateCancelled, true},
		{"working to validating", BeadStateWorking, BeadStateValidating, true},
		{"working to human queue", BeadStateWorking, BeadStateHumanQueue, true},
		{"working to retry (agent gone)", BeadStateWorking, BeadStateRetry, true},
		{"validating to retry", BeadStateValidating, BeadStateRetry, true},
		{"validating to pr open", BeadStateValidating, BeadStateMergable, true},
		{"validating to human queue", BeadStateValidating, BeadStateHumanQueue, true},
		{"retry to working", BeadStateRetry, BeadStateWorking, true},
		{"retry to human queue", BeadStateRetry, BeadStateHumanQueue, true},
		{"pr open to merged", BeadStateMergable, BeadStateMerged, true},
		{"human queue to working (retry)", BeadStateHumanQueue, BeadStateWorking, true},
		{"merged to closed", BeadStateMerged, BeadStateClosed, true},
		{"working to blocked (not in PRD)", BeadStateWorking, BeadStateBlocked, false},
		{"validating to closed (not in PRD)", BeadStateValidating, BeadStateClosed, false},
		{"retry to blocked (not in PRD)", BeadStateRetry, BeadStateBlocked, false},
		{"human queue to pr open (not in PRD)", BeadStateHumanQueue, BeadStateMergable, false},
		{"open to closed", BeadStateOpen, BeadStateClosed, false},
		{"assigned to open (manual reset escape hatch)", BeadStateAssigned, BeadStateOpen, true},
		{"retry to open (manual reset escape hatch)", BeadStateRetry, BeadStateOpen, true},
		{"blocked to open (unblock)", BeadStateBlocked, BeadStateOpen, true},
		{"closed to open", BeadStateClosed, BeadStateOpen, true},
		{"unknown to assigned", BeadState("unknown"), BeadStateAssigned, false},
	}
	sm := NewBeadStateMachine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sm.CanTransition(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestBeadCanActorTransition(t *testing.T) {
	tests := []struct {
		name  string
		from  BeadState
		to    BeadState
		actor Actor
		want  bool
	}{
		{"user assigns", BeadStateOpen, BeadStateAssigned, Actor{Type: ActorUser, Name: "alice"}, true},
		{"user blocks", BeadStateAssigned, BeadStateBlocked, Actor{Type: ActorUser, Name: "alice"}, true},
		{"user unblocks", BeadStateBlocked, BeadStateAssigned, Actor{Type: ActorUser, Name: "alice"}, true},
		{"user unblocks to open", BeadStateBlocked, BeadStateOpen, Actor{Type: ActorUser, Name: "alice"}, true},
		{"manager retries", BeadStateValidating, BeadStateRetry, Actor{Type: ActorManager, Name: "baron"}, true},
		{"agent assigns", BeadStateOpen, BeadStateAssigned, Actor{Type: ActorAgent, Name: "claude-3.5"}, false},
		{"agent blocks", BeadStateAssigned, BeadStateBlocked, Actor{Type: ActorAgent, Name: "claude-3.5"}, false},
		{"user open to closed", BeadStateOpen, BeadStateClosed, Actor{Type: ActorUser, Name: "alice"}, false},
		{"user closed to open", BeadStateClosed, BeadStateOpen, Actor{Type: ActorUser, Name: "alice"}, true},
		{"persona reopens", BeadStateClosed, BeadStateOpen, Actor{Type: ActorPersona, Name: "qa-chromium"}, true},
		{"persona blocks", BeadStateAssigned, BeadStateBlocked, Actor{Type: ActorPersona, Name: "qa-chromium"}, true},
		{"persona cannot merge", BeadStateMergable, BeadStateMerged, Actor{Type: ActorPersona, Name: "qa-chromium"}, false},
	}
	sm := NewBeadStateMachine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sm.CanActorTransition(tt.from, tt.to, tt.actor); got != tt.want {
				t.Fatalf("CanActorTransition(%q, %q, %v) = %v, want %v", tt.from, tt.to, tt.actor, got, tt.want)
			}
		})
	}
}

func TestBeadAllowedTargets(t *testing.T) {
	tests := []struct {
		name string
		from BeadState
		want []BeadState
	}{
		{"open sorted lexical, cancelled last (assigned<blocked<mergable<cancelled)", BeadStateOpen, []BeadState{BeadStateAssigned, BeadStateBlocked, BeadStateMergable, BeadStateCancelled}},
		{"assigned sorted lexical, cancelled last (blocked<open<working<cancelled) — open is the manual reset escape hatch", BeadStateAssigned, []BeadState{BeadStateBlocked, BeadStateOpen, BeadStateWorking, BeadStateCancelled}},
		{"blocked sorted lexical, cancelled last (assigned<open<cancelled)", BeadStateBlocked, []BeadState{BeadStateAssigned, BeadStateOpen, BeadStateCancelled}},
		{"working sorted lexical, cancelled last (human_queue<retry<validating<cancelled)", BeadStateWorking, []BeadState{BeadStateHumanQueue, BeadStateRetry, BeadStateValidating, BeadStateCancelled}},
		{"retry sorted lexical, cancelled last (human_queue<open<working<cancelled) — open is the manual reset escape hatch", BeadStateRetry, []BeadState{BeadStateHumanQueue, BeadStateOpen, BeadStateWorking, BeadStateCancelled}},
		{"validating sorted lexical, cancelled last (human_queue<mergable<retry<cancelled)", BeadStateValidating, []BeadState{BeadStateHumanQueue, BeadStateMergable, BeadStateRetry, BeadStateCancelled}},
		{"closed", BeadStateClosed, []BeadState{BeadStateOpen}},
		{"cancelled", BeadStateCancelled, []BeadState{}},
		{"unknown", BeadState("nonsense"), []BeadState{}},
	}
	sm := NewBeadStateMachine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sm.AllowedTargets(tt.from)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("AllowedTargets(%q) = %v, want %v", tt.from, got, tt.want)
			}
		})
	}
}
