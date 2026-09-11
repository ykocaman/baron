package tool

import (
	"context"
	"errors"
	"testing"
)

type scriptedMergeCheck struct {
	name    string // subcommand to respond to: "merge-tree", "ls-files", "diff"
	exit    int
	stdout  string
	failure bool // true = a genuine run failure (not the tool's normal "problem found" signal)
}

func (s *scriptedMergeCheck) Run(_ context.Context, name string, args []string, _ Options) (Result, error) {
	if name != "git" || len(args) == 0 || args[0] != s.name {
		return Result{}, nil
	}
	if s.exit == 0 {
		return Result{Stdout: s.stdout}, nil
	}
	if s.failure {
		return Result{ExitCode: s.exit}, errors.New("git failed")
	}
	return Result{ExitCode: s.exit, Stdout: s.stdout}, errors.New("exit status")
}

func TestPreflightClean(t *testing.T) {
	r := &scriptedMergeCheck{name: "merge-tree"}
	got, err := Preflight(context.Background(), r, "/repo", "origin/main", "baron/x")
	if err != nil {
		t.Fatalf("Preflight() error: %v", err)
	}
	if !got.Clean {
		t.Errorf("Preflight() = %+v, want clean", got)
	}
}

func TestPreflightConflict(t *testing.T) {
	r := &scriptedMergeCheck{name: "merge-tree", exit: 1, stdout: "CONFLICT (content): a.go"}
	got, err := Preflight(context.Background(), r, "/repo", "origin/main", "baron/x")
	if err != nil {
		t.Fatalf("Preflight() error: %v, want a normal conflict result, not a Go error", err)
	}
	if got.Clean {
		t.Error("Preflight() clean = true, want false for a conflict")
	}
	if got.Output == "" {
		t.Error("Preflight() Output empty, want conflict details")
	}
}

func TestPreflightRunFailure(t *testing.T) {
	r := &scriptedMergeCheck{name: "merge-tree", exit: 2, failure: true}
	_, err := Preflight(context.Background(), r, "/repo", "origin/main", "baron/x")
	if err == nil {
		t.Fatal("Preflight() error = nil, want an error for a genuine run failure (exit 2)")
	}
}

func TestUnresolvedConflictsNone(t *testing.T) {
	r := &scriptedMergeCheck{name: "ls-files", stdout: ""}
	got, err := UnresolvedConflicts(context.Background(), r, "/repo")
	if err != nil {
		t.Fatalf("UnresolvedConflicts() error: %v", err)
	}
	if got {
		t.Error("UnresolvedConflicts() = true, want false for empty ls-files -u")
	}
}

func TestUnresolvedConflictsPresent(t *testing.T) {
	r := &scriptedMergeCheck{name: "ls-files", stdout: "100644 abc 1\ta.go\n"}
	got, err := UnresolvedConflicts(context.Background(), r, "/repo")
	if err != nil {
		t.Fatalf("UnresolvedConflicts() error: %v", err)
	}
	if !got {
		t.Error("UnresolvedConflicts() = false, want true when ls-files -u is non-empty")
	}
}

func TestDirtyDiffClean(t *testing.T) {
	r := &scriptedMergeCheck{name: "diff"}
	dirty, _, err := DirtyDiff(context.Background(), r, "/repo")
	if err != nil {
		t.Fatalf("DirtyDiff() error: %v", err)
	}
	if dirty {
		t.Error("DirtyDiff() = true, want false")
	}
}

func TestDirtyDiffFound(t *testing.T) {
	r := &scriptedMergeCheck{name: "diff", exit: 2, stdout: "a.go:3: trailing whitespace"}
	dirty, out, err := DirtyDiff(context.Background(), r, "/repo")
	if err != nil {
		t.Fatalf("DirtyDiff() error: %v", err)
	}
	if !dirty || out == "" {
		t.Errorf("DirtyDiff() = (%v, %q), want dirty with output", dirty, out)
	}
}
