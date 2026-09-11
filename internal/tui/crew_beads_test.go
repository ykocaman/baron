package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

// TestPromptModeAccordionShowsPersonaOutput: the accordion (opened with
// 'l') previously showed only the audit-trail rows (Deps.PersonaActivity —
// which beads, and why) with no way to see what the persona actually said
// or did. Personas run in a tmux window ("persona-<id>", the same one
// launchPersona/FireNow spawn) exactly like Prompt Mode's own left-pane
// agent chats, so that raw output is fetchable the same way — restored via
// Deps.PersonaOutput, rendered as a capped preview under the persona's own
// row.
func TestPromptModeAccordionShowsPersonaOutput(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.PersonaActivity = func(id string) ([]store.AuditEvent, error) { return nil, nil }
	deps.PersonaOutput = func(id string) (string, error) {
		if id != "clean-code" {
			t.Fatalf("PersonaOutput(id=%q), want clean-code", id)
		}
		return "line one\nline two\nreviewed baron-a, looks good", nil
	}
	m := New(context.Background(), deps)
	m.width, m.height = 120, 30

	next, cmd := m.Update(key("P"))
	m = asModel(next)
	m = unwrapBatch(t, m, cmd)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)

	next, cmd = m.Update(key("l")) // clean-code selected by default
	m = asModel(next)
	if m.promptPersonaOpenID != "clean-code" {
		t.Fatalf("promptPersonaOpenID = %q, want clean-code after 'l'", m.promptPersonaOpenID)
	}
	m = unwrapBatch(t, m, cmd)

	if m.promptPersonaOutput["clean-code"] == "" {
		t.Fatal("promptPersonaOutput[clean-code] is empty, want the fetched output cached")
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "reviewed baron-a, looks good") {
		t.Errorf("Prompt Mode view = %q, want the persona's raw output rendered in the detail pane", view)
	}
}

func TestPromptModeBeadsCategoryTabsAndFilter(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 140, 50

	// Load beads with different statuses: Backlog (open), Active (working), Done (closed)
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-open", Title: "Open bead", Status: store.BeadStatusOpen},
		{BRN: "baron-work", Title: "Working bead", Status: store.BeadStatusWorking},
		{BRN: "baron-done", Title: "Done bead", Status: store.BeadStatusClosed},
	}})
	m = asModel(next)

	// Enter Prompt Mode (lands on Beads tab)
	next, _ = m.Update(key("P"))
	m = asModel(next)
	if m.screen != screenCrew || m.promptRightTab != promptTabBeads {
		t.Fatalf("screen = %v, promptRightTab = %v, want screenCrew / promptTabBeads", m.screen, m.promptRightTab)
	}

	// Initially lands on Active tab (promptBeadTab = 1), matching Board Mode
	if m.promptBeadTab != 1 {
		t.Fatalf("promptBeadTab = %v, want 1 (Active)", m.promptBeadTab)
	}
	if len(m.promptBeadRows()) != 1 {
		t.Fatalf("promptBeadRows() in Active = %d, want 1 (baron-work)", len(m.promptBeadRows()))
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Backlog 1") || !strings.Contains(view, "Active 1") || !strings.Contains(view, "Done 1") {
		t.Errorf("View() = %q, want tab labels with counts (matching Board Mode)", view)
	}

	// Press '1' -> filter to Backlog (open)
	next, _ = m.Update(key("1"))
	m = asModel(next)
	if m.promptBeadTab != 0 {
		t.Fatalf("promptBeadTab = %v, want 0 (Backlog)", m.promptBeadTab)
	}
	rows := m.promptBeadRows()
	if len(rows) != 1 || string(rows[0].bead.BRN) != "baron-open" {
		t.Errorf("promptBeadRows() in Backlog = %+v, want only baron-open", rows)
	}

	// Press '4' -> filter to Done (closed)
	next, _ = m.Update(key("4"))
	m = asModel(next)
	if m.promptBeadTab != 3 {
		t.Fatalf("promptBeadTab = %v, want 3 (Done)", m.promptBeadTab)
	}
	rows = m.promptBeadRows()
	if len(rows) != 1 || string(rows[0].bead.BRN) != "baron-done" {
		t.Errorf("promptBeadRows() in Done = %+v, want only baron-done", rows)
	}

	// Press '3' -> Needs You (empty)
	next, _ = m.Update(key("3"))
	m = asModel(next)
	if m.promptBeadTab != 2 {
		t.Fatalf("promptBeadTab = %v, want 2 (Needs You)", m.promptBeadTab)
	}
	rows = m.promptBeadRows()
	if len(rows) != 0 {
		t.Errorf("promptBeadRows() in Needs You = %+v, want empty", rows)
	}
	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "No beads in Needs You.") {
		t.Errorf("View() = %q, want 'No beads in Needs You.'", view)
	}
}

