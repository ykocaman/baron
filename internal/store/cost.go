package store

import (
	"sort"
	"time"
)

// BeadCost is one bead's amplification summary — how much wall-clock time
// its retries cost, not just its runs. Named for what roadmap.md's
// "İş ekonomisi kaydı" acceptance criterion calls for: "bead başına run/
// retry/duvar saati ve amplifikasyon oranı". This was originally meant to
// back a `baron report cost` command; BARON has no CLI command surface any
// more (see docs/PRD/crew-mode.md §6's note on the same constraint), so
// this is a tested library function with no UI wired to it yet — ready for
// whichever surface (a TUI screen, a Crew Mode persona's own report) ends
// up displaying it.
type BeadCost struct {
	BRN   string
	Runs  int
	Total time.Duration
	Avg   time.Duration
	// Retries is Runs-1: the first launch isn't a retry, only every launch
	// after it is.
	Retries int
	// Amplification is Retries * Avg — the wall-clock cost retries alone
	// added, in the units roadmap.md's acceptance criterion names.
	Amplification time.Duration
}

// CostReport groups every recorded run by bead and returns one BeadCost
// per bead, sorted by Amplification descending (the beads costing the
// most retry time first — what a human triaging spend would want to see
// first).
func (s *RunStore) CostReport() ([]BeadCost, error) {
	runs, err := s.All()
	if err != nil {
		return nil, err
	}

	order := make([]string, 0)
	totals := make(map[string]time.Duration)
	counts := make(map[string]int)
	for _, r := range runs {
		if _, ok := totals[r.BRN]; !ok {
			order = append(order, r.BRN)
		}
		totals[r.BRN] += r.Duration
		counts[r.BRN]++
	}

	report := make([]BeadCost, 0, len(order))
	for _, brn := range order {
		n := counts[brn]
		total := totals[brn]
		avg := total / time.Duration(n)
		retries := n - 1
		report = append(report, BeadCost{
			BRN: brn, Runs: n, Total: total, Avg: avg,
			Retries: retries, Amplification: time.Duration(retries) * avg,
		})
	}
	sort.Slice(report, func(i, j int) bool {
		return report[i].Amplification > report[j].Amplification
	})
	return report, nil
}
