package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

func TestUpdatePersonaByIDSetsEventTriggers(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	a.loadPersonas = func() ([]persona.Persona, error) {
		return []persona.Persona{
			{ID: "qa", Name: "QA", Prompt: "check", Trigger: persona.Trigger{On: []persona.TransitionRule{{From: "*", To: "merged"}}}},
		}, nil
	}
	var saved persona.Persona
	a.savePersona = func(p persona.Persona) error {
		saved = p
		return nil
	}

	if err := a.UpdatePersonaByID(persona.FormFields{ID: "qa", Prompt: "check", Description: "new desc", Schedule: "0 9 * * *", Events: "*->merged, working->retry", ModelTier: "standard", Enabled: true}); err != nil {
		t.Fatalf("UpdatePersonaByID: %v", err)
	}
	want := []persona.TransitionRule{{From: "*", To: "merged"}, {From: "working", To: "retry"}}
	if saved.Description != "new desc" || saved.Trigger.Schedule != "0 9 * * *" || !saved.Enabled {
		t.Fatalf("saved = %+v, want description/schedule/enabled updated", saved)
	}
	if len(saved.Trigger.On) != len(want) {
		t.Fatalf("saved.Trigger.On = %+v, want %+v", saved.Trigger.On, want)
	}
	for i := range want {
		if saved.Trigger.On[i] != want[i] {
			t.Errorf("saved.Trigger.On[%d] = %+v, want %+v", i, saved.Trigger.On[i], want[i])
		}
	}
}

func TestUpdatePersonaByIDClearsEventTriggersOnEmptyText(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	a.loadPersonas = func() ([]persona.Persona, error) {
		return []persona.Persona{
			{ID: "qa", Name: "QA", Prompt: "check", Trigger: persona.Trigger{On: []persona.TransitionRule{{From: "*", To: "merged"}}}},
		}, nil
	}
	var saved persona.Persona
	a.savePersona = func(p persona.Persona) error {
		saved = p
		return nil
	}

	if err := a.UpdatePersonaByID(persona.FormFields{ID: "qa", Prompt: "check", Description: "desc", ModelTier: "standard"}); err != nil {
		t.Fatalf("UpdatePersonaByID: %v", err)
	}
	if len(saved.Trigger.On) != 0 {
		t.Errorf("saved.Trigger.On = %+v, want cleared by blank events text", saved.Trigger.On)
	}
}

func TestUpdatePersonaByIDRejectsMalformedEventTriggers(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	a.loadPersonas = func() ([]persona.Persona, error) {
		return []persona.Persona{{ID: "qa", Name: "QA", Prompt: "check"}}, nil
	}
	a.savePersona = func(persona.Persona) error {
		t.Fatal("savePersona called, want the malformed events text rejected before saving")
		return nil
	}

	if err := a.UpdatePersonaByID(persona.FormFields{ID: "qa", Prompt: "check", Description: "desc", Events: "merged", ModelTier: "standard"}); err == nil {
		t.Fatal("UpdatePersonaByID(events=\"merged\") error = nil, want an error (missing \"->\")")
	}
}

func TestUpdatePersonaByIDSetsAndClearsSkills(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	a.loadPersonas = func() ([]persona.Persona, error) {
		return []persona.Persona{{ID: "qa", Name: "QA", Prompt: "check"}}, nil
	}
	var saved persona.Persona
	a.savePersona = func(p persona.Persona) error {
		saved = p
		return nil
	}

	if err := a.UpdatePersonaByID(persona.FormFields{ID: "qa", Prompt: "check", Description: "desc", Skills: "code-review-skill, https://example.com/plugin.zip", ModelTier: "standard"}); err != nil {
		t.Fatalf("UpdatePersonaByID: %v", err)
	}
	want := []string{"code-review-skill", "https://example.com/plugin.zip"}
	if len(saved.Skills) != len(want) || saved.Skills[0] != want[0] || saved.Skills[1] != want[1] {
		t.Fatalf("saved.Skills = %+v, want %+v", saved.Skills, want)
	}

	if err := a.UpdatePersonaByID(persona.FormFields{ID: "qa", Prompt: "check", Description: "desc", ModelTier: "standard"}); err != nil {
		t.Fatalf("UpdatePersonaByID: %v", err)
	}
	if saved.Skills != nil {
		t.Errorf("saved.Skills = %+v, want cleared by blank skills text", saved.Skills)
	}
}

