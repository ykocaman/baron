package tool

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// gitScript is a Runner that answers a scripted set of git invocations,
// keyed by the joined argv, and records every call in order.
type gitScript struct {
	replies map[string]Result
	errs    map[string]bool
	calls   []string
}

func (g *gitScript) Run(_ context.Context, name string, args []string, _ Options) (Result, error) {
	key := name + " " + strings.Join(args, " ")
	g.calls = append(g.calls, key)
	res := g.replies[key]
	if g.errs[key] || res.ExitCode != 0 {
		return res, errors.New("exit status " + strconv.Itoa(res.ExitCode))
	}
	return res, nil
}

func TestCommitCreatesCommit(t *testing.T) {
	g := &gitScript{replies: map[string]Result{
		"git diff --cached --quiet": {ExitCode: 1}, // index differs from HEAD
		"git rev-parse HEAD":        {Stdout: "abc123def456\n"},
	}}
	sha, err := Commit(context.Background(), g, "/wt", "feat: thing\n\nBead: baron-x\n")
	if err != nil {
		t.Fatalf("Commit error: %v", err)
	}
	if sha != "abc123def456" {
		t.Errorf("sha = %q, want the new HEAD", sha)
	}
	if !strings.Contains(strings.Join(g.calls, "|"), "git commit -m feat: thing") {
		t.Errorf("calls = %v, want a git commit with the message", g.calls)
	}
}

func TestCommitNothingStaged(t *testing.T) {
	g := &gitScript{replies: map[string]Result{
		"git diff --cached --quiet": {}, // index matches HEAD
	}}
	if _, err := Commit(context.Background(), g, "/wt", "msg"); !errors.Is(err, ErrNothingStaged) {
		t.Fatalf("Commit error = %v, want ErrNothingStaged", err)
	}
	for _, c := range g.calls {
		if strings.HasPrefix(c, "git commit") {
			t.Errorf("git commit ran with an empty index: %v", g.calls)
		}
	}
}

func TestCommitSurfacesGitError(t *testing.T) {
	g := &gitScript{replies: map[string]Result{
		"git diff --cached --quiet":   {ExitCode: 1},
		"git commit -m msg":           {ExitCode: 1, Stderr: "error: gpg failed to sign the data"},
		"git rev-parse HEAD":          {Stdout: "abc\n"},
		"git diff --cached --quiet x": {},
	}}
	_, err := Commit(context.Background(), g, "/wt", "msg")
	if err == nil {
		t.Fatal("Commit: want an error when git commit fails")
	}
	if !strings.Contains(err.Error(), "gpg failed to sign") {
		t.Errorf("error = %v, want git's own reason", err)
	}
}

func TestCommitsAhead(t *testing.T) {
	g := &gitScript{replies: map[string]Result{
		"git rev-parse --verify --quiet origin/main": {},
		"git rev-list --count origin/main..HEAD":     {Stdout: "3\n"},
	}}
	n, known, err := CommitsAhead(context.Background(), g, "/wt", "origin/main")
	if err != nil {
		t.Fatalf("CommitsAhead error: %v", err)
	}
	if !known || n != 3 {
		t.Errorf("CommitsAhead = (%d, %v), want (3, true)", n, known)
	}
}

// TestCommitsAheadUnresolvableBase: an unresolvable base must report
// "unknown", not "zero" — a caller that reads zero as "no work" would skip
// the gate on a branch that has commits.
func TestCommitsAheadUnresolvableBase(t *testing.T) {
	g := &gitScript{replies: map[string]Result{
		"git rev-parse --verify --quiet origin/main": {ExitCode: 1},
	}}
	n, known, err := CommitsAhead(context.Background(), g, "/wt", "origin/main")
	if err != nil {
		t.Fatalf("CommitsAhead error: %v", err)
	}
	if known || n != 0 {
		t.Errorf("CommitsAhead = (%d, %v), want (0, false)", n, known)
	}
}

func TestIsNotInstalled(t *testing.T) {
	if !isNotInstalled(&exec.Error{Name: "gitleaks", Err: exec.ErrNotFound}) {
		t.Error("isNotInstalled(exec.ErrNotFound) = false, want true")
	}
	if isNotInstalled(errors.New("exit status 1")) {
		t.Error("isNotInstalled(exit status 1) = true, want false — the command ran and failed")
	}
	if isNotInstalled(nil) {
		t.Error("isNotInstalled(nil) = true, want false")
	}
}
