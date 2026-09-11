package tui

import (
	"strings"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

func (m Model) searchMatches(b store.Bead) bool {
	if m.searchQuery == "" {
		return true
	}
	if m.searchRegex != nil {
		return m.searchRegex.MatchString(string(b.BRN)) ||
			m.searchRegex.MatchString(b.Title)
	}
	q := strings.ToLower(m.searchQuery)
	return strings.Contains(strings.ToLower(string(b.BRN)), q) ||
		strings.Contains(strings.ToLower(b.Title), q)
}

// searchCountForTab returns how many rows in tab match the active search query.
// Used by the tab bar to show per-tab hit counts while filtering.
func (m Model) searchCountForTab(tab int) int {
	return m.searchCountForBucket(tab, -1)
}

// searchCountForBucket returns how many rows in tab's subTab bucket match the
// active search query (subTab < 0 means every bucket in the tab). Used by
// both the tab bar and the sub-tab bar so their counts stay in lockstep
// while filtering — the sub-tab bar used to call bucketRows directly and
// ignore the search query entirely, so e.g. the Active tab's Retry pill kept
// showing the unfiltered count even as every other count changed.
func (m Model) searchCountForBucket(tab, subTab int) int {
	if m.searchQuery == "" {
		return len(m.bucketRows(tab, subTab))
	}
	n := 0
	for _, r := range m.bucketRows(tab, subTab) {
		if r.bead.BRN != "" && m.searchMatches(r.bead) {
			n++
		}
	}
	return n
}

// searchAllRows returns matching rows across every tab when a search is
// active, so the list shows the full cross-tab result set rather than only
// the current tab's matches.
func (m Model) searchAllRows() []splitRow {
	seen := map[string]bool{}
	var out []splitRow
	for tab := range boardColumns {
		for _, r := range m.bucketRows(tab, -1) {
			key := string(r.bead.BRN)
			if key == "" || seen[key] {
				continue
			}
			if m.searchMatches(r.bead) {
				seen[key] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// findBeadByBRN returns the bead with the given BRN from m.beads.
func (m Model) findBeadByBRN(brn domain.BRN) (store.Bead, bool) {
	return findBead(m.beads, string(brn))
}

// splitRow is one row in the split-pane's left list. bead is non-zero for
// selectable rows; a note row renders as a fixed muted line under its bead
// (blocks-type dependency links) and is skipped by list navigation.
