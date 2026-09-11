package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

// TestPromptModeAccordionNavigableAndHandsOff: the redesign's explicit fix
// — a persona's work-output rows must be individually reachable with
// j/k/shift-up/down (not just the persona row itself), and 'o' on one of
// those rows hands its bead off to Board Mode.
func TestPromptModeAccordionNavigableAndHandsOff(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.PersonaActivity = func(id string) ([]store.AuditEvent, error) {
		return []store.AuditEvent{
			{Time: time.Now(), Actor: store.Actor{Type: store.ActorPersona, Name: id}, Action: "persona_run", Target: "baron-a", Detail: "reviewed"},
			{Time: time.Now().Add(-time.Hour), Actor: store.Actor{Type: store.ActorPersona, Name: id}, Action: "persona_run", Target: "baron-b", Detail: "reviewed"},
		}, nil
	}
	m := New(context.Background(), deps)
	m.width, m.height = 120, 50

	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-a", Title: "Fix login bug", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "Add tests", Status: store.BeadStatusOpen},
	}})
	m = asModel(next)

	next, cmd := m.Update(key("P"))
	m = asModel(next)
	m = unwrapBatch(t, m, cmd)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)

	// clean-code (index 0) is selected by default — 'l' opens its accordion.
	next, cmd = m.Update(key("l"))
	m = asModel(next)
	if m.promptPersonaOpenID != "clean-code" {
		t.Fatalf("promptPersonaOpenID = %q, want clean-code after 'l'", m.promptPersonaOpenID)
	}
	m = unwrapBatch(t, m, cmd)

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "baron-a") || !strings.Contains(view, "baron-b") {
		t.Errorf("Prompt Mode view = %q, want the open accordion to show both work-output rows", view)
	}

	// j from the persona row must land the cursor on its first event row,
	// individually selectable — not skip straight to the next persona.
	next, _ = m.Update(key("j"))
	m = asModel(next)
	row, ok := m.selectedPersonaRow()
	if !ok || row.eventIdx != 0 {
		t.Fatalf("selectedPersonaRow() = %+v, ok=%v, want eventIdx=0 (clean-code's first output row)", row, ok)
	}

	// 'o' on that row hands baron-a off to Board Mode.
	next, cmd = m.Update(key("o"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want screenDashboard after 'o' on a work-output row", m.screen)
	}
	if m.pendingFocusBRN != "baron-a" {
		t.Errorf("pendingFocusBRN = %q, want baron-a", m.pendingFocusBRN)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want a beads reload targeting the handed-off bead")
	}

	// Reopening Prompt Mode and closing the accordion (second 'l') must
	// land the cursor back on clean-code's own row, not leave it dangling
	// on a row index that no longer exists.
	next, _ = m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default on reopen) -> Personas
	m = asModel(next)
	next, _ = m.Update(key("l")) // re-open (openID reset by togglePromptMode)
	m = asModel(next)
	next, _ = m.Update(key("j")) // move onto an event row
	m = asModel(next)
	next, _ = m.Update(key("l")) // close from an event row
	m = asModel(next)
	if m.promptPersonaOpenID != "" {
		t.Fatalf("promptPersonaOpenID = %q, want \"\" after closing", m.promptPersonaOpenID)
	}
	p, ok := m.selectedPersona()
	if !ok || p.ID != "clean-code" {
		t.Fatalf("selectedPersona() = %+v, ok=%v, want clean-code (cursor back on its own row)", p, ok)
	}
}

func TestPromptModeTabCyclesThreePanes(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	if m.promptFocus != promptPaneRightTop {
		t.Fatalf("promptFocus = %v, want promptPaneRightTop on entry", m.promptFocus)
	}

	next, _ = m.Update(key("tab"))
	m = asModel(next)
	if m.promptFocus != promptPaneRightBottom {
		t.Fatalf("promptFocus = %v, want promptPaneRightBottom after tab", m.promptFocus)
	}

	next, _ = m.Update(key("tab"))
	m = asModel(next)
	if m.promptFocus != promptPaneLeft {
		t.Fatalf("promptFocus = %v, want promptPaneLeft after a second tab", m.promptFocus)
	}

	next, _ = m.Update(key("tab"))
	m = asModel(next)
	if m.promptFocus != promptPaneRightTop {
		t.Fatalf("promptFocus = %v, want promptPaneRightTop after a third tab (full cycle)", m.promptFocus)
	}
}

