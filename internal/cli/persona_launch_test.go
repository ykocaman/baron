package cli

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

// TestPersonaCandidatesOrdering verifies that personaCandidates puts the
// directly resolved agent first, then the free-tier model, then the
// rest — without duplicates. This is the contract the quota-retry loop
// relies on: try the cheapest-first fallback path, red-before-green.
func TestPersonaCandidatesOrdering(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	opencode := agent.Agent{Name: "opencode", Status: agent.StatusActive, Args: []string{"run", "{{prompt}}"}, ModelFlag: "--model"}
	claudeAg := testClaude()
	writeAgentRegistry(t, claudeAg, opencode)
	// Seed a free-tier model that resolves via opencode so personaCandidates'
	// free-tier path actually finds a distinct agent.
	writeCatalog(t, agent.Model{
		ID:    "google/antigravity-flash-free",
		Agent: "opencode",
		Tier:  agent.TierFree,
	})
	cat := agent.LoadCatalog()

	// Persona explicitly set to use claude.
	p := persona.Persona{ID: "qa", Model: persona.Model{Agent: "claude"}}
	candidates := a.personaCandidates(cat, p)
	if len(candidates) == 0 {
		t.Fatal("personaCandidates = empty, want at least claude")
	}
	if candidates[0].Name != "claude" {
		t.Errorf("candidates[0] = %q, want %q (resolved agent goes first)", candidates[0].Name, "claude")
	}
	// Free-tier maps to opencode — must appear after claude and only once.
	seen := map[string]int{}
	for _, ag := range candidates {
		seen[ag.Name]++
	}
	for agName, count := range seen {
		if count > 1 {
			t.Errorf("candidates has %q %d times, want exactly once (deduped)", agName, count)
		}
	}
	// opencode must appear somewhere in the list (free-tier fallback).
	if seen["opencode"] == 0 {
		t.Errorf("candidates = %v, want opencode present (free-tier fallback)", candidates)
	}
}

// TestLaunchPersonaQuotaRetriesToNextCandidate verifies the retry loop: when
// the first candidate's pane shows a quota error 0 ms after spawn, the window
// is killed and the next candidate in personaCandidates is tried instead.
func TestLaunchPersonaQuotaRetriesToNextCandidate(t *testing.T) {
	orig := personaQuotaCheckDelay
	defer func() { personaQuotaCheckDelay = orig }()
	personaQuotaCheckDelay = 0 // no wall-clock wait in tests

	opencode := agent.Agent{Name: "opencode", Status: agent.StatusActive, Args: []string{"run", "{{prompt}}"}, ModelFlag: "--model"}
	claudeAg := testClaude()
	runner := &tmuxPersonaRunner{
		tmuxOut: "tmux 3.4",
		// First capture-pane call (after spawning claude) returns a quota
		// message; the loop should kill the window and try opencode next.
		captureOut: []string{"Individual quota reached, please try again in 2h46m"},
	}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, claudeAg, opencode)
	cat := loadTestCatalog(t)

	p := persona.Persona{
		ID:        "qa",
		Name:      "QA",
		Prompt:    "x",
		Model:     persona.Model{Agent: "claude"},
		Authority: persona.Authority{BDWrite: true},
	}
	cfg := store.DefaultConfig()

	if err := a.launchPersona(context.Background(), cfg, cat, p, personaSubject{brn: "baron-x", reason: "test"}); err != nil {
		t.Fatalf("launchPersona() error: %v", err)
	}

	// We expect: (1) kill-window after quota detected, (2) a second new-window
	// for the fallback agent. Both should appear in the recorded calls.
	var killCount, spawnCount int
	for _, c := range runner.calls {
		if len(c) >= 2 && c[0] == "tmux" {
			switch c[1] {
			case "kill-window":
				killCount++
			case "new-window":
				spawnCount++
			}
		}
	}
	if killCount == 0 {
		t.Errorf("calls = %v, want at least one kill-window (quota retry must kill the first window)", runner.calls)
	}
	if spawnCount < 2 {
		t.Errorf("calls = %v, want at least 2 new-window calls (first quota-hit + second fallback spawn)", runner.calls)
	}
	// The audit log must record exactly one successful persona_run (for the fallback).
	events := auditEvents(t, a)
	runEvents := 0
	for _, e := range events {
		if e.Action == "persona_run" {
			runEvents++
		}
	}
	if runEvents != 1 {
		t.Errorf("audit persona_run events = %d, want exactly 1 (logged on successful fallback spawn)", runEvents)
	}
}

