package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/store"
)

// TestNewBeadFormPrefillsTypeAndEpicParent: pressing n with an epic selected
// opens the popup with type=task and the epic pre-filled as parent, so a
// child bead is one submit away.
func TestNewBeadFormPrefillsTypeAndEpicParent(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-epic1", Title: "Epic", Status: store.BeadStatusOpen, IssueType: "epic"},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, _ = m.Update(key("n"))
	m = asModel(next)
	if m.screen != screenForm || m.formKind != formKindNewBead {
		t.Fatalf("screen=%v formKind=%v, want form/new-bead", m.screen, m.formKind)
	}
	if got := *m.formTypeResult; got != "task" {
		t.Errorf("type = %q, want prefilled \"task\"", got)
	}
	if got := *m.formParentResult; got != "baron-epic1" {
		t.Errorf("parent = %q, want prefilled epic BRN \"baron-epic1\"", got)
	}
	if !strings.Contains(m.View().Content, "New Bead") {
		t.Errorf("View() = %q, want the popup title", m.View().Content)
	}
}

// TestNewBeadSubmitPassesTypeAndParent: submitting the new-bead form builds a
// work create command carrying --type and --parent (plus acceptance),
// walking every field with tab the way huh itself navigates them (see
// updateFormKey/pumpHuhForm — huh drives the fields, BARON just forwards
// keys and reads the bound result strings back off).
func TestNewBeadSubmitPassesTypeAndParent(t *testing.T) {
	var gotTitle, gotAccept, gotType, gotParent, gotTier string
	deps := testDeps()
	deps.CreateBead = func(title, description, accept, priority, issueType, parent, tier string) (string, error) {
		gotTitle, gotAccept, gotType, gotParent, gotTier = title, accept, issueType, parent, tier
		return "created baron-new1: " + title, nil
	}
	m := New(context.Background(), deps)
	beads := []store.Bead{
		{BRN: "baron-epic1", Title: "Epic", Status: store.BeadStatusOpen, IssueType: "epic"},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, _ = m.Update(key("n"))
	m = asModel(next)

	for _, r := range "child of epic" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	for range 5 { // title -> type -> tier -> parent -> description -> acceptance
		next, _ = m.Update(key("tab"))
		m = asModel(next)
	}
	if m.beadForm == nil {
		t.Fatal("form closed before reaching the last field")
	}
	for _, r := range "the work" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}

	next, cmd := m.Update(key("enter")) // submit from the last field
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want back on the dashboard after submit", m.screen)
	}
	if cmd == nil {
		t.Fatal("expected a command after submit")
	}
	if _, ok := cmd().(commandRanMsg); !ok {
		t.Fatalf("msg = %T, want commandRanMsg", cmd())
	}
	if gotTitle != "child of epic" || gotType != "task" || gotParent != "baron-epic1" || gotAccept != "the work" || gotTier != "fast" {
		t.Errorf("CreateBead(title=%q, type=%q, parent=%q, accept=%q, tier=%q), want (\"child of epic\", \"task\", \"baron-epic1\", \"the work\", \"fast\")",
			gotTitle, gotType, gotParent, gotAccept, gotTier)
	}
}

// TestNewBeadFormDefaultsTierFast: the tier field opens on "fast" — the
// cheapest/quickest tier — rather than whichever tier the option list
// happens to sort first, so leaving it untouched still submits a sane,
// mandatory, low-cost-by-default tier.
func TestNewBeadFormDefaultsTierFast(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)
	next, _ = m.Update(key("n"))
	m = asModel(next)
	if got := *m.formTierResult; got != "fast" {
		t.Errorf("tier = %q, want default \"fast\"", got)
	}
}

// TestFormTierFieldIsSelectable: the new-bead tier field is a huh select —
// j/k move the highlighted option and the bound result follows, the same
// way the type field already works.
func TestFormTierFieldIsSelectable(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	next, _ = m.Update(key("n"))
	m = asModel(next)
	for range 2 { // title -> type -> tier
		next, _ = m.Update(key("tab"))
		m = asModel(next)
	}
	if got := *m.formTierResult; got != "fast" {
		t.Errorf("tier = %q, want prefilled fast", got)
	}
	for _, want := range []string{"free", "fast", "standard", "expert", "guru"} {
		if !strings.Contains(m.View().Content, want) {
			t.Errorf("View() = %q, want the %q option listed", m.View().Content, want)
		}
	}

	next, _ = m.Update(key("j"))
	m = asModel(next)
	if got := *m.formTierResult; got != "standard" {
		t.Errorf("tier = %q after j, want standard", got)
	}
	next, _ = m.Update(key("k"))
	m = asModel(next)
	if got := *m.formTierResult; got != "fast" {
		t.Errorf("tier = %q after k, want fast", got)
	}
}