// TestPromptModeLeftPaneLSwitchesAgentTabNotAccordion: 'l' is ambiguous —
// the Personas tab's own accordion toggle and the left pane's agent-tab
// switch share the same letter. Caught by hand running the real binary:
// updatePromptKey's "act regardless of focus" verb switch (space/e/n/f/l)
// intercepted 'l' and no-op'd it (promptFocus != promptPaneRightTop) even
// while the left pane had focus, so updatePromptLeftKey never got a turn
// to treat it as a tab switch — 'l' silently did nothing at all. Fixed by
// moving the accordion toggle into updatePromptRightTopKey, reachable only
// when the right-top list genuinely has focus.
func TestPromptModeLeftPaneLSwitchesAgentTabNotAccordion(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	deps.PromptAgentIDs = func() ([]string, error) { return []string{"agy", "claude"}, nil }
	m := New(context.Background(), deps)

	next, cmd := m.Update(key("P"))
	m = asModel(next)
	m = unwrapBatch(t, m, cmd)
	next, _ = m.Update(key("tab")) // right-top -> right-bottom
	m = asModel(next)
	next, _ = m.Update(key("tab")) // right-bottom -> left
	m = asModel(next)
	if m.promptFocus != promptPaneLeft {
		t.Fatalf("promptFocus = %v, want promptPaneLeft — test setup broken", m.promptFocus)
	}
	if m.promptAgentTab != 0 {
		t.Fatalf("promptAgentTab = %d, want 0 before pressing 'l'", m.promptAgentTab)
	}

	next, _ = m.Update(key("l"))
	m = asModel(next)
	if m.promptAgentTab != 1 {
		t.Errorf("promptAgentTab = %d, want 1 — 'l' with the left pane focused must switch the agent tab, not silently no-op", m.promptAgentTab)
	}
	if m.promptPersonaOpenID != "" {
		t.Errorf("promptPersonaOpenID = %q, want \"\" — 'l' with the left pane focused must not also toggle a persona accordion", m.promptPersonaOpenID)
	}
}

func TestPromptModeShiftLeftRightSwitchesTab(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	if m.promptRightTab != promptTabBeads {
		t.Fatalf("promptRightTab = %v, want promptTabBeads on entry (Beads shows first)", m.promptRightTab)
	}
	next, _ = m.Update(key("shift+right"))
	m = asModel(next)
	if m.promptRightTab != promptTabPersonas {
		t.Fatalf("promptRightTab = %v, want promptTabPersonas after shift+right", m.promptRightTab)
	}
	next, _ = m.Update(key("shift+left"))
	m = asModel(next)
	if m.promptRightTab != promptTabBeads {
		t.Fatalf("promptRightTab = %v, want promptTabBeads after shift+left", m.promptRightTab)
	}
}