func TestIsPersonaStartupFailure(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"You've hit your weekly limit · resets 9am (Europe/Istanbul)\nbaron-run-exit=1", true},
		{"Individual quota reached, please try again in 2h46m", true},
		{"Rate limit exceeded, retry in 30s", true},
		{"error: 429 Too Many Requests", true},
		{"529 Overloaded", true},
		{"baron-run-exit=1", true},
		{"baron-run-exit=2\nexec /bin/zsh", true},
		{"baron-run-exit=0\nexec /bin/zsh", false},
		{"Running tests... OK", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isPersonaStartupFailure(tc.input)
		if got != tc.want {
			t.Errorf("isPersonaStartupFailure(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestLaunchPersonaWeeklyLimitRetriesToNextCandidate(t *testing.T) {
	orig := personaQuotaCheckDelay
	defer func() { personaQuotaCheckDelay = orig }()
	personaQuotaCheckDelay = 0

	opencode := agent.Agent{Name: "opencode", Status: agent.StatusActive, Args: []string{"run", "{{prompt}}"}, ModelFlag: "--model"}
	claudeAg := testClaude()
	runner := &tmuxPersonaRunner{
		tmuxOut: "tmux 3.4",
		captureOut: []string{
			"You've hit your weekly limit · resets 9am (Europe/Istanbul)\nbaron-run-exit=1",
		},
	}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, claudeAg, opencode)
	cat := loadTestCatalog(t)

	p := persona.Persona{
		ID:        "qa-chromium",
		Name:      "QA (Chromium)",
		Prompt:    "check things",
		Model:     persona.Model{Agent: "claude"},
		Authority: persona.Authority{BDWrite: true},
	}
	cfg := store.DefaultConfig()

	if err := a.launchPersona(context.Background(), cfg, cat, p, personaSubject{brn: "test-eal", reason: "test"}); err != nil {
		t.Fatalf("launchPersona() error: %v", err)
	}

	var killCount, spawnCount int
	for _, c := range runner.calls {
		if len(c) >= 2 && c[0] == "tmux" {
			switch c[1] {
			case "kill-window":
				killCount++
			case "new-window":
				spawnCount++
			}
		}
	}
	if killCount == 0 {
		t.Errorf("calls = %v, want at least one kill-window on weekly limit", runner.calls)
	}
	if spawnCount < 2 {
		t.Errorf("calls = %v, want at least 2 new-window calls for fallback agent", runner.calls)
	}
}

func TestLaunchPersonaSpawnsTmuxWindow(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	cat := loadTestCatalog(t)

	p := persona.Persona{
		ID: "qa-chromium", Name: "QA", Prompt: "check things",
		Model:     persona.Model{Agent: "claude"},
		Authority: persona.Authority{BDWrite: true},
	}
	cfg := store.DefaultConfig()

	if err := a.launchPersona(context.Background(), cfg, cat, p, personaSubject{brn: "baron-x", reason: "dispatched"}); err != nil {
		t.Fatalf("launchPersona() error: %v", err)
	}

	found := false
	for _, c := range runner.calls {
		if len(c) >= 4 && c[0] == "tmux" && c[1] == "new-window" && slices.Contains(c, "persona-qa-chromium") {
			found = true
		}
	}
	if !found {
		t.Errorf("tmux calls = %v, want a new-window for persona-qa-chromium", runner.calls)
	}

	events := auditEvents(t, a)
	auditFound := false
	for _, e := range events {
		if e.Action == "persona_run" && e.Target == "baron-x" {
			auditFound = true
			if e.Actor.Type != store.ActorPersona {
				t.Errorf("persona_run actor = %q, want %q", e.Actor.Type, store.ActorPersona)
			}
		}
	}
	if !auditFound {
		t.Errorf("audit events = %+v, want a persona_run event targeting baron-x", events)
	}
}

func TestLaunchPersonaRequiresTmux(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: ""} // tmux unavailable
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	cat := loadTestCatalog(t)

	p := persona.Persona{ID: "qa", Name: "QA", Prompt: "x", Model: persona.Model{Agent: "claude"}}
	cfg := store.DefaultConfig()

	if err := a.launchPersona(context.Background(), cfg, cat, p, personaSubject{}); err == nil {
		t.Fatal("launchPersona() with no tmux available: want an error, got nil")
	}
}

