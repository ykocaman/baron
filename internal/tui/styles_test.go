package tui

import "testing"

// TestStatusBucketsCoverEveryStatus: every bead status statusGlyph knows
// about must also have a bucket, and vice versa (the 4-bucket
// palette must never silently fall back to bucketIdle for a real status).
func TestStatusBucketsCoverEveryStatus(t *testing.T) {
	statuses := []string{
		"open", "assigned", "working", "in_progress", "validating", "retry",
		"blocked", "mergable", "human_queue", "merged", "closed", "cancelled",
	}
	for _, s := range statuses {
		if _, ok := statusBuckets[s]; !ok {
			t.Errorf("status %q has no bucket assignment", s)
		}
	}
}

// TestStatusStyleGroupsByBucket: statuses in the same bucket must render
// with the same color; statuses in different buckets must not. Compares
// GetForeground() directly rather than Render() output — lipgloss disables
// ANSI escapes entirely in a non-TTY context like `go test`, so rendered
// strings can't distinguish colors here.
func TestStatusStyleGroupsByBucket(t *testing.T) {
	s := newStyles("dark", false)
	fg := func(status string) interface {
		RGBA() (r, g, b, a uint32)
	} {
		return s.statusStyle(status).GetForeground()
	}

	sameBucket := []string{"assigned", "working", "validating", "retry"} // all bucketActive
	first := fg(sameBucket[0])
	for _, status := range sameBucket[1:] {
		if got := fg(status); got != first {
			t.Errorf("statusStyle(%q).GetForeground() = %v, want the same as statusStyle(%q) = %v (same bucket)", status, got, sameBucket[0], first)
		}
	}

	needsYou := fg("human_queue")
	if needsYou == first {
		t.Error("human_queue (bucketNeedsYou) has the same color as the active bucket, want distinct")
	}
	done := fg("merged")
	if done == first || done == needsYou {
		t.Error("merged (bucketDone) shares a color with another bucket, want distinct")
	}
}

// TestStatusStyleHonorsNoColor: bucket coloring must still respect NO_COLOR.
func TestStatusStyleHonorsNoColor(t *testing.T) {
	colorStyles := newStyles("dark", false)
	noColorStyles := newStyles("dark", true)
	if colorStyles.statusStyle("working").GetForeground() == noColorStyles.statusStyle("working").GetForeground() {
		t.Error("statusStyle foreground is the same with noColor true and false, want noColor to strip it")
	}
}
