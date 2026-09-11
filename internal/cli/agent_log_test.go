package cli

import (
	"slices"
	"testing"
)

// TestRunSummaryRoundtrip: writeRunSummary/readRunSummary must round-trip a
// bead's one-shot run summary, and a missing summary reads back as nil.
func TestRunSummaryRoundtrip(t *testing.T) {
	a := newTestApp(t, &runGateRunner{})
	want := []string{"ran in 5s", "gate passed (956ms)"}
	if err := a.writeRunSummary("baron-a", want); err != nil {
		t.Fatalf("writeRunSummary: %v", err)
	}
	got, err := a.readRunSummary("baron-a")
	if err != nil {
		t.Fatalf("readRunSummary: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("readRunSummary = %v, want %v", got, want)
	}
	missing, err := a.readRunSummary("baron-none")
	if err != nil || missing != nil {
		t.Errorf("readRunSummary(missing) = %v, %v; want nil, nil", missing, err)
	}
}
