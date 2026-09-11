package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

func TestCapturePersonaEditFormWithFreeze(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	m := New(context.Background(), deps)
	m.width, m.height = 100, 36

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Personas
	m = asModel(next)
	next, _ = m.Update(key("e")) // Edit form
	m = asModel(next)

	content := stripANSI(m.View().Content)
	lines := strings.Split(content, "\n")
	var cleaned []string
	for _, l := range lines {
		cleaned = append(cleaned, strings.TrimRight(l, " "))
	}
	finalStr := strings.Join(cleaned, "\n")
	txtPath := "/tmp/persona_edit_form.txt"
	_ = os.WriteFile(txtPath, []byte(finalStr), 0o600)

	targetPNG := "/Users/yusuf/.gemini/antigravity-cli/brain/2ed52372-ed6d-4f7e-8486-2137c565dd96/persona_edit_form.png"
	fcmd := exec.CommandContext(t.Context(), "freeze", txtPath, "--language", "text", "--window", "-o", targetPNG)
	if out, err := fcmd.CombinedOutput(); err != nil {
		t.Logf("freeze run err: %v\n%s", err, out)
	}
}

func TestCaptureLivePersonasWithFreeze(t *testing.T) {
	// ponytail: deterministic model rendering — live tmux capture was flaky
	// (cursor stuck on red-team, all 5 PNGs identical). No tmux needed.
	personas := []persona.Persona{
		{ID: "clean-code", Name: "Clean-code reviewer", Description: "Flags reinvented wheels", Prompt: "review", Enabled: true},
		{ID: "closer", Name: "Closer", Description: "Re-checks merged change", Prompt: "closer", Enabled: true},
		{ID: "reviewer", Name: "Reviewer", Description: "Reviews diff vs acceptance", Prompt: "reviewer", Enabled: true},
		{ID: "qa-chromium", Name: "QA (Chromium)", Description: "E2E in live browser", Prompt: "qa", Enabled: true},
		{ID: "red-team", Name: "Red team", Description: "Daily CVE sweep", Prompt: "red-team", Enabled: true},
	}
	outputs := map[string]string{
		"clean-code":  "sort.SliceStable refactoring\nslices.SortFunc cmp.Compare line 42\n-- analysis: replace with slices.SortFunc",
		"closer":      "Test suites running\nok   github.com/baron-cli/baron/internal/tui  106.067s\ninternal/tui test logs waiting",
		"reviewer":    "15 candidate beads — commit review\ncommit abc123 meets acceptance criteria\ncommit def456 needs rework",
		"qa-chromium": "Sweep summary: 3 candidates scanned\nQA Chromium candidate state: pass 2 / fail 1\ntest coverage: 94%",
		"red-team":    "31 third-party deps scanned\nGo stdlib CVE scan: 0 vulns\ndependency audit: all clear",
	}
	cases := []struct {
		file string
		idx  int
	}{
		{"persona_1_clean_code", 0},
		{"persona_2_closer", 1},
		{"persona_3_reviewer", 2},
		{"persona_4_qa", 3},
		{"persona_5_red_team", 4},
	}
	for _, c := range cases {
		m := New(context.Background(), Deps{
			Personas: func() ([]persona.Persona, error) { return personas, nil },
			PersonaStatuses: func(ids []string) (map[string]PersonaStatus, error) {
				return map[string]PersonaStatus{}, nil
			},
			PromptAgentIDs:    func() ([]string, error) { return []string{"agy", "claude", "gemini", "opencode"}, nil },
			PromptAgentEnsure: func(agentID string) (string, bool, error) { return "persona-" + agentID, true, nil },
			Version:           "dev",
		})
		m.width, m.height = 105, 32
		m.screen = screenCrew
		m.promptRightTab = promptTabPersonas
		m.promptPersonaCursor = c.idx
		m.personas = personas
		m.promptPersonaOutput = outputs
		m.promptPersonaScroll = 0
		m.promptFocus = promptPaneRightTop
		m.promptPersonaOpenID = ""
		m.promptAgentIDs = []string{"agy", "claude", "gemini", "opencode"}
		content := stripANSI(m.View().Content)
		lines := strings.Split(content, "\n")
		var cleaned []string
		for _, l := range lines {
			cleaned = append(cleaned, strings.TrimRight(l, " "))
		}
		txtPath := filepath.Join(os.TempDir(), c.file+".txt")
		_ = os.WriteFile(txtPath, []byte(strings.Join(cleaned, "\n")), 0o600)
		targetPNG := filepath.Join("/Users/yusuf/.gemini/antigravity-cli/brain/c00b343f-595a-4e6e-9891-a08948219353", c.file+".png")
		fcmd := exec.CommandContext(t.Context(), "freeze", txtPath, "--language", "text", "--window", "-o", targetPNG)
		if fOut, fErr := fcmd.CombinedOutput(); fErr != nil {
			t.Logf("freeze %s err: %v (%s)", c.file, fErr, fOut)
		} else {
			t.Logf("Saved freeze screenshot: %s", targetPNG)
		}
	}
}

