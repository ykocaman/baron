package persona

import (
	"fmt"
	"regexp"
	"strings"
)

// ReviewerID is the built-in review persona's fixed ID — looked up by
// this exact string (internal/cli/review_gate.go), not discovered via
// Trigger matching the way every other persona is. Its Trigger is
// deliberately empty (manual/none, see personas/reviewer.md):
// Reconcile's persona-trigger engine never fires it at all — postAgentGate
// calls it directly, synchronously, as one more gate step before a bead
// reaches mergable, so a config typo or renamed ID here is a silent
// "review gate never runs" rather than a load error (see reviewGate's own
// not-found/disabled -> skip fallback).
const ReviewerID = "reviewer"

// MergeCloserID is the built-in post-merge-check persona's fixed ID —
// reviewer's own counterpart on the other side of a merge, looked up
// the same way (internal/cli/merge_closer.go), not discovered via Trigger
// matching. Its Trigger is deliberately empty for the identical reason:
// mergeCloseGate calls it directly, synchronously, right after a merge
// lands, rather than letting Reconcile fire it.
const MergeCloserID = "closer"

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// Slug derives a filesystem/tmux-safe persona ID from a display name,
// collision-suffixed against existing so two personas never silently
// overwrite each other. Both the CLI's persona-create command and the
// TUI's Personas-tab 'n' form pre-fill their ID field with this.
func Slug(name string, existing []Persona) string {
	base := strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "-")
	if base == "" {
		base = "persona"
	}
	slug := base
	for n := 2; IDTaken(slug, existing); n++ {
		slug = fmt.Sprintf("%s-%d", base, n)
	}
	return slug
}

// IDTaken reports whether id is already used by one of existing.
func IDTaken(id string, existing []Persona) bool {
	for _, p := range existing {
		if p.ID == id {
			return true
		}
	}
	return false
}