func TestPersonaSkillExtrasOnlyForClaude(t *testing.T) {
	for _, agentName := range []string{"opencode", "agy", "codex", "gemini", "cline"} {
		args, err := personaSkillExtras(agentName, "qa", []string{"some-skill"})
		if err != nil || args != nil {
			t.Errorf("personaSkillExtras(%q, ...) = %v, %v, want nil, nil (skills are claude-only)", agentName, args, err)
		}
	}
}

func TestPersonaSkillExtrasURLPassesThroughUntouched(t *testing.T) {
	args, err := personaSkillExtras("claude", "qa", []string{"https://example.com/plugin.zip"})
	if err != nil {
		t.Fatalf("personaSkillExtras: %v", err)
	}
	want := []string{"--plugin-url", "https://example.com/plugin.zip"}
	if !slices.Equal(args, want) {
		t.Errorf("personaSkillExtras = %v, want %v", args, want)
	}
}

func TestPersonaSkillExtrasInstallsNpmPackage(t *testing.T) {
	orig := runNpmInstall
	defer func() { runNpmInstall = orig }()
	var gotDir, gotSpec string
	runNpmInstall = func(dir, spec string) (string, error) {
		gotDir, gotSpec = dir, spec
		return "/scratch/baron-persona-skills/qa/node_modules/code-review-skill", nil
	}

	args, err := personaSkillExtras("claude", "qa", []string{"code-review-skill@1.2.3"})
	if err != nil {
		t.Fatalf("personaSkillExtras: %v", err)
	}
	want := []string{"--plugin-dir", "/scratch/baron-persona-skills/qa/node_modules/code-review-skill"}
	if !slices.Equal(args, want) {
		t.Errorf("personaSkillExtras = %v, want %v", args, want)
	}
	if gotSpec != "code-review-skill@1.2.3" {
		t.Errorf("runNpmInstall spec = %q, want %q", gotSpec, "code-review-skill@1.2.3")
	}
	if !strings.Contains(gotDir, "qa") {
		t.Errorf("runNpmInstall dir = %q, want it scoped to persona id %q", gotDir, "qa")
	}
}

func TestPersonaSkillExtrasPropagatesInstallError(t *testing.T) {
	orig := runNpmInstall
	defer func() { runNpmInstall = orig }()
	runNpmInstall = func(dir, spec string) (string, error) {
		return "", fmt.Errorf("npm install %s: 404 Not Found", spec)
	}

	if _, err := personaSkillExtras("claude", "qa", []string{"does-not-exist"}); err == nil {
		t.Fatal("personaSkillExtras error = nil, want the npm install failure surfaced")
	}
}

// TestPersonaSkillExtrasKnownSkillUsesOpenskills: a skill name that appears in
// persona.SkillCatalog() must be fetched via runOpenskillsInstall (the
// catalog/openskills branch), NOT via runNpmInstall (the bare npm branch) —
// the anthropics/skills monorepo is not npm-published, so sending it to npm
// would fail. An unknown spec (not in the catalog, not a URL) still falls
// through to the npm branch unchanged.
func TestPersonaSkillExtrasKnownSkillUsesOpenskills(t *testing.T) {
	origNpm := runNpmInstall
	origOS := runOpenskillsInstall
	defer func() {
		runNpmInstall = origNpm
		runOpenskillsInstall = origOS
	}()

	var npmCalled bool
	runNpmInstall = func(dir, spec string) (string, error) {
		npmCalled = true
		return "/npm-path", nil
	}
	var gotSkillName string
	runOpenskillsInstall = func(dir, skillName string) (string, error) {
		gotSkillName = skillName
		return "/catalog-path/" + skillName, nil
	}

	// "pdf" is in SkillCatalog(); "my-custom-pkg" is not.
	args, err := personaSkillExtras("claude", "qa", []string{"pdf", "my-custom-pkg"})
	if err != nil {
		t.Fatalf("personaSkillExtras: %v", err)
	}
	if npmCalled && gotSkillName == "" {
		t.Error("runNpmInstall called for a catalog skill — want runOpenskillsInstall instead")
	}
	if gotSkillName != "pdf" {
		t.Errorf("runOpenskillsInstall skillName = %q, want pdf", gotSkillName)
	}
	// The catalog skill must appear as --plugin-dir, not --plugin-url.
	wantArgs := []string{"--plugin-dir", "/catalog-path/pdf", "--plugin-dir", "/npm-path"}
	if !slices.Equal(args, wantArgs) {
		t.Errorf("personaSkillExtras = %v, want %v", args, wantArgs)
	}
}

