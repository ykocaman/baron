package persona

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// State is the project-local enabled/disabled overlay: a persona's on/off
// switch is a per-project decision (the same crew member might be wanted in
// one project and not another), so it's kept out of both the repo layer
// (shared, git-tracked defaults) and the global user layer (a real content
// fork) entirely — flipping a repo-shipped persona on in this project must
// not duplicate its whole prompt/description/etc. into a file anywhere.
// TOML, not JSON, matching config.toml's own format and the user's explicit
// "json istemiyorum" — and matching state.toml's own file extension.
type State struct {
	Personas map[string]Config `toml:"personas"`
}

// Config is one persona's project-local override — just Enabled
// today; the shape leaves room for more per-project, per-persona toggles
// later without another format change.
type Config struct {
	Enabled bool `toml:"enabled"`
}

// statePath is where a project's persona state.toml lives — .baron/ is
// already blanket-gitignored in every baron-init'd project (matching the
// existing project-local .baron/personas-state.json trigger-bookkeeping
// file, which is a completely separate concern: that one tracks Reconcile's
// own LastSeenStates/LastRun firing history, this one tracks a human's
// on/off decisions).
func statePath(projectDir string) string {
	return filepath.Join(projectDir, ".baron", "personas", "state.toml")
}

// LoadState reads projectDir's persona state.toml. A missing file is not an
// error — it means "no project-local overrides yet," the same as a fresh
// project that has never toggled anything.
func LoadState(projectDir string) (State, error) {
	data, err := os.ReadFile(statePath(projectDir))
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read %s: %w", statePath(projectDir), err)
	}
	var s State
	if err := toml.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("parse %s: %w", statePath(projectDir), err)
	}
	return s, nil
}

// SetEnabled records id's enabled state for projectDir, creating or
// updating .baron/personas/state.toml as needed — the write side of
// LoadState's "just the on/off bit" contract. Callers decide *when* this
// is the right thing to write (a repo-layer persona) versus writing a full
// user-layer override file instead (see internal/cli's UpdatePersonaByID)
// — State itself doesn't know or care which layer a given ID resolved
// from.
//
// The whole read-modify-write runs under an exclusive advisory lock on
// state.toml.lock: two writers racing here (the TUI toggling a persona
// while `baron persona enable` runs from another shell, or two Reconcile
// passes) would otherwise both LoadState the same content, both mutate
// their own copy, and the second os.Create silently clobber the first's
// change — no error from either side. The write itself lands via a temp
// file + rename so a process killed mid-encode never leaves state.toml
// truncated for the next reader.
func SetEnabled(projectDir, id string, enabled bool) error {
	p := statePath(projectDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	unlock, err := lockFile(p + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	s, err := LoadState(projectDir)
	if err != nil {
		return err
	}
	if s.Personas == nil {
		s.Personas = map[string]Config{}
	}
	s.Personas[id] = Config{Enabled: enabled}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(s); err != nil {
		return fmt.Errorf("encode %s: %w", p, err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, p, err)
	}
	return nil
}
