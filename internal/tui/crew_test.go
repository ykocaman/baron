package tui

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

// testPersonas returns two non-manual personas — there is no built-in
// "Developer" persona any more (v8, docs/PRD/crew-mode.md §2.9): Prompt
// Mode's left-pane live agent chat is now the one fixed manual-prompt
// surface, so every persona in the system is an ordinary, triggerable one.
func testPersonas() []persona.Persona {
	return []persona.Persona{
		{
			ID: "clean-code", Name: "Clean-code reviewer", Description: "flags reinvented wheels",
			Prompt:    "review",
			Trigger:   persona.Trigger{}, // manual-only (persona.Trigger.Manual)
			Enabled:   true,
			Authority: persona.Authority{BDWrite: true, Actions: []string{"comment"}},
		},
		{
			ID: "qa-chromium", Name: "QA (Chromium)", Description: "checks closed beads",
			Prompt:    "qa",
			Trigger:   persona.Trigger{On: []persona.TransitionRule{{From: "*", To: "merged"}}},
			Enabled:   false,
			Authority: persona.Authority{BDWrite: true, Actions: []string{"reopen", "comment"}},
		},
	}
}

// unwrapBatch runs cmd against m, resolving one level of tea.BatchMsg by
// feeding every sub-command's result back through Update too — several
// handlers return a Batch (e.g. a toast plus a statuses refresh), so tests
// drive the whole thing through this instead of hand-unwrapping.
func unwrapBatch(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		next, _ := m.Update(msg)
		return asModel(next)
	}
	for _, c := range batch {
		next, _ := m.Update(c())
		m = asModel(next)
	}
	return m
}

// fakePromptSession builds an *agentTerminal backed by an os.Pipe instead
// of a real pty — steer()/sendKeys() just Write to *os.File, an os.Pipe's
// write end satisfies that identically, and the read end lets a test
// assert on exactly what was sent without spawning a real process or tmux.
// Callers must Close() the returned read end when done.
func fakePromptSession(t *testing.T) (*agentTerminal, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return &agentTerminal{pty: w}, r
}

// readPipeString reads whatever is already written to r (a fakePromptSession's
// read end) without blocking past a short deadline — steer() writes
// synchronously before quickPromptCmd's tea.Msg returns, so by the time a
// test calls this the bytes are already in the pipe's kernel buffer.
func readPipeString(t *testing.T, r *os.File) string {
	t.Helper()
	buf := make([]byte, 4096)
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("read fake pty: %v", err)
	}
	return string(buf[:n])
}

func TestPromptModeTogglesWithUppercaseP(t *testing.T) {
	m := New(context.Background(), testDeps()) // testDeps() leaves Personas nil
	next, _ := m.Update(key("P"))
	m = asModel(next)
	if m.screen != screenCrew {
		t.Fatalf("screen = %v, want screenCrew", m.screen)
	}
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	if !strings.Contains(m.View().Content, "aren't wired up") {
		t.Errorf("View() = %q, want a hint that Personas has no Deps.Personas", m.View().Content)
	}

	next, _ = m.Update(key("P"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want back to screenDashboard on second P", m.screen)
	}
}

func TestPromptModeLowercasePDoesNotToggleIt(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key("p"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want to stay on screenDashboard — lowercase p does not open Prompt Mode", m.screen)
	}
}

func TestPromptModeListsPersonas(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	if len(m.personas) != 2 {
		t.Fatalf("personas = %+v, want 2", m.personas)
	}
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	view := m.View().Content
	if !strings.Contains(view, "Clean-code reviewer") || !strings.Contains(view, "QA (Chromium)") {
		t.Errorf("View() = %q, want both persona names listed", view)
	}

	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.screen != screenCrew {
		t.Fatalf("screen = %v, want plain esc to stay in Prompt Mode (nothing open to close)", m.screen)
	}

	next, _ = m.Update(key("shift+esc"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want shift+esc to return to screenDashboard", m.screen)
	}
}

// TestBoardModeQuickPromptSendsToActiveAgentSession: Board Mode's 'p' no
// longer dispatches a persona (v8) — it types the entered text straight
// into whichever agent-CLI tab is currently active in Prompt Mode's left
// pane, prefixed with the bead's BRN for context.
func TestBoardModeQuickPromptSendsToActiveAgentSession(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-a", Title: "Fix login bug", Status: store.BeadStatusOpen},
	}})
	m = asModel(next)
	m.detail = m.beads[0]

	t2, r := fakePromptSession(t)
	m.promptAgentIDs = []string{"claude"}
	m.promptAgentTab = 0
	m.promptSessions = map[string]*agentTerminal{"claude": t2}

	next, _ = m.Update(key("p"))
	m = asModel(next)
	if !m.quickPromptMode {
		t.Fatal("quickPromptMode = false, want true after 'p' with a bead selected")
	}
	if m.quickPromptBRN != "baron-a" {
		t.Fatalf("quickPromptBRN = %q, want baron-a", m.quickPromptBRN)
	}

	for _, rn := range "fix the flaky test" {
		next, _ = m.Update(key(string(rn)))
		m = asModel(next)
	}
	next, cmd := m.Update(key("enter"))
	m = asModel(next)
	if m.quickPromptMode {
		t.Fatal("quickPromptMode = true, want false after submitting")
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the quick-prompt command")
	}
	unwrapBatch(t, m, cmd)

	sent := readPipeString(t, r)
	if !strings.Contains(sent, "baron-a") || !strings.Contains(sent, "fix the flaky test") {
		t.Errorf("sent to session = %q, want it to contain the BRN and the typed text", sent)
	}
}

