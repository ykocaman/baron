package store

import (
	"fmt"
	"time"
)

// Run records one model launch attempt against a bead: bead_id, model,
// start/end time, cost, wall-clock time. CostUSD is always 0 today: no
// model CLI has a documented convention for reporting its own spend back
// to BARON — the model runs with the user's own credentials, which BARON
// never reads, so only wall-clock time is tracked. The field exists so a
// future integration doesn't need a schema change.
type Run struct {
	BRN       string        `json:"brn"`
	Model     string        `json:"model"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration_ns"`
	CostUSD   float64       `json:"cost_usd,omitempty"`
}

// RunStore is the append-only log of model launch attempts, used by
// `baron report cost`.
type RunStore struct {
	path string
}

// NewRunStore creates a store at the given path.
func NewRunStore(path string) *RunStore {
	return &RunStore{path: path}
}

// Append records one run. Creates the file if needed.
func (s *RunStore) Append(r Run) error {
	if err := appendJSONL(s.path, r); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

// All returns every recorded run.
func (s *RunStore) All() ([]Run, error) {
	runs, err := readJSONL[Run](s.path)
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	return runs, nil
}

// ForBead returns brn's runs.
func (s *RunStore) ForBead(brn string) ([]Run, error) {
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	matches := make([]Run, 0, len(all))
	for _, r := range all {
		if r.BRN == brn {
			matches = append(matches, r)
		}
	}
	return matches, nil
}
