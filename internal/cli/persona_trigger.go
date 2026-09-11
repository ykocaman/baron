package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/baron-cli/baron/internal/persona"
)

// loadPersonasFromDisk is app.loadPersonas' real, production implementation
// — the three-layer resolution persona.LoadAll's own doc comment describes
// (embedded built-ins, projectDir's own personas/*.md, ~/.config/baron/
// personas). Tests stub app.loadPersonas instead of calling this, so a
// test run never reads or writes the developer's actual persona config
// (same reasoning as loadConfig's own test stub in cli_test.go).
func loadPersonasFromDisk(projectDir string) ([]persona.Persona, error) {
	dir, err := persona.Dir()
	if err != nil {
		return nil, err
	}
	return persona.LoadAll(dir, projectDir)
}

// personaQuotaCheckDelay is how long launchPersona waits after spawning a
// candidate before capturing its pane to decide whether it hit a quota/rate-
// limit error and should be retried with the next candidate. 2.5 s gives
// even slow agent CLIs enough startup time to print their first error line.
// Tests set this to 0 to avoid real wall-clock waits.
var personaQuotaCheckDelay = 2500 * time.Millisecond

// savePersonaToDisk is app.savePersona's real, production implementation —
// the write-side counterpart of loadPersonasFromDisk, same test-isolation
// reasoning.
func savePersonaToDisk(p persona.Persona) error {
	dir, err := persona.Dir()
	if err != nil {
		return err
	}
	return persona.Save(dir, p)
}

// findPersona looks id up in personas, the one lookup every entry point in
// this file needs before it can do anything else.
func findPersona(personas []persona.Persona, id string) (persona.Persona, bool) {
	for _, p := range personas {
		if p.ID == id {
			return p, true
		}
	}
	return persona.Persona{}, false
}

// UpdatePersonaByID edits a persona's prompt, description, trigger (cron
// schedule and event/state-machine-transition rules — see
// persona.ParseTransitionRules for the edit form's "from->to, from->to"
// text format), Skills (see persona.ParseSkills for the edit form's
// comma-separated text format), and enabled flag in place — the Prompt
// Mode 'e' edit form. Model/Authority stay untouched — this is a
// content/metadata edit, never a way to grant a persona new authority from
// the TUI.
//
// Where the write actually lands depends on what changed and where f.ID
// currently resolves from (see persona.LoadAll's own doc comment for the
// three-layer precedence this mirrors): a persona that already has a real
// file in the user layer (~/.config/baron/personas/<id>.md) gets every
// edit written there — including a bare enabled flip — since the fork
// already exists and there's nothing left to protect by keeping it out.
// Otherwise (a repo/embedded persona never before touched): a change to
// anything but enabled creates that first fork; a change to *only* enabled
// writes nothing but persona.SetEnabled's own project-local state.toml
// entry, so flipping a built-in on/off in one project never duplicates its
// whole prompt into ~/.config just to record one bit.
func (a *app) UpdatePersonaByID(f persona.FormFields) error {
	personas, err := a.loadPersonas()
	if err != nil {
		return err
	}
	p, ok := findPersona(personas, f.ID)
	if !ok {
		return fmt.Errorf("no persona named %q", f.ID)
	}
	rules, err := persona.ParseTransitionRules(f.Events)
	if err != nil {
		return err
	}
	newSkills := persona.ParseSkills(f.Skills)

	userDir, err := persona.Dir()
	if err != nil {
		return err
	}
	forked, err := personaHasUserFile(userDir, f.ID)
	if err != nil {
		return err
	}

	contentUnchanged := f.Prompt == p.Prompt && f.Description == p.Description &&
		f.Schedule == p.Trigger.Schedule && transitionRulesEqual(rules, p.Trigger.On) &&
		slices.Equal(newSkills, p.Skills) &&
		p.Model.Tier == f.ModelTier && p.Model.Agent == f.ModelAgent &&
		f.BDWrite == p.Authority.BDWrite && slices.Equal(f.BDActions, p.Authority.Actions)
	if !forked && contentUnchanged {
		return persona.SetEnabled(a.dir, f.ID, f.Enabled)
	}

	p.Prompt = f.Prompt
	p.Description = f.Description
	p.Trigger.Schedule = f.Schedule
	p.Trigger.On = rules
	p.Skills = newSkills
	p.Model.Tier = f.ModelTier
	p.Model.Agent = f.ModelAgent
	p.Authority.BDWrite = f.BDWrite
	p.Authority.Actions = f.BDActions
	p.Enabled = f.Enabled
	if p.Source == persona.SourceBuiltin {
		// A genuine content fork becomes user-owned from here on — the
		// same Source a human-authored persona.CreatePersonaByFields
		// entry gets, and keeps Installed()'s ingest-safety check
		// (crew-mode.md §6) from ever seeing a builtin-tagged file outside
		// this package's own embedded/repo layers.
		p.Source = persona.SourceUser
	}
	if err := p.Validate(); err != nil {
		return err
	}
	return a.savePersona(p)
}

// personaHasUserFile reports whether id already has a real file in the
// user layer (userDir, ~/.config/baron/personas in production) — the
// dividing line UpdatePersonaByID uses between "still resolving from the
// embedded/repo layer, an enabled-only edit can stay a lightweight
// state.toml entry" and "already forked, everything lands here now."
func personaHasUserFile(userDir, id string) (bool, error) {
	_, err := os.Stat(filepath.Join(userDir, id+".md"))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// transitionRulesEqual compares two Trigger.On slices order-sensitively —
// the edit form always rebuilds On fresh from its own EventsText/
// ParseTransitionRules round trip, so a genuinely unchanged selection
// reproduces the exact same order every time.
func transitionRulesEqual(a, b []persona.TransitionRule) bool {
	return slices.Equal(a, b)
}

// FireNow manually fires personaID right now, against every bead
// currently matching its own Trigger.IssueTypes scope, ignoring Schedule
// timing and MinIntervalMinutes debounce entirely — the Crew Roster's 'f'
// key ("run it now, regardless of when it's next due").
