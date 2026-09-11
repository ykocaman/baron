package cli

import "testing"

func TestParseMergeCloseVerdictOK(t *testing.T) {
	verdict, reason := parseMergeCloseVerdict("Looks good.\nCLOSE: OK")
	if verdict != mergeCloseOK {
		t.Errorf("verdict = %v, want mergeCloseOK", verdict)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty on OK", reason)
	}
}

func TestParseMergeCloseVerdictReopenWithReason(t *testing.T) {
	verdict, reason := parseMergeCloseVerdict("Something's off.\nCLOSE: REOPEN: the test never actually runs")
	if verdict != mergeCloseReopen {
		t.Errorf("verdict = %v, want mergeCloseReopen", verdict)
	}
	if reason != "the test never actually runs" {
		t.Errorf("reason = %q, want the trailing text after the colon", reason)
	}
}

func TestParseMergeCloseVerdictReopenWithoutReasonFallsBack(t *testing.T) {
	_, reason := parseMergeCloseVerdict("CLOSE: REOPEN:")
	if reason == "" {
		t.Error("reason = \"\", want a fallback reason when none was given")
	}
}

// TestParseMergeCloseVerdictOnlyLastLineCounts: reasoning en route to the
// verdict routinely mentions both keywords ("if this held up I'd say
// CLOSE: OK, but..."); only the final non-empty line is the actual answer.
func TestParseMergeCloseVerdictOnlyLastLineCounts(t *testing.T) {
	verdict, reason := parseMergeCloseVerdict(
		"If this held up I'd say CLOSE: OK, but it doesn't.\nCLOSE: REOPEN: the merge dropped a file",
	)
	if verdict != mergeCloseReopen {
		t.Errorf("verdict = %v, want mergeCloseReopen (only the last line counts)", verdict)
	}
	if reason != "the merge dropped a file" {
		t.Errorf("reason = %q, want the last line's own reason", reason)
	}
}

func TestParseMergeCloseVerdictUnparseable(t *testing.T) {
	tests := []string{
		"",
		"I'm not sure what to make of this.",
		"CLOSE: MAYBE",
	}
	for _, out := range tests {
		if verdict, _ := parseMergeCloseVerdict(out); verdict != mergeCloseUnparsed {
			t.Errorf("parseMergeCloseVerdict(%q) verdict = %v, want mergeCloseUnparsed", out, verdict)
		}
	}
}

func TestParseMergeCloseVerdictTrailingBlankLinesIgnored(t *testing.T) {
	verdict, _ := parseMergeCloseVerdict("CLOSE: OK\n\n\n")
	if verdict != mergeCloseOK {
		t.Errorf("verdict = %v, want mergeCloseOK (trailing blank lines shouldn't matter)", verdict)
	}
}