// assertPersonaSelectionPreserved fails (non-fatally, so later assertions in
// the same test still run) unless the currently selected persona is id.
func assertPersonaSelectionPreserved(t *testing.T, m Model, id string) {
	t.Helper()
	sel, ok := m.selectedPersona()
	if !ok || sel.ID != id {
		t.Errorf("selectedPersona = %v, want %q preserved", sel.ID, id)
	}
}

func TestPromptModePersonaRunAutoExpandsAndPreservesCursor(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	firedID := ""
	deps.FireNow = func(id string) error {
		firedID = id
		return nil
	}
	deps.PersonaActivity = func(id string) ([]store.AuditEvent, error) {
		return []store.AuditEvent{
			{Time: time.Now(), Action: "persona_run", Target: "bd-1", Detail: "dispatched"},
		}, nil
	}
	deps.PersonaOutput = func(id string) (string, error) {
		return "run output line 1\nrun output line 2\nrun output line 3\n", nil
	}

	m := New(context.Background(), deps)
	m.width, m.height = 100, 36

	// 1. Open Prompt Mode and switch to Personas tab
	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)

	// 2. Move to 2nd persona (qa-chromium)
	next, _ = m.Update(key("j"))
	m = asModel(next)
	sel, ok := m.selectedPersona()
	if !ok || sel.ID != "qa-chromium" {
		t.Fatalf("selectedPersona = %v, want qa-chromium", sel.ID)
	}

	// 3. Press 'r' to run persona
	next, cmd := m.Update(key("r"))
	m = asModel(next)
	if m.promptPersonaOpenID != "qa-chromium" {
		t.Errorf("promptPersonaOpenID = %q, want qa-chromium after 'r'", m.promptPersonaOpenID)
	}
	// Execute batched commands to trigger FireNow, load activity and output
	if cmd != nil {
		for _, msg := range flattenBatch(cmd()) {
			next, _ = m.Update(msg)
			m = asModel(next)
		}
	}
	if firedID != "qa-chromium" {
		t.Errorf("firedID = %q, want qa-chromium", firedID)
	}
	assertPersonaSelectionPreserved(t, m, "qa-chromium")

	// 4. Press 'l' to toggle accordion closed
	next, _ = m.Update(key("l"))
	m = asModel(next)
	if m.promptPersonaOpenID != "" {
		t.Errorf("promptPersonaOpenID = %q, want empty after closing accordion", m.promptPersonaOpenID)
	}
	assertPersonaSelectionPreserved(t, m, "qa-chromium")

	// 5. Press 'l' to toggle accordion open again
	next, _ = m.Update(key("l"))
	m = asModel(next)
	if m.promptPersonaOpenID != "qa-chromium" {
		t.Errorf("promptPersonaOpenID = %q, want qa-chromium after re-opening", m.promptPersonaOpenID)
	}
	assertPersonaSelectionPreserved(t, m, "qa-chromium")

	// 6. Test scrolling right-bottom detail pane (RightTop -> RightBottom)
	next, _ = m.Update(key("tab")) // focus right-bottom
	m = asModel(next)
	if m.promptFocus != promptPaneRightBottom {
		t.Errorf("promptFocus = %v, want promptPaneRightBottom", m.promptFocus)
	}
	next, _ = m.Update(key("j"))
	m = asModel(next)
	if m.promptPersonaDetailScroll != 1 {
		t.Errorf("promptPersonaDetailScroll = %d, want 1 after pressing j", m.promptPersonaDetailScroll)
	}
	next, _ = m.Update(key("k"))
	m = asModel(next)
	if m.promptPersonaDetailScroll != 0 {
		t.Errorf("promptPersonaDetailScroll = %d, want 0 after pressing k", m.promptPersonaDetailScroll)
	}
}