// TestPersonaSkillExtrasCatalogSkillErrorSurfaces: openskills failure for a
// catalog-known skill must propagate as an error (not silently succeed).
func TestPersonaSkillExtrasCatalogSkillErrorSurfaces(t *testing.T) {
	orig := runOpenskillsInstall
	defer func() { runOpenskillsInstall = orig }()
	runOpenskillsInstall = func(dir, skillName string) (string, error) {
		return "", fmt.Errorf("openskills install %s: command not found", skillName)
	}

	if _, err := personaSkillExtras("claude", "qa", []string{"pdf"}); err == nil {
		t.Fatal("personaSkillExtras error = nil, want the openskills failure surfaced")
	}
}

func TestBeadTransitionsFirstSightDoesNotFire(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	beads := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusClosed}}
	state := &personaState{}

	if got := a.beadTransitions(beads, state); len(got) != 0 {
		t.Errorf("beadTransitions() = %+v on first-ever observation, want none (nothing to compare against)", got)
	}
	if state.LastSeenStates["baron-x"] != string(store.BeadStatusClosed) {
		t.Errorf("LastSeenStates[baron-x] = %q, want seeded to %q", state.LastSeenStates["baron-x"], store.BeadStatusClosed)
	}
}

func TestBeadTransitionsDetectsChange(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	state := &personaState{LastSeenStates: map[string]string{"baron-x": "working"}}
	beads := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusClosed}}

	got := a.beadTransitions(beads, state)
	if len(got) != 1 || got[0].from != "working" || got[0].to != "closed" {
		t.Errorf("beadTransitions() = %+v, want one working->closed transition", got)
	}
}

func TestBeadTransitionsNoChangeNoTransition(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	state := &personaState{LastSeenStates: map[string]string{"baron-x": "closed"}}
	beads := []store.Bead{{ID: "baron-x", BRN: "baron-x", Status: store.BeadStatusClosed}}

	if got := a.beadTransitions(beads, state); len(got) != 0 {
		t.Errorf("beadTransitions() = %+v, want none (already closed last pass too)", got)
	}
}

func TestMatchesTransitionWildcards(t *testing.T) {
	on := []persona.TransitionRule{{From: "*", To: "merged"}}
	if !matchesTransition(on, "working", "merged") {
		t.Error("matchesTransition() = false, want true (wildcard from)")
	}
	if matchesTransition(on, "working", "closed") {
		t.Error("matchesTransition() = true, want false (to doesn't match)")
	}
}

func TestMatchesIssueTypeEmptyMatchesAll(t *testing.T) {
	if !matchesIssueType(store.Bead{IssueType: "bug"}, nil) {
		t.Error("matchesIssueType() = false with no restriction, want true")
	}
	if !matchesIssueType(store.Bead{IssueType: "bug"}, []string{"bug", "task"}) {
		t.Error("matchesIssueType() = false, want true (bug is in the list)")
	}
	if matchesIssueType(store.Bead{IssueType: "feature"}, []string{"bug"}) {
		t.Error("matchesIssueType() = true, want false (feature not in the list)")
	}
}

func TestCronDueNeverFiresOnFirstObservation(t *testing.T) {
	if cronDue("0 9 * * *", time.Time{}, time.Now()) {
		t.Error("cronDue() = true with a zero lastRun, want false (first observation only seeds)")
	}
}

func TestCronDueRespectsSchedule(t *testing.T) {
	last := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	if cronDue("0 9 * * *", last, last.Add(time.Hour)) {
		t.Error("cronDue() = true one hour after a 09:00 daily run, want false")
	}
	if !cronDue("0 9 * * *", last, last.Add(25*time.Hour)) {
		t.Error("cronDue() = false 25h after a 09:00 daily run, want true (next day's 09:00 has passed)")
	}
}

