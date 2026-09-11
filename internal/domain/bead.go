package domain

import (
	"slices"
	"time"
)

// BeadState represents the state of a bead.
type BeadState string

// BeadState constants define the valid bead lifecycle states.
const (
	BeadStateOpen       BeadState = "open"
	BeadStateAssigned   BeadState = "assigned"
	BeadStateWorking    BeadState = "working"
	BeadStateValidating BeadState = "validating"
	BeadStateRetry      BeadState = "retry"
	BeadStateBlocked    BeadState = "blocked"
	BeadStateMergable   BeadState = "mergable"
	BeadStateHumanQueue BeadState = "human_queue"
	BeadStateMerged     BeadState = "merged"
	BeadStateClosed     BeadState = "closed"
	BeadStateCancelled  BeadState = "cancelled"
)

// DepLink is one dependency edge from a bead to another.
type DepLink struct {
	IssueID     string
	DependsOnID string
	Type        string // blocks, related, parent-child, discovered-from
}

// Bead represents a work item with state.
type Bead struct {
	ID           string
	BRN          BRN
	Title        string
	Description  string
	State        BeadState
	Assignee     string // model name
	Tags         []string
	Dependencies []DepLink
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// allowedTransitions defines which state transitions are structurally
// valid, independent of actor. "any→cancelled" covers every non-terminal
// state the table enumerates. closed→open is the reopen path: a bead closed
// by mistake or prematurely isn't a dead end.
var allowedTransitions = map[BeadState]map[BeadState]bool{
	// open -> mergable: a parent bead (epic or otherwise) never runs its
	// own agent pipeline — its "gate" is its children finishing. Once the
	// last one merges into its branch, it lands here directly so a human
	// still reviews and merges the accumulated branch, same as any other
	// bead (see tryAdvanceParent).
	// assigned -> open: a manual reset/escape-hatch for a bead stuck waiting
	// (e.g. auto_start disabled, or an agent that never picked it up) — bd's
	// own status is already "open" here (assigned is derived, not a real bd
	// status; see store.Bead.DomainState), so ChangeStatus additionally
	// clears the assignee itself, or this would read right back as
	// "assigned" the moment anything re-lists it.
	BeadStateOpen:     {BeadStateAssigned: true, BeadStateBlocked: true, BeadStateCancelled: true, BeadStateMergable: true},
	BeadStateAssigned: {BeadStateWorking: true, BeadStateBlocked: true, BeadStateCancelled: true, BeadStateOpen: true},
	// working -> retry: the startup reconcile's landing spot for a "working"
	// bead whose agent process is gone (tmux window dead) — the status is a
	// lie, and retry is what lets a human or `baron run` relaunch it.
	//
	// retry -> open: the same manual reset as assigned -> open, for a bead
	// whose last attempt failed and is waiting on autoRetry/reassignment
	// instead of running.
	BeadStateWorking:    {BeadStateValidating: true, BeadStateRetry: true, BeadStateHumanQueue: true, BeadStateCancelled: true},
	BeadStateValidating: {BeadStateRetry: true, BeadStateMergable: true, BeadStateHumanQueue: true, BeadStateCancelled: true},
	BeadStateRetry:      {BeadStateWorking: true, BeadStateHumanQueue: true, BeadStateCancelled: true, BeadStateOpen: true},
	// blocked -> open: the startup reconcile's landing spot for a "blocked"
	// bead with nothing live blocking it — the status is a lie, and open is
	// what makes it actionable again instead of stranding it forever.
	BeadStateBlocked:    {BeadStateAssigned: true, BeadStateOpen: true, BeadStateCancelled: true},
	BeadStateMergable:   {BeadStateMerged: true, BeadStateWorking: true, BeadStateCancelled: true},
	BeadStateHumanQueue: {BeadStateAssigned: true, BeadStateClosed: true, BeadStateWorking: true, BeadStateCancelled: true, BeadStateOpen: true},
	// merged -> open: the closer gate's own reopen path (a persona's
	// judgment that the merge doesn't actually hold, mediated through
	// BARON's own trusted code the same way reviewGate mediates the
	// reviewer persona's verdict — see internal/cli/merge_closer.go).
	// Lands on open, not working: the bead's original worktree is gone by
	// this point (readyForMerge's callers remove it right after merging),
	// so there is nothing live to resume into — a fresh assignment starts
	// clean, same as any other open bead.
	BeadStateMerged:    {BeadStateOpen: true, BeadStateClosed: true, BeadStateCancelled: true},
	BeadStateClosed:    {BeadStateOpen: true},
	BeadStateCancelled: {},
}

// CommentType represents the type of comment.
type CommentType string

// CommentType constants define the valid comment types.
const (
	CommentInstruction CommentType = "instruction" // from actor:user
	CommentData        CommentType = "data"        // from actor:agent
)

// Comment represents a comment on a bead.
type Comment struct {
	BeadID    string
	Actor     Actor
	Type      CommentType
	Content   string
	CreatedAt time.Time
}

// BeadStateMachine manages bead state transitions.
type BeadStateMachine struct{}

// NewBeadStateMachine creates a new state machine.
func NewBeadStateMachine() *BeadStateMachine {
	return &BeadStateMachine{}
}

// CanTransition checks if a transition is allowed.
func (sm *BeadStateMachine) CanTransition(from, to BeadState) bool {
	return allowedTransitions[from][to]
}

// AllowedTargets returns the valid transition targets from a state, sorted
// for deterministic menus — except BeadStateCancelled, which always sorts
// last regardless of its alphabetical position. "cancelled" is a legal
// target from every non-terminal state (see allowedTransitions), and
// alphabetical order alone put it *first* — and so pre-selected, since the
// TUI's status picker always opens with the cursor on index 0 — for every
// "from" whose other targets all sort after "c" (working, validating,
// retry, mergable, merged). That made the fastest possible gesture on a
// bead mid-flight ('s' then 'enter') cancel it. Deterministic ordering is
// still the goal; only the destructive entry moves.
func (sm *BeadStateMachine) AllowedTargets(from BeadState) []BeadState {
	targets := make([]BeadState, 0, len(allowedTransitions[from]))
	for target := range allowedTransitions[from] {
		targets = append(targets, target)
	}
	slices.Sort(targets)
	if i := slices.Index(targets, BeadStateCancelled); i >= 0 && i != len(targets)-1 {
		targets = append(targets[:i], targets[i+1:]...)
		targets = append(targets, BeadStateCancelled)
	}
	return targets
}

// CanActorTransition checks if an actor can perform a specific transition.
// actor:user can perform any structurally valid transition; actor:agent
// (the model process) can never change bead state — that rule is v1's
// structural, non-negotiable part of the mutation-ownership model.
//
// actor:persona (a Crew Mode persona run, docs/PRD/crew-mode.md) gets the
// same rights as actor:user/actor:manager for everything except merging:
// -> BeadStateMerged is denied outright, no policy or config can grant it.
// A human-configured merge.auto policy already lets actor:manager merge on
// the human's pre-approved terms (tryAutoMerge); a persona's authority is
// scoped to managing bead state (reopen a broken bead, comment, create a
// follow-up), never to landing code — that boundary stays exclusively
// actor:user (or the one pre-approved actor:manager exception), matching
// crew-mode.md §4's "persona için ayrı bir merge-bypass eklenmez".
func (sm *BeadStateMachine) CanActorTransition(from, to BeadState, actor Actor) bool {
	if actor.Type == ActorAgent {
		return false
	}
	if actor.Type == ActorPersona && to == BeadStateMerged {
		return false
	}
	return sm.CanTransition(from, to)
}

// HasLiveBlockers checks if a bead has any live (non-done) blockers.
// A live blocker is a blocks-type dependency on a bead that is not closed/merged/cancelled.
func (sm *BeadStateMachine) HasLiveBlockers(bead *Bead, allBeads []*Bead) bool {
	if bead == nil {
		return false
	}
	beadByBRN := make(map[BRN]*Bead, len(allBeads))
	for _, b := range allBeads {
		beadByBRN[b.BRN] = b
	}
	for _, dep := range bead.Dependencies {
		if dep.Type != "blocks" || dep.DependsOnID == "" {
			continue
		}
		if blocker, ok := beadByBRN[BRN(dep.DependsOnID)]; ok {
			if !beadDone(blocker.State) {
				return true
			}
		}
	}
	return false
}

// ShouldBeBlocked returns true if the bead has live blockers and is not already blocked/terminal.
func (sm *BeadStateMachine) ShouldBeBlocked(bead *Bead, allBeads []*Bead) bool {
	if bead == nil || beadDone(bead.State) {
		return false
	}
	return sm.HasLiveBlockers(bead, allBeads)
}

// ShouldBeUnblocked returns true if the bead is blocked but has no live blockers.
func (sm *BeadStateMachine) ShouldBeUnblocked(bead *Bead, allBeads []*Bead) bool {
	if bead == nil || bead.State != BeadStateBlocked {
		return false
	}
	return !sm.HasLiveBlockers(bead, allBeads)
}

// beadDone reports whether a bead state is terminal (done).
func beadDone(s BeadState) bool {
	switch s {
	case BeadStateClosed, BeadStateMerged, BeadStateCancelled:
		return true
	default:
		return false
	}
}

// ReconcileBlockedStatus updates beads' blocked status based on their dependencies.
// Returns a list of (bead BRN, new state) for beads that should change.
func (sm *BeadStateMachine) ReconcileBlockedStatus(allBeads []*Bead) [][2]string {
	var changes [][2]string
	for _, b := range allBeads {
		if b == nil {
			continue
		}
		if sm.ShouldBeBlocked(b, allBeads) && b.State != BeadStateBlocked {
			changes = append(changes, [2]string{string(b.BRN), string(BeadStateBlocked)})
		} else if sm.ShouldBeUnblocked(b, allBeads) {
			changes = append(changes, [2]string{string(b.BRN), string(BeadStateOpen)})
		}
	}
	return changes
}
