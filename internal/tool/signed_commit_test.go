package tool

import (
	"context"
	"reflect"
	"testing"
)

type scriptedLog struct {
	out string
}

func (s *scriptedLog) Run(_ context.Context, name string, args []string, _ Options) (Result, error) {
	if name == "git" && len(args) > 0 && args[0] == "log" {
		return Result{Stdout: s.out}, nil
	}
	return Result{}, nil
}

func TestUnsignedCommitsAllSigned(t *testing.T) {
	r := &scriptedLog{out: "aaa G\nbbb U\n"}
	got, err := UnsignedCommits(context.Background(), r, "/repo", "origin/main")
	if err != nil {
		t.Fatalf("UnsignedCommits() error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UnsignedCommits() = %v, want none (G and U both count as signed)", got)
	}
}

func TestUnsignedCommitsMixed(t *testing.T) {
	r := &scriptedLog{out: "aaa G\nbbb N\nccc B\nddd U\n"}
	got, err := UnsignedCommits(context.Background(), r, "/repo", "origin/main")
	if err != nil {
		t.Fatalf("UnsignedCommits() error: %v", err)
	}
	want := []string{"bbb", "ccc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnsignedCommits() = %v, want %v", got, want)
	}
}

func TestUnsignedCommitsEmpty(t *testing.T) {
	r := &scriptedLog{out: ""}
	got, err := UnsignedCommits(context.Background(), r, "/repo", "origin/main")
	if err != nil {
		t.Fatalf("UnsignedCommits() error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UnsignedCommits() = %v, want none for an empty range", got)
	}
}