func TestBoardModeDispatchNoSelectionIsNoop(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	next, _ := m.Update(key("p"))
	m = asModel(next)
	if m.quickPromptMode {
		t.Fatal("quickPromptMode = true, want false — nothing is selected")
	}
}

func TestDispatchEscCancelsWithoutFiring(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "X", Status: store.BeadStatusOpen}}})
	m = asModel(next)
	m.detail = m.beads[0]

	next, _ = m.Update(key("p"))
	m = asModel(next)
	for _, r := range "abandoned" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.quickPromptMode {
		t.Fatal("quickPromptMode = true, want false after esc")
	}
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want to stay on screenDashboard", m.screen)
	}
}

func TestPromptModeSpaceTogglesEnabled(t *testing.T) {
	var gotID string
	var gotEnabled bool
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.UpdatePersona = func(f persona.FormFields) error {
		gotID, gotEnabled = f.ID, f.Enabled
		return nil
	}
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	next, _ = m.Update(key("j")) // move to qa-chromium (enabled: false) — right-top has focus by default
	m = asModel(next)

	_, cmd := m.Update(key(" "))
	if cmd == nil {
		t.Fatal("cmd = nil, want the persona-update command")
	}
	cmd()

	if gotID != "qa-chromium" || !gotEnabled {
		t.Errorf("UpdatePersona(id=%q, enabled=%v), want (qa-chromium, true)", gotID, gotEnabled)
	}
}

func TestPromptModeFireNow(t *testing.T) {
	var gotID string
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.FireNow = func(id string) error {
		gotID = id
		return nil
	}
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	next, cmd := m.Update(key("r")) // clean-code selected by default
	m = asModel(next)
	if cmd == nil {
		t.Fatal("cmd = nil, want the fire-now command")
	}
	unwrapBatch(t, m, cmd)

	if gotID != "clean-code" {
		t.Errorf("FireNow(id=%q), want clean-code", gotID)
	}
}