func TestPromptModeExampleWorkClosedWithFreeze(t *testing.T) {
	outDir := "/Users/yusuf/.gemini/antigravity-cli/brain/c00b343f-595a-4e6e-9891-a08948219353"
	_ = os.MkdirAll(outDir, 0o750)

	beadOpen := store.Bead{
		ID:        "baron-ex1",
		BRN:       domain.BRN("baron-ex1"),
		Title:     "Add Greet function with unit tests",
		IssueType: "task",
		Status:    store.BeadStatusOpen,
		Assignee:  "claude",
		Priority:  store.Priority("P2"),
	}

	beadWorking := store.Bead{
		ID:        "baron-ex1",
		BRN:       domain.BRN("baron-ex1"),
		Title:     "Add Greet function with unit tests",
		IssueType: "task",
		Status:    store.BeadStatusWorking,
		Assignee:  "claude",
		Priority:  store.Priority("P2"),
	}

	beadClosed := store.Bead{
		ID:        "baron-ex1",
		BRN:       domain.BRN("baron-ex1"),
		Title:     "Add Greet function with unit tests",
		IssueType: "task",
		Status:    store.BeadStatusClosed,
		Assignee:  "claude",
		Priority:  store.Priority("P2"),
	}

	stages := []struct {
		name string
		bead store.Bead
		tab  int
	}{
		{"prompt_bead_1_open", beadOpen, 0},
		{"prompt_bead_2_working", beadWorking, 0},
		{"prompt_bead_3_closed", beadClosed, 4},
	}

	for _, s := range stages {
		deps := testDeps()
		deps.PromptAgentIDs = func() ([]string, error) { return []string{"agy", "claude", "gemini", "opencode"}, nil }
		deps.PromptAgentEnsure = func(agentID string) (string, bool, error) { return "persona-" + agentID, true, nil }

		m := New(context.Background(), deps)
		m.width, m.height = 105, 32
		m.screen = screenCrew
		m.promptRightTab = promptTabBeads
		m.promptBeadTab = s.tab
		m.promptBeadCursor = 0
		m.beads = []store.Bead{s.bead}
		m.detail = s.bead
		m.promptFocus = promptPaneRightTop
		m.promptAgentIDs = []string{"agy", "claude", "gemini", "opencode"}

		content := stripANSI(m.View().Content)
		lines := strings.Split(content, "\n")
		var cleaned []string
		for _, l := range lines {
			cleaned = append(cleaned, strings.TrimRight(l, " "))
		}
		txtPath := filepath.Join(os.TempDir(), s.name+".txt")
		_ = os.WriteFile(txtPath, []byte(strings.Join(cleaned, "\n")), 0o600)
		targetPNG := filepath.Join(outDir, s.name+".png")
		fcmd := exec.CommandContext(t.Context(), "freeze", txtPath, "--language", "text", "--window", "-o", targetPNG)
		if fOut, fErr := fcmd.CombinedOutput(); fErr != nil {
			t.Logf("freeze %s err: %v (%s)", s.name, fErr, fOut)
		} else {
			t.Logf("Saved freeze screenshot: %s", targetPNG)
		}
	}
}

