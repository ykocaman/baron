package persona

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStateMissingFileIsNotError(t *testing.T) {
	s, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadState() error = %v, want nil for a project with no state.toml yet", err)
	}
	if len(s.Personas) != 0 {
		t.Errorf("Personas = %v, want empty", s.Personas)
	}
}

func TestSetEnabledRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := SetEnabled(dir, "closer", true); err != nil {
		t.Fatalf("SetEnabled() error: %v", err)
	}
	s, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if !s.Personas["closer"].Enabled {
		t.Errorf("Personas[closer].Enabled = false, want true")
	}
}

func TestSetEnabledOverwritesPreviousValue(t *testing.T) {
	dir := t.TempDir()
	if err := SetEnabled(dir, "qa-chromium", true); err != nil {
		t.Fatalf("SetEnabled(true) error: %v", err)
	}
	if err := SetEnabled(dir, "qa-chromium", false); err != nil {
		t.Fatalf("SetEnabled(false) error: %v", err)
	}
	s, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if s.Personas["qa-chromium"].Enabled {
		t.Errorf("Personas[qa-chromium].Enabled = true, want false (the later write should win)")
	}
}

func TestSetEnabledPreservesOtherPersonas(t *testing.T) {
	dir := t.TempDir()
	if err := SetEnabled(dir, "qa-chromium", true); err != nil {
		t.Fatalf("SetEnabled(qa-chromium) error: %v", err)
	}
	if err := SetEnabled(dir, "closer", true); err != nil {
		t.Fatalf("SetEnabled(closer) error: %v", err)
	}
	s, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if !s.Personas["qa-chromium"].Enabled || !s.Personas["closer"].Enabled {
		t.Errorf("Personas = %+v, want both entries enabled", s.Personas)
	}
}

// TestStateFileIsUnderDotBaronPersonas locks in the exact path — .baron/
// is already blanket-gitignored, and this must land inside it, not
// alongside the repo's own committed personas/ directory.
func TestStateFileIsUnderDotBaronPersonas(t *testing.T) {
	dir := t.TempDir()
	if err := SetEnabled(dir, "red-team", true); err != nil {
		t.Fatalf("SetEnabled() error: %v", err)
	}
	want := filepath.Join(dir, ".baron", "personas", "state.toml")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected state file at %s, stat error: %v", want, err)
	}
}
