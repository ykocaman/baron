package persona

import (
	"os"
	"path/filepath"
	"testing"
)

func findPersona(personas []Persona, id string) (Persona, bool) {
	for _, p := range personas {
		if p.ID == id {
			return p, true
		}
	}
	return Persona{}, false
}

// TestLoadAllReturnsEmbeddedBuiltinsWithEmptyUserDir: the 5 built-ins are
// always available via the embedded layer (personas/*.md, compiled in —
// see embedded.go), whether or not userDir exists or has anything in it —
// no "first run seeds files into userDir" step exists any more (the old
// JSON-era behavior this replaces used to materialize a full copy on disk;
// the whole point of the embedded layer is that userDir stays untouched
// until a human genuinely creates or forks something there).
func TestLoadAllReturnsEmbeddedBuiltinsWithEmptyUserDir(t *testing.T) {
	userDir := filepath.Join(t.TempDir(), "personas") // must not exist yet
	projectDir := t.TempDir()

	got, err := LoadAll(userDir, projectDir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	base, err := embeddedPersonas()
	if err != nil {
		t.Fatalf("embeddedPersonas() error: %v", err)
	}
	if len(got) != len(base) {
		t.Fatalf("LoadAll() = %d personas, want %d (the embedded built-ins)", len(got), len(base))
	}
	for _, p := range got {
		if p.Enabled {
			t.Errorf("embedded persona %q is enabled, want disabled (no background process starts unopted)", p.ID)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID > got[i].ID {
			t.Errorf("personas not alphabetical: %q before %q", got[i-1].ID, got[i].ID)
		}
	}
	if _, err := os.Stat(userDir); !os.IsNotExist(err) {
		t.Errorf("userDir = %s exists after LoadAll(), want it left untouched (no seeding)", userDir)
	}
}

func TestSaveRejectsInvalidPersona(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Persona{ID: "bad"}); err == nil {
		t.Fatal("Save() with an invalid persona (no name/prompt/trigger) succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.md")); !os.IsNotExist(err) {
		t.Errorf("Save() of an invalid persona wrote a file anyway")
	}
}

func TestSaveOverwrites(t *testing.T) {
	dir := t.TempDir()
	p := Persona{ID: "qa", Name: "QA v1", Prompt: "check", Trigger: Trigger{}}
	if err := Save(dir, p); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	p.Name = "QA v2"
	if err := Save(dir, p); err != nil {
		t.Fatalf("Save() (overwrite) error: %v", err)
	}
	got, err := LoadAll(dir, t.TempDir())
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	for _, loaded := range got {
		if loaded.ID == "qa" && loaded.Name != "QA v2" {
			t.Errorf("qa.Name = %q, want %q (the overwrite)", loaded.Name, "QA v2")
		}
	}
}

// TestStateTomlEnablesAnEmbeddedBuiltinWithoutForkingIt: the whole point of
// keeping enabled apart from content (persona.State's own doc comment) —
// flipping a built-in on in this project must show up as Enabled:true from
// LoadAll, without ever creating a file under userDir.
func TestStateTomlEnablesAnEmbeddedBuiltinWithoutForkingIt(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	if err := SetEnabled(projectDir, "qa-chromium", true); err != nil {
		t.Fatalf("SetEnabled() error: %v", err)
	}
	got, err := LoadAll(userDir, projectDir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	p, ok := findPersona(got, "qa-chromium")
	if !ok {
		t.Fatalf("LoadAll() = %+v, want qa-chromium present", got)
	}
	if !p.Enabled {
		t.Errorf("qa-chromium.Enabled = false, want true (from state.toml)")
	}
	entries, err := os.ReadDir(userDir)
	if err != nil {
		t.Fatalf("ReadDir(userDir) error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("userDir has %d entries after only a state.toml enable, want 0 (no fork should have been created)", len(entries))
	}
}

// TestStateTomlIgnoredOnceAUserForkExists: once a persona has a real
// user-layer file, its own embedded enabled field is authoritative — an
// older state.toml entry from before the fork existed must not override
// it (see LoadAll's own doc comment, the fromUser skip).
func TestStateTomlIgnoredOnceAUserForkExists(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	if err := SetEnabled(projectDir, "qa-chromium", true); err != nil {
		t.Fatalf("SetEnabled() error: %v", err)
	}
	fork := Persona{
		ID: "qa-chromium", Name: "QA (Chromium)", Prompt: "a locally edited prompt",
		Source: SourceUser, Trigger: Trigger{}, Enabled: false,
	}
	if err := Save(userDir, fork); err != nil {
		t.Fatalf("Save(fork) error: %v", err)
	}

	got, err := LoadAll(userDir, projectDir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	p, ok := findPersona(got, "qa-chromium")
	if !ok {
		t.Fatalf("LoadAll() = %+v, want qa-chromium present", got)
	}
	if p.Enabled {
		t.Errorf("qa-chromium.Enabled = true, want false — the fork's own Enabled field must win over the stale state.toml entry")
	}
}

// TestDeletingAUserForkRevertsToTheEmbeddedBuiltin: deleting a built-in's
// user-layer override doesn't remove the persona entirely — there's
// nothing to remove it *from*, the built-in's own embedded definition is
// compiled into the binary — it just falls back to that definition again,
// same as if the fork had never been created.
func TestDeletingAUserForkRevertsToTheEmbeddedBuiltin(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	fork := Persona{
		ID: "red-team", Name: "Red team (edited)", Prompt: "a locally edited prompt",
		Source: SourceUser, Trigger: Trigger{Schedule: "0 9 * * *"},
	}
	if err := Save(userDir, fork); err != nil {
		t.Fatalf("Save(fork) error: %v", err)
	}
	got, err := LoadAll(userDir, projectDir)
	if err != nil {
		t.Fatalf("LoadAll() (with fork) error: %v", err)
	}
	if p, ok := findPersona(got, "red-team"); !ok || p.Name != "Red team (edited)" {
		t.Fatalf("LoadAll() with the fork present = %+v, want the forked content", got)
	}

	if err := Delete(userDir, "red-team"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	got, err = LoadAll(userDir, projectDir)
	if err != nil {
		t.Fatalf("LoadAll() (after delete) error: %v", err)
	}
	p, ok := findPersona(got, "red-team")
	if !ok {
		t.Fatalf("LoadAll() after deleting the fork = %+v, want red-team still present (reverted to the embedded builtin)", got)
	}
	if p.Name != "Red team" {
		t.Errorf("red-team.Name = %q after deleting the fork, want the embedded builtin's own name back", p.Name)
	}
}