func TestCaptureAllScreensAndModalsWithFreeze(t *testing.T) {
	outDir := "/Users/yusuf/.gemini/antigravity-cli/brain/c00b343f-595a-4e6e-9891-a08948219353"
	_ = os.MkdirAll(outDir, 0o750)

	bead := store.Bead{
		ID:                 "baron-f1",
		BRN:                domain.BRN("baron-f1"),
		Title:              "Add high-performance caching layer",
		Description:        "Implement a memory-bounded LRU cache with eviction telemetry and concurrency tests.",
		AcceptanceCriteria: "All unit tests pass with -race; zero allocations on cache hit.",
		IssueType:          "task",
		Status:             store.BeadStatusWorking,
		Assignee:           "claude",
		Priority:           store.Priority("P1"),
	}

	personas := testPersonas()

	tests := []struct {
		name  string
		setup func(m *Model)
	}{
		{
			name: "screen_board_overview",
			setup: func(m *Model) {
				m.screen = screenDashboard
				m.detailTab = 0
				m.beads = []store.Bead{bead}
				m.detail = bead
			},
		},
		{
			name: "screen_board_diff",
			setup: func(m *Model) {
				m.screen = screenDashboard
				m.detailTab = 2
				m.beads = []store.Bead{bead}
				m.detail = bead
			},
		},
		{
			name: "screen_board_audit",
			setup: func(m *Model) {
				m.screen = screenDashboard
				m.detailTab = 3
				m.beads = []store.Bead{bead}
				m.detail = bead
			},
		},
		{
			name: "screen_prompt_beads_backlog",
			setup: func(m *Model) {
				m.screen = screenCrew
				m.promptRightTab = promptTabBeads
				m.promptBeadTab = 0
				m.promptFocus = promptPaneRightTop
				m.beads = []store.Bead{bead}
				m.detail = bead
			},
		},
		{
			name: "screen_prompt_personas_overview",
			setup: func(m *Model) {
				m.screen = screenCrew
				m.promptRightTab = promptTabPersonas
				m.promptPersonaCursor = 0
				m.promptFocus = promptPaneRightTop
				m.personas = personas
			},
		},
		{
			name: "screen_prompt_personas_accordion",
			setup: func(m *Model) {
				m.screen = screenCrew
				m.promptRightTab = promptTabPersonas
				m.promptPersonaCursor = 0
				m.promptPersonaOpenID = "clean-code"
				m.promptFocus = promptPaneRightTop
				m.personas = personas
				m.promptPersonaActivity = map[string][]store.AuditEvent{
					"clean-code": {
						{Time: time.Now().Add(-5 * time.Minute), Action: "persona_run", Target: "baron-f1", Detail: "analyzed diff, suggested table tests"},
					},
				}
				m.promptPersonaOutput = map[string]string{
					"clean-code": "Analyzing baron-f1: caching layer conforms to clean architecture.",
				}
			},
		},
		{
			name: "screen_prompt_agent_chat",
			setup: func(m *Model) {
				m.screen = screenCrew
				m.promptFocus = promptPaneLeft
				m.promptRightTab = promptTabBeads
				m.promptAgentTab = 1
				m.promptAgentIDs = []string{"agy", "claude", "gemini", "opencode"}
			},
		},
		{
			name: "screen_help_modal",
			setup: func(m *Model) {
				m.screen = screenHelp
			},
		},
		{
			name: "screen_quick_prompt",
			setup: func(m *Model) {
				m.screen = screenDashboard
				m.quickPromptMode = true
				m.quickPromptInput.SetValue("Investigate cache eviction timeout")
				m.quickPromptBRN = "baron-f1"
				m.beads = []store.Bead{bead}
				m.detail = bead
			},
		},
	}

	for _, tc := range tests {
		deps := testDeps()
		deps.Personas = func() ([]persona.Persona, error) { return personas, nil }
		deps.PersonaStatuses = func(ids []string) (map[string]PersonaStatus, error) {
			return map[string]PersonaStatus{
				"clean-code":  {Running: false},
				"closer":      {Running: false},
				"reviewer":    {Running: false},
				"qa-chromium": {Running: false},
				"red-team":    {Running: false},
			}, nil
		}
		deps.PersonaActivity = func(id string) ([]store.AuditEvent, error) {
			return []store.AuditEvent{
				{Time: time.Now().Add(-5 * time.Minute), Action: "persona_run", Target: "baron-f1", Detail: "reviewed merge diff"},
			}, nil
		}
		deps.PersonaOutput = func(id string) (string, error) {
			return "Analyzing baron-f1: caching layer conforms to clean architecture.", nil
		}
		deps.PromptAgentIDs = func() ([]string, error) { return []string{"agy", "claude", "gemini", "opencode"}, nil }
		deps.PromptAgentEnsure = func(agentID string) (string, bool, error) { return "persona-" + agentID, true, nil }

		m := New(context.Background(), deps)
		m.width, m.height = 110, 32
		m.promptAgentIDs = []string{"agy", "claude", "gemini", "opencode"}
		tc.setup(&m)

		content := stripANSI(m.View().Content)
		lines := strings.Split(content, "\n")
		var cleaned []string
		for _, l := range lines {
			cleaned = append(cleaned, strings.TrimRight(l, " "))
		}
		txtPath := filepath.Join(os.TempDir(), tc.name+".txt")
		_ = os.WriteFile(txtPath, []byte(strings.Join(cleaned, "\n")), 0o600)
		targetPNG := filepath.Join(outDir, tc.name+".png")
		fcmd := exec.CommandContext(t.Context(), "freeze", txtPath, "--language", "text", "--window", "-o", targetPNG)
		if fOut, fErr := fcmd.CombinedOutput(); fErr != nil {
			t.Logf("freeze %s err: %v (%s)", tc.name, fErr, fOut)
		} else {
			t.Logf("Saved freeze screenshot: %s", targetPNG)
		}
	}
}