func TestPromptModePersonaDetailRichContent(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) {
		return []persona.Persona{
			{
				ID:          "sec-audit",
				Name:        "Security Auditor",
				Description: "Performs static security analysis and audits",
				Prompt:      "Check for CVEs and insecure input handling",
				Model:       persona.Model{Agent: "claude", Tier: "smart"},
				Trigger: persona.Trigger{
					Schedule:   "0 12 * * *",
					On:         []persona.TransitionRule{{From: "*", To: "merged"}},
					IssueTypes: []string{"bug", "feature"},
				},
				Authority: persona.Authority{
					BDWrite: true,
					Actions: []string{"comment", "reopen"},
				},
				Skills:  []string{"sec-checker", "https://example.com/plugin.zip"},
				Enabled: true,
				Source:  persona.SourceUser,
			},
		}, nil
	}
	m := New(context.Background(), deps)
	m.width, m.height = 140, 60

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Switch to Personas tab
	m = asModel(next)

	view := stripANSI(m.viewPromptPersonaDetail(80))
	for _, want := range []string{
		"Security Auditor",
		"sec-audit",
		"ENABLED",
		"src: user",
		"agent: claude (smart)",
		"Performs static security analysis and audits",
		"Trigger:",
		"Schedule: 0 12 * * *",
		"Events:   * → merged",
		"Skills:",
		"sec-checker",
		"Authority:",
		"BD Write: allowed (read/write)",
		"Actions:  comment, reopen",
		"Prompt / Instructions:",
		"Check for CVEs and insecure input handling",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("Persona detail view missing %q", want)
		}
	}
}

func TestPromptModePersonaDetailRunOutput(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) {
		return []persona.Persona{
			{
				ID:   "clean-code",
				Name: "Clean Code",
			},
		}, nil
	}
	deps.PersonaActivity = func(id string) ([]store.AuditEvent, error) {
		return []store.AuditEvent{
			{Time: time.Now(), Actor: store.Actor{Type: store.ActorPersona, Name: id}, Action: "persona_run", Target: "baron-123", Detail: "found refactor opportunity"},
		}, nil
	}
	deps.PersonaOutput = func(id string) (string, error) {
		return "Scanning repo files...\nRefactoring suggestions generated for baron-123.", nil
	}
	m := New(context.Background(), deps)
	m.width, m.height = 140, 60

	next, cmd := m.Update(key("P"))
	m = asModel(next)
	m = unwrapBatch(t, m, cmd)
	next, _ = m.Update(key("shift+right")) // Personas tab
	m = asModel(next)

	// Open accordion with 'l'
	next, cmd = m.Update(key("l"))
	m = asModel(next)
	m = unwrapBatch(t, m, cmd)

	// Move cursor down to the first work-output row
	next, _ = m.Update(key("j"))
	m = asModel(next)

	view := stripANSI(m.View().Content)
	for _, want := range []string{
		"Clean Code ▸ Run Detail",
		"Action: persona_run",
		"Target: baron-123",
		"Detail: found refactor opportunity",
		"Output / Execution Log:",
		"Scanning repo files...",
		"Refactoring suggestions generated for baron-123.",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("Run detail view missing %q\nView:\n%s", want, view)
		}
	}
}

func TestPromptModeBeadsSubTabNavigation(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 140, 60

	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-open", Title: "Open bead", Status: store.BeadStatusOpen},
		{BRN: "baron-work", Title: "Working bead", Status: store.BeadStatusWorking},
		{BRN: "baron-valid", Title: "Validating bead", Status: store.BeadStatusValidating},
	}})
	m = asModel(next)

	next, _ = m.Update(key("P"))
	m = asModel(next)

	// Switch to Active category (tab 2: 1 Backlog, 2 Active)
	next, _ = m.Update(key("2"))
	m = asModel(next)

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Working") || !strings.Contains(view, "Validating") || !strings.Contains(view, "Retry") {
		t.Errorf("Active tab view missing sub-tabs (Working, Validating, Retry)\nView:\n%s", view)
	}

	// Navigate sub-tab with 'l' (when right-top is focused)
	next, _ = m.Update(key("l"))
	m = asModel(next)
	if m.promptBeadSubTab != 0 {
		t.Errorf("promptBeadSubTab = %d, want 0 after pressing 'l'", m.promptBeadSubTab)
	}
}

