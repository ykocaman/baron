package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRunStoreAppendAndAll(t *testing.T) {
	s := NewRunStore(filepath.Join(t.TempDir(), "runs.jsonl"))
	r1 := Run{BRN: "baron-a", Model: "claude", StartedAt: time.Now(), Duration: 5 * time.Second}
	r2 := Run{BRN: "baron-b", Model: "codex", StartedAt: time.Now(), Duration: 3 * time.Second}
	if err := s.Append(r1); err != nil {
		t.Fatalf("Append() error: %v", err)
	}
	if err := s.Append(r2); err != nil {
		t.Fatalf("Append() error: %v", err)
	}
	all, err := s.All()
	if err != nil {
		t.Fatalf("All() error: %v", err)
	}
	if len(all) != 2 || all[0].BRN != "baron-a" || all[1].BRN != "baron-b" {
		t.Errorf("All() = %+v, want both runs in order", all)
	}
}

func TestRunStoreForBead(t *testing.T) {
	s := NewRunStore(filepath.Join(t.TempDir(), "runs.jsonl"))
	_ = s.Append(Run{BRN: "baron-a", Duration: 5 * time.Second})
	_ = s.Append(Run{BRN: "baron-b", Duration: 3 * time.Second})
	_ = s.Append(Run{BRN: "baron-a", Duration: 2 * time.Second})

	got, err := s.ForBead("baron-a")
	if err != nil {
		t.Fatalf("ForBead() error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ForBead() = %+v, want 2 runs for baron-a", got)
	}
}

func TestRunStoreAllMissingFile(t *testing.T) {
	s := NewRunStore(filepath.Join(t.TempDir(), "missing.jsonl"))
	all, err := s.All()
	if err != nil {
		t.Fatalf("All() error: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("All() = %v, want empty for a missing file", all)
	}
}