// TestPromptModeBeadsTabShowsTreeAndHandsOffToBoard: the Beads tab renders
// epics/children as a tree (splitRow/splitRowLine, the exact structure
// Board Mode's own list uses — the redesign's explicit ask), and the
// right-bottom pane reuses detailContent's exact Overview rendering.
func TestPromptModeBeadsTabShowsTreeAndHandsOffToBoard(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 120, 40

	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-tso", Title: "Big epic", Status: store.BeadStatusOpen, IssueType: "epic"},
		{BRN: "baron-tso.1", Title: "Child one", Status: store.BeadStatusOpen, IssueType: "task", Parent: "baron-tso"},
	}})
	m = asModel(next)

	next, cmd := m.Update(key("P"))
	m = asModel(next)
	m = unwrapBatch(t, m, cmd)

	// Switch to Backlog tab (1) where open beads live
	next, cmd = m.Update(key("1"))
	m = asModel(next)
	m = unwrapBatch(t, m, cmd)

	rows := m.promptBeadRows()
	if len(rows) != 2 || !rows[0].epic || rows[1].depth != 1 {
		t.Fatalf("promptBeadRows() = %+v, want [epic(depth0), child(depth1)]", rows)
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Big epic") || !strings.Contains(view, "Child one") {
		t.Errorf("Prompt Mode view = %q, want both the epic and its child rendered", view)
	}

	next, cmd = m.Update(key("j")) // select the child row
	m = asModel(next)
	m = unwrapBatch(t, m, cmd)
	if m.detail.BRN != "baron-tso.1" {
		t.Fatalf("detail.BRN = %q, want baron-tso.1", m.detail.BRN)
	}

	next, cmd = m.Update(key("o"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want screenDashboard after 'o'", m.screen)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want a beads reload targeting the handed-off bead")
	}
	if m.pendingFocusBRN != "baron-tso.1" {
		t.Errorf("pendingFocusBRN = %q, want baron-tso.1", m.pendingFocusBRN)
	}
}

// TestPromptModeBeadsTabDetailLoadsAfterLateBeadsLoad: caught by hand —
// pressing 'P' before the app's very first beadsLoadedMsg lands (m.beads
// still empty; entirely plausible right after a real launch) leaves
// togglePromptMode's own promptBeadDetailCmd with nothing to select, so
// the right-bottom pane is stuck on "select a bead to read it" — and nothing
// retried it once beads DID load a moment later, since only screenDashboard
// was wired to react to a beadsLoadedMsg arriving while Prompt Mode is
// already open.
func TestPromptModeBeadsTabDetailLoadsAfterLateBeadsLoad(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P")) // beads haven't loaded yet — m.beads is empty
	m = asModel(next)
	if m.detail.BRN != "" {
		t.Fatalf("detail.BRN = %q, want empty — test setup broken (beads shouldn't be loaded yet)", m.detail.BRN)
	}

	next, _ = m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-a", Title: "Fix login bug", Status: store.BeadStatusWorking},
	}}) // the load that was already in flight lands after entering Prompt Mode (Active tab)
	m = asModel(next)
	if m.detail.BRN != "baron-a" {
		t.Fatalf("detail.BRN = %q, want baron-a — the Beads-tab detail pane must not stay stuck empty once beads load", m.detail.BRN)
	}
}

// TestPromptModeBeadsTabFollowsBoardTabs verifies Prompt Mode filters beads
// using the exact same categories as Board Mode.
func TestPromptModeBeadsTabFollowsBoardTabs(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 120, 40

	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-a", Title: "Already closed", Status: store.BeadStatusClosed},
		{BRN: "baron-b", Title: "Still open", Status: store.BeadStatusOpen},
		{BRN: "baron-c", Title: "Already merged", Status: store.BeadStatusMerged},
		{BRN: "baron-d", Title: "Blocked, still live", Status: store.BeadStatusBlocked},
	}})
	m = asModel(next)
	next, _ = m.Update(key("P"))
	m = asModel(next)

	// Backlog (1) has open and blocked
	next, _ = m.Update(key("1"))
	m = asModel(next)
	rows := m.promptBeadRows()
	if len(rows) != 2 {
		t.Fatalf("promptBeadRows() in Backlog = %+v, want 2 rows (open, blocked)", rows)
	}

	// Done (4) has closed and merged
	next, _ = m.Update(key("4"))
	m = asModel(next)
	rows = m.promptBeadRows()
	if len(rows) != 2 {
		t.Fatalf("promptBeadRows() in Done = %+v, want 2 rows (closed, merged)", rows)
	}
}

