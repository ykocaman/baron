package persona

import (
	"embed"
	"fmt"
)

// embeddedPersonas is BARON's own built-in persona set, compiled into the
// binary from the repo's own personas/*.md — the file-per-persona,
// human-readable-markdown replacement for the old defaults() Go literal.
// Embedding guarantees the built-ins ship even when baron runs against a
// project that has no personas/ directory of its own (most projects); a
// project that *does* have its own personas/*.md (this repo included, when
// baron runs from a real checkout rather than just the built binary) reads
// those directly off disk instead — see loadRepoLayer, which prefers a
// real on-disk directory over this embedded copy whenever both exist.
//
//go:embed personas/*.md
var embeddedPersonasFS embed.FS

// embeddedPersonas parses every file embeddedPersonasFS holds.
func embeddedPersonas() ([]Persona, error) {
	entries, err := embeddedPersonasFS.ReadDir("personas")
	if err != nil {
		return nil, fmt.Errorf("read embedded personas: %w", err)
	}
	personas := make([]Persona, 0, len(entries))
	for _, e := range entries {
		data, err := embeddedPersonasFS.ReadFile("personas/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", e.Name(), err)
		}
		p, err := UnmarshalMD(data)
		if err != nil {
			return nil, fmt.Errorf("parse embedded %s: %w", e.Name(), err)
		}
		personas = append(personas, p)
	}
	return personas, nil
}
