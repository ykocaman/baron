package persona

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatterDelim is the line that opens and closes a persona file's YAML
// header — the same convention Claude Code's own SKILL.md files use, which
// is the whole point: a human who already knows how to read/write a
// SKILL.md already knows how to read/write a persona file.
const frontmatterDelim = "---"

// frontmatter is every Persona field except Prompt — Prompt is the file's
// own body, not a YAML string (see MarshalMD's doc comment for why that
// split exists at all). Mirrors Persona's own JSON tags 1:1 so a human
// editing a field's name here matches what they'd expect from the old
// JSON shape.
type frontmatter struct {
	ID          string    `yaml:"id"`
	Name        string    `yaml:"name"`
	Description string    `yaml:"description,omitempty"`
	Model       Model     `yaml:"model"`
	Trigger     Trigger   `yaml:"trigger"`
	Authority   Authority `yaml:"authority"`
	Enabled     bool      `yaml:"enabled"`
	Source      Source    `yaml:"source"`
	Skills      []string  `yaml:"skills,omitempty"`
}

// MarshalMD renders p as a persona file: a YAML frontmatter header (every
// field except Prompt) between two `---` lines, then Prompt verbatim as the
// rest of the file. Prompt is deliberately never YAML-encoded (no quoting,
// no escaping, no line-wrapping decisions a YAML library would make on its
// own) — the entire reason for this format over JSON is that a human opens
// the file and the prompt is just the file, readable and editable with
// nothing but a text editor. A multi-paragraph or bulleted prompt round-
// trips exactly as written.
func MarshalMD(p Persona) ([]byte, error) {
	fm := frontmatter{
		ID: p.ID, Name: p.Name, Description: p.Description,
		Model: p.Model, Trigger: p.Trigger, Authority: p.Authority,
		Enabled: p.Enabled, Source: p.Source, Skills: p.Skills,
	}
	header, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("marshal persona %s frontmatter: %w", p.ID, err)
	}
	var b bytes.Buffer
	b.WriteString(frontmatterDelim)
	b.WriteString("\n")
	b.Write(header)
	b.WriteString(frontmatterDelim)
	b.WriteString("\n")
	b.WriteString(p.Prompt)
	if !strings.HasSuffix(p.Prompt, "\n") {
		b.WriteString("\n")
	}
	return b.Bytes(), nil
}

// UnmarshalMD parses a persona file back into a Persona — MarshalMD's
// inverse. The body (everything after the closing `---`) becomes Prompt
// verbatim, with exactly one trailing newline trimmed (MarshalMD's own
// always-end-in-one-newline guarantee) so a round trip through
// MarshalMD(UnmarshalMD(b)) reproduces b unchanged.
func UnmarshalMD(data []byte) (Persona, error) {
	s := string(data)
	if !strings.HasPrefix(s, frontmatterDelim+"\n") {
		return Persona{}, fmt.Errorf("missing opening %q frontmatter delimiter", frontmatterDelim)
	}
	rest := s[len(frontmatterDelim)+1:]
	closeIdx := strings.Index(rest, "\n"+frontmatterDelim+"\n")
	if closeIdx < 0 {
		return Persona{}, fmt.Errorf("missing closing %q frontmatter delimiter", frontmatterDelim)
	}
	header := rest[:closeIdx+1]
	prompt := rest[closeIdx+1+len(frontmatterDelim)+1:]
	prompt = strings.TrimSuffix(prompt, "\n")

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
		return Persona{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	return Persona{
		ID: fm.ID, Name: fm.Name, Description: fm.Description,
		Prompt: prompt, Model: fm.Model, Trigger: fm.Trigger,
		Authority: fm.Authority, Enabled: fm.Enabled, Source: fm.Source,
		Skills: fm.Skills,
	}, nil
}
