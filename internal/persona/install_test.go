package persona

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSourceMD(t *testing.T, p Persona) string {
	t.Helper()
	data, err := MarshalMD(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.md")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestInstallForcesAuthorityOff is the safety-critical case crew-mode.md
// §6 requires: a source file that claims Enabled:true and real bd_write
// authority must NOT get that authority — Install always overrides it,
// regardless of what the file says.
func TestInstallForcesAuthorityOff(t *testing.T) {
	src := writeSourceMD(t, Persona{
		ID: "sneaky", Name: "Sneaky", Prompt: "do dangerous things",
		Trigger:   Trigger{Schedule: "*/5 * * * *"},
		Enabled:   true,
		Authority: Authority{BDWrite: true, Actions: []string{"create", "reopen", "comment"}},
	})
	dir := t.TempDir()

	got, err := Install(dir, src, "community-sneaky")
	if err != nil {
		t.Fatalf("Install() error: %v", err)
	}
	if got.Enabled {
		t.Error("Enabled = true after install, want false regardless of what the source file claimed")
	}
	if got.Authority.BDWrite || len(got.Authority.Actions) != 0 {
		t.Errorf("Authority = %+v after install, want zero value regardless of what the source file claimed", got.Authority)
	}
	if got.Source != "installed:community-sneaky" {
		t.Errorf("Source = %q, want %q", got.Source, "installed:community-sneaky")
	}

	// Re-read from disk — the safety rule must actually be persisted, not
	// just returned in memory.
	all, err := LoadAll(dir, t.TempDir())
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	for _, p := range all {
		if p.ID != "sneaky" {
			continue
		}
		if p.Enabled || p.Authority.BDWrite {
			t.Errorf("persona on disk = %+v, want authority still stripped after a reload", p)
		}
	}
}

func TestInstallRequiresName(t *testing.T) {
	src := writeSourceMD(t, Persona{ID: "x", Name: "X", Prompt: "p", Trigger: Trigger{}})
	if _, err := Install(t.TempDir(), src, ""); err == nil {
		t.Fatal("Install() with an empty name: want an error, got nil")
	}
}

func TestUpdateRefreshesContentKeepsAuthority(t *testing.T) {
	dir := t.TempDir()
	installed := Persona{
		ID: "qa", Name: "QA v1", Description: "old", Prompt: "old prompt",
		Trigger: Trigger{Schedule: "*/10 * * * *"},
		Source:  "installed:community-qa",
		// Simulates a human having already reviewed and enabled it after
		// the original Install forced these off.
		Enabled:   true,
		Authority: Authority{BDWrite: true, Actions: []string{"comment"}},
	}
	if err := Save(dir, installed); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	src := writeSourceMD(t, Persona{
		ID: "qa", Name: "QA v2", Description: "new", Prompt: "new prompt",
		Trigger: Trigger{Schedule: "*/15 * * * *"},
		// Even if the upstream source now claims more authority, Update
		// must never apply it — only Name/Description/Prompt/Model/Trigger.
		Enabled:   true,
		Authority: Authority{BDWrite: true, Actions: []string{"create", "reopen"}},
	})

	updated, err := Update(dir, src, "qa")
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if !updated {
		t.Fatal("Update() = false, want true (content changed)")
	}

	got, err := loadOne(dir, "qa")
	if err != nil {
		t.Fatalf("loadOne() error: %v", err)
	}
	if got.Name != "QA v2" || got.Prompt != "new prompt" || got.Trigger.Schedule != "*/15 * * * *" {
		t.Errorf("content not refreshed: %+v", got)
	}
	if !got.Enabled || !got.Authority.BDWrite || len(got.Authority.Actions) != 1 || got.Authority.Actions[0] != "comment" {
		t.Errorf("authority = %+v, want the human's locally-set authority untouched by the content update", got.Authority)
	}
}

func TestUpdateNoopWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := Persona{
		ID: "qa", Name: "QA", Prompt: "check", Trigger: Trigger{},
		Source: "installed:community-qa",
	}
	if err := Save(dir, p); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	src := writeSourceMD(t, p)

	updated, err := Update(dir, src, "qa")
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if updated {
		t.Error("Update() = true, want false (source and stored copy already match)")
	}
}

func TestUpdateRejectsNonInstalledPersona(t *testing.T) {
	dir := t.TempDir()
	p := Persona{ID: "qa", Name: "QA", Prompt: "check", Trigger: Trigger{}, Source: SourceBuiltin}
	if err := Save(dir, p); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	src := writeSourceMD(t, p)

	if _, err := Update(dir, src, "qa"); err == nil {
		t.Fatal("Update() on a builtin persona: want an error, got nil")
	}
}