func TestPromptModeEditFormOpensAndSubmits(t *testing.T) {
	var gotID, gotPrompt, gotDesc, gotSchedule, gotEvents string
	var gotEnabled bool
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.UpdatePersona = func(f persona.FormFields) error {
		gotID, gotPrompt, gotDesc, gotSchedule, gotEvents, gotEnabled = f.ID, f.Prompt, f.Description, f.Schedule, f.Events, f.Enabled
		return nil
	}
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	next, _ = m.Update(key("j")) // move to qa-chromium
	m = asModel(next)

	next, _ = m.Update(key("e"))
	m = asModel(next)
	if m.screen != screenForm || m.formKind != formKindPersonaEdit {
		t.Fatalf("screen=%v formKind=%v, want screenForm/formKindPersonaEdit after 'e'", m.screen, m.formKind)
	}
	if m.editingPersonaID != "qa-chromium" {
		t.Fatalf("editingPersonaID = %q, want qa-chromium", m.editingPersonaID)
	}
	if *m.formPersonaPromptResult != "qa" || *m.formDescResult != "checks closed beads" || *m.formPersonaScheduleResult != "" ||
		!slices.Equal(*m.formPersonaEventsResult, []string{"merged"}) || *m.formPersonaEnabledResult != "off" {
		t.Fatalf("form prefilled as prompt=%q desc=%q schedule=%q events=%v enabled=%q, want the persona's current values",
			*m.formPersonaPromptResult, *m.formDescResult, *m.formPersonaScheduleResult, *m.formPersonaEventsResult, *m.formPersonaEnabledResult)
	}

	*m.formPersonaPromptResult = "qa v2"
	*m.formDescResult = "now checks open beads too"
	*m.formPersonaScheduleResult = "0 9 * * *"
	*m.formPersonaEventsResult = []string{"merged", "closed"}
	*m.formPersonaEnabledResult = "on"
	next, cmd := m.submitForm()
	m = asModel(next)
	if m.screen != screenCrew {
		t.Fatalf("screen = %v, want back to screenCrew after submitting the persona edit form", m.screen)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the persona-update command")
	}
	cmd()

	if gotID != "qa-chromium" || gotPrompt != "qa v2" || gotDesc != "now checks open beads too" || gotSchedule != "0 9 * * *" ||
		gotEvents != "*->merged, *->closed" || !gotEnabled {
		t.Errorf("UpdatePersona(id=%q, prompt=%q, desc=%q, schedule=%q, events=%q, enabled=%v), want (qa-chromium, \"qa v2\", \"now checks open beads too\", \"0 9 * * *\", \"*->merged, *->closed\", true)",
			gotID, gotPrompt, gotDesc, gotSchedule, gotEvents, gotEnabled)
	}
}

func TestPromptModeNewPersonaFormCreates(t *testing.T) {
	var gotName, gotPrompt, gotEvents string
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.CreatePersona = func(f persona.FormFields) (string, error) {
		gotName, gotPrompt, gotEvents = f.Name, f.Prompt, f.Events
		return "test-id", nil
	}
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	next, _ = m.Update(key("n"))
	m = asModel(next)
	if m.screen != screenForm || m.formKind != formKindPersonaNew {
		t.Fatalf("screen=%v formKind=%v, want screenForm/formKindPersonaNew after 'n'", m.screen, m.formKind)
	}

	*m.formPersonaNameResult = "Red Team"
	*m.formPersonaPromptResult = "find bugs"
	*m.formPersonaEventsResult = []string{"open"}
	*m.formPersonaEnabledResult = "on"
	next, cmd := m.submitForm()
	m = asModel(next)
	if m.screen != screenCrew {
		t.Fatalf("screen = %v, want back to screenCrew after submitting the persona new form", m.screen)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the persona-create command")
	}
	cmd()

	if gotName != "Red Team" || gotPrompt != "find bugs" || gotEvents != "*->open" {
		t.Errorf("CreatePersona(name=%q, prompt=%q, events=%q), want (\"Red Team\", \"find bugs\", \"*->open\")",
			gotName, gotPrompt, gotEvents)
	}
}

// TestPromptModeNewPersonaFormRespectsTypedID is a regression test: the
// New Persona form's ID field ("leave blank to auto-generate") used to be
// captured into formPersonaIDResult but never read by the submit handler —
// deps.CreatePersona had no id parameter at all, so whatever a human typed
// there was silently discarded and the id was always derived from Name
// instead, contradicting the field's own on-screen description.
func TestPromptModeNewPersonaFormRespectsTypedID(t *testing.T) {
	var gotID string
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.CreatePersona = func(f persona.FormFields) (string, error) {
		gotID = f.ID
		return f.ID, nil
	}
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	next, _ = m.Update(key("n"))
	m = asModel(next)

	*m.formPersonaIDResult = "custom-slug"
	*m.formPersonaNameResult = "Ignored Display Name"
	*m.formPersonaPromptResult = "find bugs"
	_, cmd := m.submitForm()
	if cmd == nil {
		t.Fatal("cmd = nil, want the persona-create command")
	}
	cmd()

	if gotID != "custom-slug" {
		t.Errorf("CreatePersona(id=%q), want the form's own typed ID field (\"custom-slug\") honored, not derived from Name", gotID)
	}
}

