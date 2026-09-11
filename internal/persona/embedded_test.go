package persona

import "testing"

func TestEmbeddedPersonasParsesAllFiles(t *testing.T) {
	got, err := embeddedPersonas()
	if err != nil {
		t.Fatalf("embeddedPersonas() error: %v", err)
	}
	want := []string{"clean-code", "closer", "reviewer", "qa-chromium", "red-team"}
	if len(got) != len(want) {
		t.Fatalf("embeddedPersonas() = %d personas, want %d: %+v", len(got), len(want), got)
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p.ID] = true
		if err := p.Validate(); err != nil {
			t.Errorf("embedded persona %s: Validate() = %v", p.ID, err)
		}
		if p.Enabled {
			t.Errorf("embedded persona %s: Enabled = true, want false (opt-in only)", p.ID)
		}
	}
	for _, id := range want {
		if !seen[id] {
			t.Errorf("embeddedPersonas() missing %q", id)
		}
	}
}
