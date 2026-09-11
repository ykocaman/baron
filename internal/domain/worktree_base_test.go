package domain

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// baseRunner answers the worktree/rev-parse sequence Create makes, with the
// branch and base-ref probes controlled independently — the shared
// scriptedRunner fails every rev-parse at once, which cannot express "the
// bead branch is new but the base ref resolves".
type baseRunner struct {
	branchExists bool
	baseResolves bool
	calls        [][]string
}

func (r *baseRunner) Run(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	switch {
	case len(args) == 0:
		return tool.Result{}, nil
	case args[0] == "worktree" && len(args) > 1 && args[1] == "list":
		return tool.Result{}, nil
	case args[0] == "rev-parse" && slices.Contains(args, "refs/heads/task/b1"):
		if !r.branchExists {
			return tool.Result{ExitCode: 1}, errors.New("exit status 1")
		}
		return tool.Result{}, nil
	case args[0] == "rev-parse":
		if !r.baseResolves {
			return tool.Result{ExitCode: 1}, errors.New("exit status 1")
		}
		return tool.Result{}, nil
	default:
		return tool.Result{}, nil
	}
}

// addArgs returns the `git worktree add` invocation, joined for matching.
func (r *baseRunner) addArgs(t *testing.T) string {
	t.Helper()
	for _, c := range r.calls {
		if len(c) > 2 && c[1] == "worktree" && c[2] == "add" {
			return strings.Join(c, " ")
		}
	}
	t.Fatalf("no `git worktree add` in %v", r.calls)
	return ""
}

func newBaseManager(runner tool.Runner) *WorktreeManager {
	m := NewWorktreeManager(runner, "/repo", "/repo/.baron/worktrees")
	m.BaseBranch = "main"
	return m
}

// TestCreateForksFromLocalBase is the regression test for the stale-base
// bug: a new bead branch forked from the local checkout's HEAD, which is
// wrong the moment BARON has merged anything into the base branch since the
// checkout was last touched. Forking from the base branch directly (`baron
// merge` keeps it current with every local `git merge`) fixes that without
// any remote involved.
func TestCreateForksFromLocalBase(t *testing.T) {
	r := &baseRunner{baseResolves: true}
	if _, err := newBaseManager(r).Create(context.Background(), "b1", "task", ""); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if got := r.addArgs(t); !strings.HasSuffix(got, "-b task/b1 main") {
		t.Errorf("worktree add = %q, want it to fork from main", got)
	}
}

// TestCreateFallsBackWhenBaseRefMissing covers a project whose base_branch
// is misconfigured or not yet created locally.
func TestCreateFallsBackWhenBaseRefMissing(t *testing.T) {
	r := &baseRunner{}
	if _, err := newBaseManager(r).Create(context.Background(), "b1", "task", ""); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if got := r.addArgs(t); !strings.HasSuffix(got, "-b task/b1") {
		t.Errorf("worktree add = %q, want no start point when the base ref is missing", got)
	}
}

// TestCreateNeverRebasesAnExistingBranch: a resumed bead keeps its own
// commits. Re-pointing it at the base would discard them.
func TestCreateNeverRebasesAnExistingBranch(t *testing.T) {
	r := &baseRunner{branchExists: true, baseResolves: true}
	if _, err := newBaseManager(r).Create(context.Background(), "b1", "task", ""); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if got := r.addArgs(t); !strings.HasSuffix(got, "add /repo/.baron/worktrees/b1 task/b1") {
		t.Errorf("worktree add = %q, want the existing branch checked out as-is", got)
	}
}

// TestCreateWithoutBaseConfigured keeps the unconfigured path working: no
// base set means fork from HEAD.
func TestCreateWithoutBaseConfigured(t *testing.T) {
	r := &baseRunner{baseResolves: true}
	m := NewWorktreeManager(r, "/repo", "/repo/.baron/worktrees")
	if _, err := m.Create(context.Background(), "b1", "task", ""); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if got := r.addArgs(t); !strings.HasSuffix(got, "-b task/b1") {
		t.Errorf("worktree add = %q, want no start point", got)
	}
}

// parentRunner scripts Create for a hierarchical child: rev-parse reports
// whether an arbitrary named branch exists (not hardcoded to one branch,
// unlike baseRunner), and every command is recorded for ordering/content
// assertions.
type parentRunner struct {
	existingBranches map[string]bool
	calls            [][]string
}

func (r *parentRunner) Run(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if len(args) == 0 {
		return tool.Result{}, nil
	}
	switch args[0] {
	case "worktree":
		return tool.Result{}, nil
	case "rev-parse":
		branch := strings.TrimPrefix(args[len(args)-1], "refs/heads/")
		if r.existingBranches[branch] {
			return tool.Result{}, nil
		}
		return tool.Result{ExitCode: 1}, errors.New("exit status 1")
	default:
		return tool.Result{}, nil
	}
}

func (r *parentRunner) addArgs(t *testing.T) string {
	t.Helper()
	for _, c := range r.calls {
		if len(c) > 2 && c[1] == "worktree" && c[2] == "add" {
			return strings.Join(c, " ")
		}
	}
	t.Fatalf("no `git worktree add` in %v", r.calls)
	return ""
}

// TestCreateChildForksFromExistingParentBranch is the regression test for
// the flat-branch bug: a hierarchical child (bd's dotted-id/Parent link)
// must integrate into its parent's branch, not straight into main, when
// that parent branch already exists (an earlier sibling created it).
func TestCreateChildForksFromExistingParentBranch(t *testing.T) {
	r := &parentRunner{existingBranches: map[string]bool{"epic/e1": true}}
	m := newBaseManager(r)
	if _, err := m.Create(context.Background(), "e1.1", "task", "epic/e1"); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if got := r.addArgs(t); !strings.HasSuffix(got, "-b task/e1.1 epic/e1") {
		t.Errorf("worktree add = %q, want it to fork from the parent branch", got)
	}
	for _, c := range r.calls {
		if len(c) > 1 && c[1] == "branch" {
			t.Errorf("calls = %v, want no `git branch` — the parent branch already existed", r.calls)
		}
	}
}

// TestCreateChildCreatesMissingParentBranch: the parent bead may never have
// run its own agent, so its branch might not exist yet — the first child to
// fork from it must create it (at the base branch's tip), not fail.
func TestCreateChildCreatesMissingParentBranch(t *testing.T) {
	r := &parentRunner{existingBranches: map[string]bool{"main": true}}
	m := newBaseManager(r)
	if _, err := m.Create(context.Background(), "e1.1", "task", "epic/e1"); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	var branchCreated, forkedFromParent bool
	for _, c := range r.calls {
		if len(c) >= 3 && c[1] == "branch" && c[2] == "epic/e1" {
			branchCreated = true
			if !strings.HasSuffix(strings.Join(c, " "), "epic/e1 main") {
				t.Errorf("git branch call = %v, want it to fork the parent branch from main", c)
			}
		}
	}
	if !branchCreated {
		t.Fatalf("calls = %v, want a `git branch epic/e1 main` to create the missing parent branch", r.calls)
	}
	if got := r.addArgs(t); strings.HasSuffix(got, "-b task/e1.1 epic/e1") {
		forkedFromParent = true
	}
	if !forkedFromParent {
		t.Errorf("worktree add = %q, want the child to fork from the newly created parent branch", r.addArgs(t))
	}
}
