package tui

import (
	"context"
	"testing"

	"github.com/baron-cli/baron/internal/persona"
)

// TestOpenPersonaEditFormPreservesTier is a regression test: the Model Tier
// select field used to offer only {standard, fast, "smart"} — "smart" was
// never a real agent.Tier value, and neither "expert" nor "guru" (both real
// tiers; red-team.md, a built-in persona, ships with tier: expert) were
// offered at all. huh.Select's Value(ptr) binding writes the first option
// back into the bound variable the instant the field is constructed
// whenever the current value doesn't match any option — so merely opening
// the edit form for an expert/guru-tier persona silently downgraded it to
// "standard" before the user ever touched the field, and saving any other
// edit (e.g. just toggling enabled) persisted that silent downgrade.
func TestOpenPersonaEditFormPreservesTier(t *testing.T) {
	for _, tier := range []string{"expert", "guru", "fast", "standard", "free"} {
		t.Run(tier, func(t *testing.T) {
			m := New(context.Background(), testDeps())
			p := persona.Persona{
				ID:     "red-team",
				Name:   "Red team",
				Prompt: "check for CVEs",
				Model:  persona.Model{Tier: tier},
			}
			m = asModel(m.openPersonaEditForm(p))
			if m.formPersonaTierResult == nil {
				t.Fatal("formPersonaTierResult is nil")
			}
			if got := *m.formPersonaTierResult; got != tier {
				t.Errorf("tier field = %q after opening edit form, want unchanged %q", got, tier)
			}
		})
	}
}
