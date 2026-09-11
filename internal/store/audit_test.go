package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testEvent(target string) AuditEvent {
	return AuditEvent{
		Time:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Actor:  Actor{Type: ActorAgent, Name: "test-agent"},
		Action: "bead.create",
		Target: target,
		Detail: "created bead",
	}
}

func testStore(t *testing.T) *AuditStore {
	t.Helper()
	return NewAuditStore(filepath.Join(t.TempDir(), "events.jsonl"))
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if len(data) == 0 {
		return 0
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}

func TestAuditAppend(t *testing.T) {
	tests := []struct {
		name     string
		events   int
		expected int
	}{
		{"single", 1, 1},
		{"three", 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testStore(t)
			for i := 0; i < tt.events; i++ {
				if err := s.Append(testEvent("brn:test:1")); err != nil {
					t.Fatalf("append: %v", err)
				}
			}
			if got := countLines(t, s.Path()); got != tt.expected {
				t.Fatalf("file lines = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestAuditQueryByTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   int
	}{
		{"matches one", "brn:bead:aaa", 1},
		{"matches many", "brn:bead:bbb", 2},
		{"no match", "brn:bead:zzz", 0},
	}
	s := testStore(t)
	for _, e := range []AuditEvent{
		testEvent("brn:bead:aaa"),
		testEvent("brn:bead:bbb"),
		testEvent("brn:bead:bbb"),
		testEvent("brn:bead:ccc"),
	} {
		if err := s.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Query(tt.target)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("matches = %d, want %d", len(got), tt.want)
			}
			for _, e := range got {
				if e.Target != tt.target {
					t.Fatalf("got target %q, want %q", e.Target, tt.target)
				}
			}
		})
	}
}

func TestAuditRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		event AuditEvent
	}{
		{"full", testEvent("brn:bead:aaa")},
		{"empty detail", AuditEvent{
			Time:   time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
			Actor:  Actor{Type: ActorUser, Name: "yusuf"},
			Action: "merge.request",
			Target: "pr:42",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testStore(t)
			if err := s.Append(tt.event); err != nil {
				t.Fatalf("append: %v", err)
			}
			events, err := s.All()
			if err != nil {
				t.Fatalf("all: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("events = %d, want 1", len(events))
			}
			got := events[0]
			if !got.Time.Equal(tt.event.Time) || got.Actor != tt.event.Actor ||
				got.Action != tt.event.Action || got.Target != tt.event.Target ||
				got.Detail != tt.event.Detail {
				t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, tt.event)
			}
		})
	}
}

func TestAuditCorruptionTolerance(t *testing.T) {
	s := testStore(t)
	if err := os.WriteFile(s.Path(), []byte("this is not json\n"), 0o600); err != nil {
		t.Fatalf("write malformed line: %v", err)
	}
	if err := s.Append(testEvent("brn:bead:ok")); err != nil {
		t.Fatalf("append: %v", err)
	}
	events, err := s.All()
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (only valid)", len(events))
	}
	if events[0].Target != "brn:bead:ok" {
		t.Fatalf("target = %q, want brn:bead:ok", events[0].Target)
	}
}

func TestAuditEmptyLog(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"non-existent file", filepath.Join(t.TempDir(), "missing.jsonl")},
		{"empty file", func() string {
			p := filepath.Join(t.TempDir(), "empty.jsonl")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			return p
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewAuditStore(tt.path)
			got, err := s.Query("brn:bead:none")
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("matches = %d, want 0", len(got))
			}
		})
	}
}

func TestAuditAppendOnly(t *testing.T) {
	s := testStore(t)
	first := testEvent("brn:bead:one")
	if err := s.Append(first); err != nil {
		t.Fatalf("append: %v", err)
	}
	data1, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := s.Append(testEvent("brn:bead:two")); err != nil {
		t.Fatalf("append: %v", err)
	}
	data2, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data2) <= len(data1) || string(data2[:len(data1)]) != string(data1) {
		t.Fatalf("first event was modified: original %q, after %q", data1, data2)
	}
}

func TestAuditConcurrentAppend(t *testing.T) {
	s := testStore(t)
	const n = 100
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if err := s.Append(testEvent("brn:bead:conc")); err != nil {
				t.Errorf("append: %v", err)
			}
		})
	}
	wg.Wait()
	if got := countLines(t, s.Path()); got != n {
		t.Fatalf("lines = %d, want %d", got, n)
	}
	events, err := s.All()
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(events) != n {
		t.Fatalf("events = %d, want %d", len(events), n)
	}
}