func TestPromptModeBeadsTabNewBeadForm(t *testing.T) {
	var gotTitle string
	deps := testDeps()
	deps.CreateBead = func(title, description, accept, priority, issueType, parent, tier string) (string, error) {
		gotTitle = title
		return "ran", nil
	}
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	// Beads is the default tab on entry — no tab switch needed.
	next, _ = m.Update(key("n"))
	m = asModel(next)
	if m.screen != screenForm || m.formKind != formKindPromptNewBead {
		t.Fatalf("screen=%v formKind=%v, want screenForm/formKindPromptNewBead after 'n' on the Beads tab", m.screen, m.formKind)
	}
	if m.pendingNewBead {
		t.Error("pendingNewBead = true, want false — the prompt-owned new-bead form must not arm Board Mode's own jump")
	}

	*m.formTitleResult = "New bead from Prompt Mode"
	*m.formTierResult = "fast"
	next, cmd := m.submitForm()
	m = asModel(next)
	if m.screen != screenCrew {
		t.Fatalf("screen = %v, want back to screenCrew after submitting", m.screen)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the create-bead command")
	}
	cmd()
	if gotTitle != "New bead from Prompt Mode" {
		t.Errorf("CreateBead title=%q, want %q", gotTitle, "New bead from Prompt Mode")
	}
}

func TestPromptModeBeadsTabExpandsComments(t *testing.T) {
	deps := testDeps()
	m := New(context.Background(), deps)
	m.width, m.height = 120, 80

	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "X", Status: store.BeadStatusWorking}}})
	m = asModel(next)
	next, cmd := m.Update(key("P"))
	m = asModel(next)
	m = unwrapBatch(t, m, cmd)

	comments := make([]store.Comment, 4)
	for i := range comments {
		comments[i] = store.Comment{Author: "you", Text: fmt.Sprintf("comment %d", i)}
	}
	m.comments = comments
	if m.commentsExpanded {
		t.Fatal("commentsExpanded = true, want false initially")
	}

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "press u to expand") {
		t.Errorf("Prompt Mode view = %q, want the collapsed-comments hint to say \"press u to expand\"", view)
	}

	next, _ = m.Update(key("u"))
	m = asModel(next)
	if !m.commentsExpanded {
		t.Fatal("commentsExpanded = false, want true after 'u' on the Beads tab")
	}
	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "press u to collapse") {
		t.Errorf("Prompt Mode view = %q, want the expanded-comments hint to say \"press u to collapse\"", view)
	}
}

func TestPromptModeHeaderShowsPersonaSummary(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	m := New(context.Background(), deps)
	m.width, m.height = 100, 30

	next, _ := m.Update(key("P"))
	m = asModel(next)
	header := stripANSI(m.viewHeaderV2())
	if !strings.Contains(header, "personas enabled") {
		t.Errorf("viewHeaderV2() = %q, want a persona-count summary while Prompt Mode is open", header)
	}
	if strings.Contains(header, "changed") || strings.Contains(header, "⎇") {
		t.Errorf("viewHeaderV2() = %q, want no stale bead/git stats while Prompt Mode is open", header)
	}
}

// manyPersonas returns n distinct personas for scroll-window tests — real
// projects can accumulate more personas than fit in the Personas tab's row
// budget.
func manyPersonas(n int) []persona.Persona {
	var out []persona.Persona
	for i := range n {
		out = append(out, persona.Persona{
			ID: fmt.Sprintf("p%02d", i), Name: fmt.Sprintf("Persona %02d", i),
			Trigger: persona.Trigger{},
		})
	}
	return out
}

// TestPromptModePersonaListScrollsToKeepCursorVisible: with more personas
// than fit in the Personas tab's row budget, moving the cursor past the
// visible window must scroll it into view rather than let it render off
// the bottom, silently clipped.
func TestPromptModePersonaListScrollsToKeepCursorVisible(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return manyPersonas(20), nil }
	m := New(context.Background(), deps)
	m.width, m.height = 120, 20 // a short terminal keeps the row budget small enough to force scrolling

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	visible := m.promptRightTopListRows()
	target := visible + 2 // definitely past the first window
	for range target {
		next, _ = m.Update(key("j"))
		m = asModel(next)
	}
	if m.promptPersonaCursor != target {
		t.Fatalf("promptPersonaCursor = %d, want %d", m.promptPersonaCursor, target)
	}
	view := stripANSI(m.View().Content)
	want := fmt.Sprintf("Persona %02d", target)
	if !strings.Contains(view, want) {
		t.Errorf("Prompt Mode view = %q, want the scrolled-to persona %q actually rendered, not clipped off-screen", view, want)
	}
}

