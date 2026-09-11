package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir returns ~/.config/baron/personas — the global, cross-project user
// layer (a genuine content fork of a repo-sourced persona, or a brand-new
// custom persona not tied to one project), matching config.toml's own user
// layer (internal/store/config_load.go's LoadConfig).
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "baron", "personas"), nil
}

// path is where p is stored within dir.
func path(dir, id string) string {
	return filepath.Join(dir, id+".md")
}

// LoadAll resolves the full, effective persona set for a project: BARON's
// own embedded built-ins (personas/*.md, compiled in — see embedded.go),
// overridden/extended by projectDir's own personas/*.md if it has one (a
// project's own committed crew — additive for a new ID, a full replace for
// one that collides with a built-in), overridden again by userDir's own
// content forks and custom personas (~/.config/baron/personas — the same
// full-replace rule), with projectDir's own .baron/personas/state.toml
// applying an enabled override on top of whichever content won — unless
// that content already came from userDir, whose own enabled field is
// authoritative once a real content fork exists (see persona.State's own
// doc comment for why enabled is kept apart from content in the first
// place: flipping a repo-shipped persona on/off in one project must not
// fork its entire prompt just to record one bit). Returned alphabetical by
// ID.
func LoadAll(userDir, projectDir string) ([]Persona, error) {
	byID := map[string]Persona{}
	fromUser := map[string]bool{}

	base, err := embeddedPersonas()
	if err != nil {
		return nil, err
	}
	for _, p := range base {
		byID[p.ID] = p
	}

	repo, err := loadLayer(filepath.Join(projectDir, "personas"))
	if err != nil {
		return nil, err
	}
	for _, p := range repo {
		byID[p.ID] = p
	}

	user, err := loadLayer(userDir)
	if err != nil {
		return nil, err
	}
	for _, p := range user {
		byID[p.ID] = p
		fromUser[p.ID] = true
	}

	state, err := LoadState(projectDir)
	if err != nil {
		return nil, err
	}
	for id, ps := range state.Personas {
		if fromUser[id] {
			continue
		}
		if p, ok := byID[id]; ok {
			p.Enabled = ps.Enabled
			byID[id] = p
		}
	}

	personas := make([]Persona, 0, len(byID))
	for _, p := range byID {
		personas = append(personas, p)
	}
	sort.Slice(personas, func(i, j int) bool { return personas[i].ID < personas[j].ID })
	return personas, nil
}

// loadLayer reads every *.md persona file directly under dir — a real,
// optional directory (a project's own personas/, or the global user
// override dir); a missing dir is not an error, just "this layer has
// nothing to contribute."
func loadLayer(dir string) ([]Persona, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var personas []Persona
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		p, err := UnmarshalMD(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		personas = append(personas, p)
	}
	return personas, nil
}

// Save writes p to dir/<id>.md, creating dir if needed. Overwrites
// whatever was there — callers that care about clobbering a hand-edit
// (baron persona update, crew-mode.md §6) check first. dir is always
// userDir (~/.config/baron/personas) in practice — the embedded and
// project-repo layers are read-only from BARON's own perspective, a human
// edits those files directly with a text editor, not through this.
func Save(dir string, p Persona) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := MarshalMD(p)
	if err != nil {
		return fmt.Errorf("marshal persona %s: %w", p.ID, err)
	}
	if err := os.WriteFile(path(dir, p.ID), data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", p.ID, err)
	}
	return nil
}

// Delete removes a persona's file from dir (the user layer — see Save's
// own doc comment; there is nothing to delete for an embedded or
// project-repo persona, only disable).
func Delete(dir, id string) error {
	if err := os.Remove(path(dir, id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", id, err)
	}
	return nil
}
