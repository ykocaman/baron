package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// personaState is the trigger engine's local bookkeeping — never synced,
// never part of the beads store, same spirit as .baron/state-snapshot.json
// (state_snapshot.go). It answers two questions across Reconcile passes:
// "when did this persona (cron Trigger.Schedule or Trigger.On event) last
// fire" (LastRun, also debounced's own MinIntervalMinutes clock) and "which
// state was each bead in the last time reconcilePersonaTriggers looked, so
// a genuine transition (any from->to, not just ->closed/->merged) can be
// told apart from a first observation" (LastSeenStates).
type personaState struct {
	LastRun map[string]time.Time `json:"last_run"`
	// LastSeenStates is bead id -> the state reconcilePersonaTriggers
	// observed it in last pass. Deliberately its own snapshot, not a reuse
	// of state_snapshot.go's — that one tracks "what BARON believes is
	// correct" for drift detection; this one only needs "did this bead's
	// state just change since I last checked" (beadTransitions), a plain
	// diff with no legitimacy question attached.
	LastSeenStates map[string]string `json:"last_seen_states"`
}

func (a *app) personaStatePath() string {
	return filepath.Join(a.dir, ".baron", "personas-state.json")
}

func (a *app) loadPersonaState() personaState {
	data, err := os.ReadFile(a.personaStatePath())
	if err != nil {
		return personaState{}
	}
	var s personaState
	if err := json.Unmarshal(data, &s); err != nil {
		return personaState{}
	}
	return s
}

// savePersonaState fails silently by design — see state_snapshot.go's
// saveStateSnapshot for the identical rationale: this file is a scheduling
// aid, not a source of truth anything else depends on.
func (a *app) savePersonaState(s personaState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	path := a.personaStatePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o750)
	_ = os.WriteFile(path, data, 0o600)
}