// TestPromptModeShiftUpDownMovesListRegardlessOfFocus: shift+up/down is an
// always-available alias for moving the active tab's list cursor, usable
// even while the left pane (or right-bottom) has keyboard focus — the
// redesign's explicit ask ("shift asagi/yukari da personalar veya beadler
// arasinda gezmeyi yapsin").
func TestPromptModeShiftUpDownMovesListRegardlessOfFocus(t *testing.T) {
	deps := testDeps()
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	m := New(context.Background(), deps)

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	next, _ = m.Update(key("tab")) // move focus off right-top, onto right-bottom
	m = asModel(next)
	if m.promptFocus == promptPaneRightTop {
		t.Fatal("promptFocus still promptPaneRightTop after tab — test setup broken")
	}

	next, _ = m.Update(key("shift+down"))
	m = asModel(next)
	if m.promptPersonaCursor != 1 {
		t.Fatalf("promptPersonaCursor = %d, want 1 after shift+down, even without right-top focus", m.promptPersonaCursor)
	}
}

// TestPromptModeHighlightFollowsCursorRegardlessOfFocus: caught by hand
// running the real binary — shift+up/down visibly moved m.promptBeadCursor
// (confirmed above), but the CardSelected background highlight only ever
// followed it when promptFocus was actually promptPaneRightTop, because
// viewPromptPersonaList/viewPromptBeadList gated their row-highlight check
// on both "is this the selected row" AND "does this pane have keyboard
// focus" — two different signals conflated into one condition. Tabbing
// into the list with plain arrows worked (focus was already right-top by
// then), which is what made this easy to miss from inside the app. Needs
// NoColor:false (testDeps() defaults it true, a deliberate no-op for
// every other test) since this is specifically a rendering assertion.
func TestPromptModeHighlightFollowsCursorRegardlessOfFocus(t *testing.T) {
	deps := testDeps()
	deps.NoColor = false
	deps.Personas = func() ([]persona.Persona, error) { return testPersonas(), nil }
	m := New(context.Background(), deps)
	m.width, m.height = 120, 30

	next, _ := m.Update(key("P"))
	m = asModel(next)
	next, _ = m.Update(key("shift+right")) // Beads (default) -> Personas
	m = asModel(next)
	next, _ = m.Update(key("tab")) // right-top -> right-bottom: focus leaves the list
	m = asModel(next)
	if m.promptFocus == promptPaneRightTop {
		t.Fatal("promptFocus still promptPaneRightTop after tab — test setup broken")
	}

	next, _ = m.Update(key("shift+down"))
	m = asModel(next)
	if m.promptPersonaCursor != 1 {
		t.Fatalf("promptPersonaCursor = %d, want 1 — test setup broken", m.promptPersonaCursor)
	}

	rawLine := func(view, name string) string {
		for ln := range strings.SplitSeq(view, "\n") {
			if strings.Contains(stripANSI(ln), name) {
				return ln
			}
		}
		return ""
	}
	view := m.View().Content
	selected := rawLine(view, "QA (Chromium)")         // index 1, the new cursor position
	unselected := rawLine(view, "Clean-code reviewer") // index 0, no longer selected
	if selected == "" || unselected == "" {
		t.Fatalf("could not find both persona rows in view = %q", view)
	}
	// Every row in the box carries some ANSI (the border color), so check
	// for CardSelected's own reverse-video start sequence specifically,
	// not "any ANSI at all".
	seq := m.styles.reselectSeq
	if seq == "" {
		t.Fatal("styles.reselectSeq is empty — test setup broken (NoColor accidentally still true?)")
	}
	if !strings.Contains(selected, seq) {
		t.Errorf("selected row %q missing CardSelected's highlight sequence %q — highlight isn't following the cursor without right-top focus", selected, seq)
	}
	if strings.Contains(unselected, seq) {
		t.Errorf("unselected row %q still carries CardSelected's highlight sequence %q — stale highlight left behind on the old cursor position", unselected, seq)
	}
}