func TestPersonaPromptSingleBeadContract(t *testing.T) {
	a := &app{}
	p := persona.Persona{ID: "qa", Prompt: "do the thing", Authority: persona.Authority{Actions: []string{"comment", "reopen"}}}
	subj := personaSubject{brn: "baron-42", reason: "dispatched"}
	got := a.personaPrompt(p, subj)
	if !strings.Contains(got, "comment, reopen") {
		t.Errorf("personaPrompt() = %q, want it to name the allowed actions", got)
	}
	if !strings.Contains(got, "baron-42") || !strings.Contains(got, "persona:qa") {
		t.Errorf("personaPrompt() = %q, want the subject bead and the persona's own --actor tag", got)
	}
	if !strings.Contains(got, "do the thing") {
		t.Errorf("personaPrompt() = %q, want the persona's own prompt appended", got)
	}
}

func TestPersonaPromptCandidateList(t *testing.T) {
	a := &app{}
	p := persona.Persona{ID: "red-team", Prompt: "sweep for CVEs"}
	subj := personaSubject{
		candidates: []store.Bead{{BRN: "baron-a", Title: "Fix login", Status: store.BeadStatusOpen}},
		reason:     "cron sweep (1 candidates)",
	}
	got := a.personaPrompt(p, subj)
	if !strings.Contains(got, "baron-a") || !strings.Contains(got, "Fix login") {
		t.Errorf("personaPrompt() = %q, want the candidate bead listed", got)
	}
	if !strings.Contains(got, "sweep for CVEs") {
		t.Errorf("personaPrompt() = %q, want the persona's own prompt appended", got)
	}
}

func TestPersonaPromptNoActionsRestrictionOmitted(t *testing.T) {
	a := &app{}
	p := persona.Persona{Prompt: "do the thing"}
	got := a.personaPrompt(p, personaSubject{})
	if strings.Contains(got, "Yalnızca") {
		t.Errorf("personaPrompt() = %q, want no action-restriction line when Authority.Actions is empty", got)
	}
	if !strings.Contains(got, "do the thing") {
		t.Errorf("personaPrompt() = %q, want the prompt present", got)
	}
}

// tmuxPersonaRunner scripts a healthy tmux server for launchPersona and
// records every tmux + agent invocation, same shape as tmuxrun_test.go's
// tmuxRunRunner but without a gate-running inner (persona launches never
// run the gate).
type tmuxPersonaRunner struct {
	tmuxOut string
	calls   [][]string
	// captureOut is a FIFO of strings returned by successive capture-pane
	// calls; when exhausted subsequent calls return "".
	captureOut []string
}

func (r *tmuxPersonaRunner) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if name == "bd" {
		return tool.Result{Stdout: `[{"id":"1","title":"Fix bug","type":"bug","status":"open"},{"id":"2","title":"Add feature","type":"feature","status":"closed"}]`}, nil
	}
	if name != "tmux" {
		return tool.Result{}, nil // the agent CLI itself — persona launches don't wait on it
	}
	switch {
	case slices.Equal(args, []string{"-V"}):
		return tool.Result{Stdout: r.tmuxOut}, nil
	case len(args) > 2 && args[0] == "has-session" && strings.Contains(args[2], ":"):
		// WindowExists' own has-session -t session:window (used both by
		// personaWindowBusy's exists-check and Spawn's own doesn't-matter
		// kill-window) — "not found" so every persona launch in this test
		// file always sees a clean session with no window running yet.
		return tool.Result{Stderr: "can't find window: " + args[2]}, errNotFound
	case len(args) > 0 && (args[0] == "has-session" || args[0] == "kill-window" || args[0] == "new-window"):
		return tool.Result{}, nil
	case len(args) > 0 && args[0] == "capture-pane":
		if len(r.captureOut) > 0 {
			out := r.captureOut[0]
			r.captureOut = r.captureOut[1:]
			return tool.Result{Stdout: out}, nil
		}
		return tool.Result{}, nil
	}
	return tool.Result{}, nil
}

var errNotFound = fmt.Errorf("no such tmux target")
