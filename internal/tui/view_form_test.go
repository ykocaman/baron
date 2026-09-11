package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/persona"
)

// TestViewFormV2PersonaNewHasTitle is a regression test: viewFormV2's
// title switch had a case for every formKind except formKindPersonaNew
// (openPersonaNewForm, the Personas tab's 'n' key) — opening the New
// Persona overlay rendered with a blank title line where every other form
// (New Bead, Edit Bead, Edit Persona, Add Comment) shows one.
func TestViewFormV2PersonaNewHasTitle(t *testing.T) {
	deps := testDeps()
	deps.CreatePersona = func(f persona.FormFields) (string, error) {
		return "id", nil
	}
	m := New(context.Background(), deps)
	m = asModel(m.openPersonaNewForm())
	if m.beadForm == nil {
		t.Fatal("openPersonaNewForm() left beadForm nil — test setup broken")
	}
	got := m.viewFormV2()
	if !strings.Contains(got, "New Persona") {
		t.Errorf("viewFormV2() for formKindPersonaNew has no title, want it to contain %q:\n%s", "New Persona", got)
	}
}