func TestReconcilePersonaTriggersFiresOnMatchingTransition(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) {
		return []persona.Persona{
			{ID: "manual", Name: "Manual", Prompt: "x", Enabled: true, Trigger: persona.Trigger{}}, // never fires: manual-only
			{
				ID: "qa", Name: "QA", Prompt: "check", Enabled: true,
				Model:     persona.Model{Agent: "claude"},
				Trigger:   persona.Trigger{On: []persona.TransitionRule{{From: "*", To: "merged"}}},
				Authority: persona.Authority{BDWrite: true},
			},
		}, nil
	}
	// Seed LastSeenStates on pass 1 (first observation never fires), then
	// transition the bead to merged and reconcile again.
	beadsWorking := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusOpen}}
	cfg := store.DefaultConfig()
	cat := loadTestCatalog(t)
	a.reconcilePersonaTriggers(context.Background(), cfg, beadsWorking, cat)

	beadsMerged := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusMerged}}
	a.reconcilePersonaTriggers(context.Background(), cfg, beadsMerged, cat)

	found := false
	for _, c := range runner.calls {
		if len(c) >= 2 && c[0] == "tmux" && c[1] == "new-window" && slices.Contains(c, "persona-qa") {
			found = true
		}
	}
	if !found {
		t.Errorf("tmux calls = %v, want qa to fire once its subscribed bead transitioned to merged", runner.calls)
	}
}

func TestReconcilePersonaTriggersSkipsNonMatchingTransition(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) {
		return []persona.Persona{
			{
				ID: "qa", Name: "QA", Prompt: "check", Enabled: true,
				Model:     persona.Model{Agent: "claude"},
				Trigger:   persona.Trigger{On: []persona.TransitionRule{{From: "*", To: "merged"}}},
				Authority: persona.Authority{BDWrite: true},
			},
		}, nil
	}
	beadsOpen := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusOpen}}
	cfg := store.DefaultConfig()
	cat := loadTestCatalog(t)
	a.reconcilePersonaTriggers(context.Background(), cfg, beadsOpen, cat)

	beadsWorking := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusWorking}}
	a.reconcilePersonaTriggers(context.Background(), cfg, beadsWorking, cat)

	for _, c := range runner.calls {
		if len(c) >= 2 && c[0] == "tmux" && c[1] == "new-window" && slices.Contains(c, "persona-qa") {
			t.Errorf("tmux calls = %v, want qa NOT to fire on open->working transition", runner.calls)
		}
	}
}

func TestReconcilePersonaTriggersSkipsDisabledAndManual(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) {
		return []persona.Persona{
			{ID: "manual", Name: "Manual", Prompt: "x", Enabled: true, Trigger: persona.Trigger{}},
			{
				ID: "disabled-qa", Name: "QA", Prompt: "check", Enabled: false,
				Trigger: persona.Trigger{On: []persona.TransitionRule{{From: "*", To: "merged"}}},
			},
		}, nil
	}
	cfg := store.DefaultConfig()
	cat := loadTestCatalog(t)

	a.reconcilePersonaTriggers(context.Background(), cfg, nil, cat)

	for _, c := range runner.calls {
		if len(c) >= 2 && c[0] == "tmux" && c[1] == "new-window" {
			t.Errorf("tmux calls = %v, want no window spawned (manual is manual-only, qa is disabled)", runner.calls)
		}
	}
}

func TestReconcilePersonaTriggersScheduleNotDueYet(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) {
		return []persona.Persona{
			{
				ID: "red-team", Name: "Red team", Prompt: "check", Enabled: true,
				Model:     persona.Model{Agent: "claude"},
				Trigger:   persona.Trigger{Schedule: "0 9 * * *"},
				Authority: persona.Authority{BDWrite: true},
			},
		}, nil
	}
	a.savePersonaState(personaState{LastRun: map[string]time.Time{"red-team": time.Now()}}) // just fired
	cfg := store.DefaultConfig()
	cat := loadTestCatalog(t)

	a.reconcilePersonaTriggers(context.Background(), cfg, nil, cat)

	for _, c := range runner.calls {
		if len(c) >= 2 && c[0] == "tmux" && c[1] == "new-window" {
			t.Errorf("tmux calls = %v, want no window spawned (cron schedule not due yet)", runner.calls)
		}
	}
}

func TestReconcilePersonaTriggersScheduleFirstObservationOnlySeeds(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) {
		return []persona.Persona{
			{
				ID: "red-team", Name: "Red team", Prompt: "check", Enabled: true,
				Model:     persona.Model{Agent: "claude"},
				Trigger:   persona.Trigger{Schedule: "0 9 * * *"},
				Authority: persona.Authority{BDWrite: true},
			},
		}, nil
	}
	cfg := store.DefaultConfig()
	cat := loadTestCatalog(t)

	a.reconcilePersonaTriggers(context.Background(), cfg, nil, cat)

	for _, c := range runner.calls {
		if len(c) >= 2 && c[0] == "tmux" && c[1] == "new-window" {
			t.Errorf("tmux calls = %v, want no window spawned on the very first check (seed-only, same rule beadTransitions uses)", runner.calls)
		}
	}
	state := a.loadPersonaState()
	if _, ok := state.LastRun["red-team"]; !ok {
		t.Error("personaState.LastRun has no entry for red-team, want it seeded on first observation")
	}
}
