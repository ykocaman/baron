package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCostReportGroupsAndRanksByAmplification(t *testing.T) {
	s := NewRunStore(filepath.Join(t.TempDir(), "runs.jsonl"))
	runs := []Run{
		{BRN: "baron-a", Duration: 10 * time.Minute},
		{BRN: "baron-a", Duration: 20 * time.Minute}, // 1 retry
		{BRN: "baron-a", Duration: 30 * time.Minute}, // 2nd retry
		{BRN: "baron-b", Duration: 5 * time.Minute},  // no retries
	}
	for _, r := range runs {
		if err := s.Append(r); err != nil {
			t.Fatalf("Append() error: %v", err)
		}
	}

	report, err := s.CostReport()
	if err != nil {
		t.Fatalf("CostReport() error: %v", err)
	}
	if len(report) != 2 {
		t.Fatalf("CostReport() = %+v, want 2 beads", report)
	}

	// baron-a: 3 runs, total 60m, avg 20m, 2 retries, amplification = 2*20m = 40m.
	a := report[0]
	if a.BRN != "baron-a" {
		t.Fatalf("report[0].BRN = %q, want baron-a (highest amplification first)", a.BRN)
	}
	if a.Runs != 3 || a.Retries != 2 {
		t.Errorf("baron-a: Runs=%d Retries=%d, want 3 and 2", a.Runs, a.Retries)
	}
	if a.Avg != 20*time.Minute {
		t.Errorf("baron-a: Avg = %s, want 20m", a.Avg)
	}
	if a.Amplification != 40*time.Minute {
		t.Errorf("baron-a: Amplification = %s, want 40m", a.Amplification)
	}

	// baron-b: 1 run, no retries, amplification = 0.
	b := report[1]
	if b.BRN != "baron-b" || b.Retries != 0 || b.Amplification != 0 {
		t.Errorf("baron-b = %+v, want 0 retries and 0 amplification", b)
	}
}

func TestCostReportEmpty(t *testing.T) {
	s := NewRunStore(filepath.Join(t.TempDir(), "runs.jsonl"))
	report, err := s.CostReport()
	if err != nil {
		t.Fatalf("CostReport() error: %v", err)
	}
	if len(report) != 0 {
		t.Errorf("CostReport() = %+v, want empty for no recorded runs", report)
	}
}
