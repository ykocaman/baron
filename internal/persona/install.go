package persona

import (
	"fmt"
	"os"
	"reflect"
)

// Install reads a persona definition from sourcePath (a local persona
// file, the same markdown+frontmatter shape Save/LoadAll use — deliberately
// not an arbitrary URL fetch: pulling and executing a prompt with potential
// bd-write authority from anywhere on the network is a meaningfully bigger
// attack surface than this package should open on its own; a network-fetch
// front end, if one is ever built, belongs in whatever caller decides to
// take that risk on, not here) and writes it into dir tagged
// "installed:<name>". ingestSafe strips any authority the source file
// claims for itself before it's ever saved — see its doc comment and
// crew-mode.md §6.
func Install(dir, sourcePath, name string) (Persona, error) {
	if name == "" {
		return Persona{}, fmt.Errorf("install: name is empty")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return Persona{}, fmt.Errorf("read %s: %w", sourcePath, err)
	}
	p, err := UnmarshalMD(data)
	if err != nil {
		return Persona{}, fmt.Errorf("parse %s: %w", sourcePath, err)
	}
	p.Source = Source("installed:" + name)
	p = ingestSafe(p)
	if err := Save(dir, p); err != nil {
		return Persona{}, err
	}
	return p, nil
}

// ingestSafe enforces crew-mode.md §6's rule: a persona whose Source this
// codebase didn't itself set (SourceBuiltin/SourceUser) never gets to
// bring its own authority along for the ride. enabled and any bd-mutating
// authority are forced off, regardless of what the source JSON claims —
// installing a persona is the same risk class as installing a dependency
// with a postinstall script. A human must explicitly review it and flip
// Enabled/Authority back on (a plain Save with the fields set) after
// reading what its prompt actually says to do.
func ingestSafe(p Persona) Persona {
	if p.Installed() {
		p.Enabled = false
		p.Authority = Authority{}
	}
	return p
}

// Update re-reads id from sourcePath and overwrites the stored copy's
// content fields only (Name, Description, Prompt, Model, Trigger) — never
// Enabled or Authority, which stay whatever a human last set locally on
// disk. This is deliberate: ingestSafe already locked authority down at
// install time, and re-running it on every content refresh would silently
// disable a persona the human already reviewed and opted into every time
// its prompt text changed upstream, which defeats the point of "update"
// existing at all. Returns updated=false, err=nil when the source and the
// stored copy already match (nothing written) — errors on any target that
// isn't itself Installed() (a builtin/user persona has no source file to
// re-fetch from).
func Update(dir, sourcePath, id string) (updated bool, err error) {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", sourcePath, err)
	}
	incoming, err := UnmarshalMD(data)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", sourcePath, err)
	}

	current, err := loadOne(dir, id)
	if err != nil {
		return false, err
	}
	if !current.Installed() {
		return false, fmt.Errorf("persona %q is %s, not an installed persona — nothing to update from", id, current.Source)
	}

	next := current
	next.Name, next.Description, next.Prompt = incoming.Name, incoming.Description, incoming.Prompt
	next.Model, next.Trigger = incoming.Model, incoming.Trigger

	if reflect.DeepEqual(next, current) {
		return false, nil
	}
	if err := Save(dir, next); err != nil {
		return false, err
	}
	return true, nil
}

// loadOne reads a single persona by id without going through LoadAll's
// seed-on-missing-dir behavior — Update operates on a specific,
// already-installed persona, not the whole set.
func loadOne(dir, id string) (Persona, error) {
	data, err := os.ReadFile(path(dir, id))
	if err != nil {
		return Persona{}, fmt.Errorf("read %s: %w", id, err)
	}
	p, err := UnmarshalMD(data)
	if err != nil {
		return Persona{}, fmt.Errorf("parse %s: %w", id, err)
	}
	return p, nil
}
