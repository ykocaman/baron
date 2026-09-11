package tui

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// TestSpawnAgentTerminalUsesPassedBeadsNotDepsBeads: beads must come from
// the Model's own already-loaded list (m.beads), never a fresh `bd list`
// inside the spawn itself — that subprocess alone measured ~3s against a
// real project's bd store, and blocking a spawn on it (instead of the
// render loop's own periodic reload) is what made the Agent/Diff tabs
// visibly slow to open. Proven the hard way: deps.Beads is nil here, so
// any reintroduced deps.Beads.List call would nil-pointer-panic instead of
// quietly passing.
func TestSpawnAgentTerminalUsesPassedBeadsNotDepsBeads(t *testing.T) {
	deps := testDeps()
	deps.Beads = nil
	beads := []store.Bead{{BRN: "baron-a", ID: "a", IssueType: "task", Status: store.BeadStatusAssigned}}
	cmd := spawnAgentTerminal(context.Background(), deps, beads, "baron-a", 80, 24, "")
	msg, ok := cmd().(agentSpawnedMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want agentSpawnedMsg", cmd())
	}
	// Assignee == "" on the passed-in bead — a clean early exit before any
	// worktree/process work, so this proves the lookup itself (finding
	// "baron-a" and reading its fields) came from beads, not deps.Beads.
	if !errors.Is(msg.err, domain.ErrNoAgent) {
		t.Fatalf("err = %v, want domain.ErrNoAgent (bead found via the passed beads slice, unassigned)", msg.err)
	}
}

// TestSpawnDiffTerminalUsesPassedBeadsNotDepsBeads: same proof as above,
// for the Diff tab's spawn — see its sibling's doc comment.
func TestSpawnDiffTerminalUsesPassedBeadsNotDepsBeads(t *testing.T) {
	deps := testDeps()
	deps.Beads = nil
	deps.Worktrees = nil // stay on deps.Dir; no git operations needed for this check
	beads := []store.Bead{{BRN: "baron-a", ID: "a", IssueType: "task", Status: store.BeadStatusMergable}}
	// Not asserting on the result — hunk may or may not be on PATH in the
	// test environment, and either a clean spawn or a clean "not found"
	// error is fine. What this guards against is a nil-pointer panic from
	// a reintroduced deps.Beads.List call, which cmd() would surface
	// immediately as a test failure (an uncaught panic fails the test).
	cmd := spawnDiffTerminal(context.Background(), deps, beads, "baron-a", 80, 24)
	cmd()
}

// TestDiffArgvForNoTarget: with no resolvable base branch (deps.BaseBranch
// itself unset), the Diff tab falls back to the old targetless behavior —
// still a valid, well-formed hunk invocation.
func TestDiffArgvForNoTarget(t *testing.T) {
	argv := diffArgvFor("")
	want := []string{"hunk", "diff", "--pager", "--no-hunk-headers", "--watch", "--mode", "stack", "--", ":!.omo"}
	if !slices.Equal(argv, want) {
		t.Fatalf("diffArgvFor(\"\") = %v, want %v", argv, want)
	}
}

// TestDiffArgvForWithTarget: the fix's whole point — a resolved base branch
// must land as an explicit hunk diff target (before the "--" pathspec
// separator), not get silently dropped back to a targetless working-tree
// diff. See diffArgvFor's own doc comment for why a targetless diff goes
// empty on any bead whose work is already committed (mergable and beyond).
func TestDiffArgvForWithTarget(t *testing.T) {
	argv := diffArgvFor("main")
	want := []string{"hunk", "diff", "--pager", "--no-hunk-headers", "--watch", "--mode", "stack", "main", "--", ":!.omo"}
	if !slices.Equal(argv, want) {
		t.Fatalf("diffArgvFor(\"main\") = %v, want %v", argv, want)
	}
}

// TestMergeBaseBranchRootFallsBackToProjectBase: a bead with no parent has
// nothing for mergeBaseFor to resolve — mergeBaseBranch must still return a
// real, usable branch name (the project's own configured base branch)
// rather than propagating mergeBaseFor's "" straight through, which is
// exactly what the Diff tab's target resolution depends on.
func TestMergeBaseBranchRootFallsBackToProjectBase(t *testing.T) {
	root := store.Bead{BRN: "baron-a", ID: "baron-a"}
	got := mergeBaseBranch(root, []store.Bead{root}, "main")
	if got != "main" {
		t.Fatalf("mergeBaseBranch(root bead) = %q, want %q", got, "main")
	}
}

// TestMergeBaseBranchChildUsesParentBranch: a hierarchical child integrates
// into its parent's branch first, never straight into the project base —
// mergeBaseBranch must prefer that over the projectBase fallback.
func TestMergeBaseBranchChildUsesParentBranch(t *testing.T) {
	parent := store.Bead{BRN: "baron-a", ID: "baron-a", IssueType: "feature"}
	child := store.Bead{BRN: "baron-a.1", ID: "baron-a.1", Parent: "baron-a", IssueType: "task"}
	got := mergeBaseBranch(child, []store.Bead{parent, child}, "main")
	want := "feature/a"
	if got != want {
		t.Fatalf("mergeBaseBranch(child bead) = %q, want %q", got, want)
	}
}

// TestDiffAgainstRecordedMergeOnlyForMergedAndClosed: the Diff tab's two
// modes — recorded-SHA (main checkout, no live worktree left) vs live
// worktree diff against the merge-base branch — split on bead status.
// Every status except merged/closed must still have its own worktree and
// branch to diff live, mergable included — that's the exact case this fix
// addresses (a mergable bead is fully committed but not yet merged, so its
// branch's own diff against base is what the Diff tab must show, not
// nothing). Table covers every store.BeadStatus so a newly added status
// doesn't silently fall on the wrong side of this split.
func TestDiffAgainstRecordedMergeOnlyForMergedAndClosed(t *testing.T) {
	cases := []struct {
		status store.BeadStatus
		want   bool
	}{
		{store.BeadStatusOpen, false},
		{store.BeadStatusAssigned, false},
		{store.BeadStatusWorking, false},
		{store.BeadStatusValidating, false},
		{store.BeadStatusRetry, false},
		{store.BeadStatusBlocked, false},
		{store.BeadStatusMergable, false},
		{store.BeadStatusHumanQueue, false},
		{store.BeadStatusMerged, true},
		{store.BeadStatusClosed, true},
		{store.BeadStatusCancelled, false},
	}
	for _, c := range cases {
		if got := diffAgainstRecordedMerge(c.status); got != c.want {
			t.Errorf("diffAgainstRecordedMerge(%s) = %v, want %v", c.status, got, c.want)
		}
	}
}