func TestPromptModeSubTabSpilloverNavigation(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)

	// Set to tab 0 (Backlog), sub-tab 2 (Blocked, last bucket in Backlog)
	m.promptBeadTab = 0
	m.promptBeadSubTab = len(boardColumns[0].statuses) - 1 // 2 (Blocked)

	// Pressing 'l' (right) spills over to tab 1 (Active), sub-tab 0 (Working)
	next, _ = m.Update(key("l"))
	m = asModel(next)
	if m.promptBeadTab != 1 || m.promptBeadSubTab != 0 {
		t.Errorf("promptBeadTab=%d, promptBeadSubTab=%d, want 1/0 after spillover from Backlog", m.promptBeadTab, m.promptBeadSubTab)
	}

	// Pressing 'h' (left) spills back to tab 0 (Backlog), sub-tab 2 (Blocked)
	next, _ = m.Update(key("h"))
	m = asModel(next)
	if m.promptBeadTab != 0 || m.promptBeadSubTab != len(boardColumns[0].statuses)-1 {
		t.Errorf("promptBeadTab=%d, promptBeadSubTab=%d, want 0/%d after spillback to Backlog", m.promptBeadTab, m.promptBeadSubTab, len(boardColumns[0].statuses)-1)
	}
}

func TestPromptModeMouseClicksAndScroll(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 140, 50

	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-open-1", Title: "Open bead 1", Status: store.BeadStatusOpen},
		{BRN: "baron-open-2", Title: "Open bead 2", Status: store.BeadStatusOpen},
		{BRN: "baron-work-1", Title: "Work bead 1", Status: store.BeadStatusWorking},
	}})
	m = asModel(next)

	next, _ = m.Update(key("P"))
	m = asModel(next)

	leftW, _ := promptPaneWidths(m.width)

	// Click on Personas tab header (X on the right side of right box top)
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 15, Y: 2})
	m = asModel(next)
	if m.promptRightTab != promptTabPersonas {
		t.Errorf("promptRightTab = %v, want promptTabPersonas after clicking Personas tab", m.promptRightTab)
	}

	// Click on Beads tab header
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 4, Y: 2})
	m = asModel(next)
	if m.promptRightTab != promptTabBeads {
		t.Errorf("promptRightTab = %v, want promptTabBeads after clicking Beads tab", m.promptRightTab)
	}

	// Click on Backlog category tab (1st category) at Y=3 -> promptBeadTab = 0
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 5, Y: 3})
	m = asModel(next)
	if m.promptBeadTab != 0 {
		t.Errorf("promptBeadTab = %v, want 0 (Backlog) after click at Y=3", m.promptBeadTab)
	}

	// Click on Active category tab (2nd category) at Y=3 -> promptBeadTab = 1
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 20, Y: 3})
	m = asModel(next)
	if m.promptBeadTab != 1 {
		t.Errorf("promptBeadTab = %v, want 1 (Active) after click at Y=3", m.promptBeadTab)
	}

	// Click on Needs You category tab (3rd category) at Y=3 -> promptBeadTab = 2
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 32, Y: 3})
	m = asModel(next)
	if m.promptBeadTab != 2 {
		t.Errorf("promptBeadTab = %v, want 2 (Needs You) after click at Y=3", m.promptBeadTab)
	}

	// Click on Done category tab (4th category) at Y=3 -> promptBeadTab = 3
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 48, Y: 3})
	m = asModel(next)
	if m.promptBeadTab != 3 {
		t.Errorf("promptBeadTab = %v, want 3 (Done) after click at Y=3", m.promptBeadTab)
	}

	// Click back to Backlog
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 5, Y: 3})
	m = asModel(next)

	// Click on Sub-tab bar (Y=4) -> Open sub-tab
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 4, Y: 4})
	m = asModel(next)
	if m.promptBeadSubTab != 0 {
		t.Errorf("promptBeadSubTab = %v, want 0 (Open) after click at Y=4", m.promptBeadSubTab)
	}

	// Click on 2nd bead in the list (Y=6 since list rows start at Y=5)
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 10, Y: 6})
	m = asModel(next)
	if m.promptBeadCursor != 1 {
		t.Errorf("promptBeadCursor = %v, want 1 after clicking 2nd row at Y=6", m.promptBeadCursor)
	}

	// Switch to Personas tab
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 15, Y: 2})
	m = asModel(next)
	if m.promptRightTab != promptTabPersonas {
		t.Errorf("promptRightTab = %v, want promptTabPersonas", m.promptRightTab)
	}

	// Click on 1st persona row (Y=4)
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: leftW + 10, Y: 4})
	m = asModel(next)
	if m.promptPersonaCursor != 0 {
		t.Errorf("promptPersonaCursor = %v, want 0 after clicking 1st persona row at Y=4", m.promptPersonaCursor)
	}

	// Mouse wheel down on right-top pane
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: leftW + 10, Y: 8})
	m = asModel(next)
	if m.promptFocus != promptPaneRightTop {
		t.Errorf("promptFocus = %v, want promptPaneRightTop after wheel in top pane", m.promptFocus)
	}

	// Mouse wheel on right-bottom detail pane
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: leftW + 10, Y: 35})
	m = asModel(next)
	if m.promptFocus != promptPaneRightBottom {
		t.Errorf("promptFocus = %v, want promptPaneRightBottom after wheel in bottom pane", m.promptFocus)
	}
}