// TestFormTypeFieldIsSelectable: the new-bead type field is a huh select —
// j/k move the highlighted option and the bound result follows the cursor.
func TestFormTypeFieldIsSelectable(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	next, _ = m.Update(key("n"))
	m = asModel(next)
	next, _ = m.Update(key("tab")) // title -> type
	m = asModel(next)
	if got := *m.formTypeResult; got != "task" {
		t.Errorf("type = %q, want prefilled task", got)
	}
	for _, want := range []string{"task", "feature", "bug", "epic", "chore", "decision"} {
		if !strings.Contains(m.View().Content, want) {
			t.Errorf("View() = %q, want the %q option listed", m.View().Content, want)
		}
	}

	next, _ = m.Update(key("j"))
	m = asModel(next)
	next, _ = m.Update(key("j"))
	m = asModel(next)
	if got := *m.formTypeResult; got != "bug" {
		t.Errorf("type = %q after j j, want bug", got)
	}
	next, _ = m.Update(key("k"))
	m = asModel(next)
	if got := *m.formTypeResult; got != "feature" {
		t.Errorf("type = %q after k, want feature", got)
	}
}

// TestFormDescriptionAcceptsNewlines: the description is a huh Text field —
// alt+enter inserts a newline (huh's own NewLine binding); plain enter
// advances to the next field instead, huh's normal convention.
func TestFormDescriptionAcceptsNewlines(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)
	next, _ = m.Update(key("n"))
	m = asModel(next)
	for range 4 { // title -> type -> tier -> parent -> description
		next, _ = m.Update(key("tab"))
		m = asModel(next)
	}

	next, _ = m.Update(key("alt+enter"))
	m = asModel(next)
	if m.screen != screenForm {
		t.Fatalf("screen = %v, want still on the form (alt+enter inserts a newline)", m.screen)
	}
	if got := *m.formDescResult; got != "\n" {
		t.Errorf("description = %q, want a newline inserted", got)
	}
}

// TestNewBeadFormParentOptions: the parent-epic select lists "(none)" first,
// then every active epic (closed/cancelled excluded) newest-updated first,
// each displayed as "BRN · first words of the description".
func TestNewBeadFormParentOptions(t *testing.T) {
	now := time.Now()
	beads := []store.Bead{
		{BRN: "baron-e1", Title: "Epic one", Status: store.BeadStatusOpen, IssueType: "epic", Description: "first second third fourth fifth sixth seventh eighth", UpdatedAt: now.Add(-2 * time.Hour)},
		{BRN: "baron-e2", Title: "Epic two", Status: store.BeadStatusWorking, IssueType: "epic", Description: "alpha beta gamma", UpdatedAt: now.Add(-time.Hour)},
		{BRN: "baron-ec", Title: "Epic closed", Status: store.BeadStatusClosed, IssueType: "epic", UpdatedAt: now},
		{BRN: "baron-ex", Title: "Epic cancelled", Status: store.BeadStatusCancelled, IssueType: "epic"},
	}
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	opts := m.newBeadFormParentOptions()
	if len(opts) != 3 { // "(none)" + 2 active epics (closed/cancelled excluded)
		t.Fatalf("parent options = %d, want 3 ((none) + 2 active epics)", len(opts))
	}
	if opts[0].Value != "" {
		t.Errorf("option[0] = %+v, want the empty \"(none)\" value first", opts[0])
	}
	if opts[1].Value != "baron-e2" || opts[2].Value != "baron-e1" {
		t.Fatalf("parent order = [%v %v], want newest-updated first (baron-e2, baron-e1)", opts[1].Value, opts[2].Value)
	}
	if opts[1].Key != "baron-e2 · alpha beta gamma" {
		t.Errorf("option[1].Key = %q, want BRN + first words of the description", opts[1].Key)
	}
	if !strings.Contains(opts[2].Key, "first second third fourth fifth sixth seventh") {
		t.Errorf("option[2].Key = %q, want description clipped to 7 words", opts[2].Key)
	}
}
