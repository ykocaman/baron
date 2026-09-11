package cli

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/baron-cli/baron/internal/domain"
)

// stateSnapshotPath is where Reconcile's state-drift detector persists the
// last state BARON itself legitimately set for each bead (see
// recordStateSnapshot, reconcileStateDrift). Not synced anywhere, not part
// of the beads store itself — a local reconciliation aid, same spirit as
// .baron/runs.jsonl. Keyed by bd's native (unprefixed) issue id, matching
// what transitionStatus and its two exceptions already receive as id.
func (a *app) stateSnapshotPath() string {
	return filepath.Join(a.dir, ".baron", "state-snapshot.json")
}

// loadStateSnapshot reads the snapshot file, or returns an empty map if it
// doesn't exist yet or is unreadable — a missing/corrupt snapshot means
// every bead looks "first seen" to reconcileStateDrift, which seeds it
// rather than guess at drift with nothing to compare against.
func (a *app) loadStateSnapshot() map[string]domain.BeadState {
	data, err := os.ReadFile(a.stateSnapshotPath())
	if err != nil {
		return map[string]domain.BeadState{}
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return map[string]domain.BeadState{}
	}
	snap := make(map[string]domain.BeadState, len(raw))
	for k, v := range raw {
		snap[k] = domain.BeadState(v)
	}
	return snap
}

// saveStateSnapshot writes snap back to disk. Failure is silent by design:
// this file is a reconciliation aid, not a source of truth bd itself
// depends on — a write failure just means the next pass falls back to
// treating more beads as "first seen" than strictly necessary, never a
// crash or a lost bead.
func (a *app) saveStateSnapshot(snap map[string]domain.BeadState) {
	raw := make(map[string]string, len(snap))
	for k, v := range snap {
		raw[k] = string(v)
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	path := a.stateSnapshotPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o750)
	_ = os.WriteFile(path, data, 0o600)
}

// recordStateSnapshot records that BARON itself just legitimately set id
// (bd's native issue id) to state. Called from every code path that
// mutates a bead's bd status: transitionStatus (the validated chokepoint
// nearly everything goes through), and its two known exceptions —
// work_mutate.go's epic auto-close (a deliberate direct Status write,
// since transitionStatus's own graph has no open->closed edge) and
// CloseBead's `bd close` path (a distinct bd subcommand, not
// BeadStore.Status at all).
//
// reconcileStateDrift trusts this file as "the last state BARON believes
// is correct" for each bead; any future code path that changes bd status
// without calling this makes that bead's next Reconcile pass see a false
// positive (a legitimate change reported and reverted as drift) — so any
// new direct bd-status mutation added to this package must call it too.
func (a *app) recordStateSnapshot(id string, state domain.BeadState) {
	snap := a.loadStateSnapshot()
	snap[id] = state
	a.saveStateSnapshot(snap)
}