// TestPromptModePersonaEventsSelectableAndSearchable verifies the
// new/edit-persona form's event-trigger picker lists all 11 canonical
// bd statuses. It's now a huh.MultiSelect: checkable options, filterable
// (type-to-search) — see personaEventOptions.
func TestPromptModePersonaEventsSelectableAndSearchable(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.CreatePersona = func(f persona.FormFields) (string, error) {
		return "id", nil
	}
	m := New(context.Background(), deps)
	m.width, m.height = 120, 80

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	_, _ = m.Update(key("n"))

	opts := personaEventOptions()
	for _, want := range []string{
		"open", "assigned", "working", "validating", "retry", "blocked",
		"mergable", "human_queue", "merged", "closed", "cancelled",
	} {
		found := false
		for _, opt := range opts {
			if opt.Value == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("personaEventOptions missing status %q", want)
		}
	}
}

func TestPromptModePersonaEventsFilterEscDoesNotAbortForm(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.CreatePersona = func(f persona.FormFields) (string, error) {
		return "id", nil
	}
	m := New(context.Background(), deps)
	m.width, m.height = 120, 80

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	next, _ = m.Update(key("n"))
	m = asModel(next)

	for _, r := range "red-team" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	for range 5 { // ID -> Name -> Prompt -> Description -> Cron schedule -> Events
		next, _ = m.Update(key("tab"))
		m = asModel(next)
	}
	if m.screen != screenForm {
		t.Fatalf("screen = %v, want still screenForm after tabbing to the Events field — test setup broken", m.screen)
	}

	next, _ = m.Update(key("/"))
	m = asModel(next)
	next, _ = m.Update(key("z")) // a query that matches nothing
	m = asModel(next)
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.screen != screenForm {
		t.Fatalf("screen = %v, want still screenForm — esc while filtering should clear the search, not abort the form", m.screen)
	}
	if *m.formPersonaIDResult != "red-team" {
		t.Errorf("formPersonaIDResult = %q, want red-team preserved — the form must still be the same live one, not reopened fresh", *m.formPersonaIDResult)
	}

	// ctrl+c aborts form
	next, _ = m.Update(key("ctrl+c"))
	m = asModel(next)
	if m.screen != screenCrew {
		t.Fatalf("screen = %v, want screenCrew — ctrl+c must abort the form", m.screen)
	}
}

func TestPromptModePersonaEditPreservesCustomEventRule(t *testing.T) {
	custom := persona.TransitionRule{From: "working", To: "retry"}
	personas := []persona.Persona{{
		ID: "watcher", Name: "Watcher", Prompt: "watch and report",
		Trigger: persona.Trigger{On: []persona.TransitionRule{{From: "*", To: "merged"}, custom}},
	}}
	var gotEvents string
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return personas, nil }
	deps.UpdatePersona = func(f persona.FormFields) error {
		gotEvents = f.Events
		return nil
	}
	m := New(context.Background(), deps)
	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads -> Personas
	m = asModel(next)
	next, _ = m.Update(key("e"))
	m = asModel(next)
	if m.screen != screenForm {
		t.Fatalf("screen = %v, want screenForm after 'e'", m.screen)
	}
	if !slices.Equal(m.formPersonaEventsCustom, []persona.TransitionRule{custom}) {
		t.Fatalf("formPersonaEventsCustom = %v, want [%v] preserved from the persona's existing rules", m.formPersonaEventsCustom, custom)
	}

	// Submit untouched — the custom rule must still round-trip through.
	_, cmd := m.submitForm()
	if cmd == nil {
		t.Fatal("cmd = nil, want the persona-update command")
	}
	cmd()
	if gotEvents != "*->merged, working->retry" {
		t.Errorf("UpdatePersona events=%q, want \"*->merged, working->retry\" (preset + preserved custom rule)", gotEvents)
	}
}
