package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/domain"
)

// parseBeads parses bd --json output into beads, deriving each bead's BRN
// from its ID using the given prefix.
func parseBeads(data []byte, prefix string) ([]Bead, error) {
	items, err := jsonItems(data)
	if err != nil {
		return nil, err
	}
	beads := make([]Bead, 0, len(items))
	for _, it := range items {
		var b Bead
		if err := json.Unmarshal(it, &b); err != nil {
			return nil, fmt.Errorf("parse bead: %w", err)
		}
		b.Status = normalizeStatus(b.Status)
		b.BRN = brnFromID(b.ID, prefix)
		beads = append(beads, b)
	}
	return beads, nil
}

// parseComments parses bd comments --json output into comments, accepting
// both the bare array (bd 1.x) and the versioned envelope (bd 2.0+).
func parseComments(data []byte) ([]Comment, error) {
	items, err := jsonItems(data)
	if err != nil {
		return nil, err
	}
	comments := make([]Comment, 0, len(items))
	for _, it := range items {
		var c Comment
		if err := json.Unmarshal(it, &c); err != nil {
			return nil, fmt.Errorf("parse comment: %w", err)
		}
		comments = append(comments, c)
	}
	return comments, nil
}

// envelope is the bd 2.0+ JSON wrapper {"version":2,"items":[...]}.
type envelope struct {
	Version int               `json:"version"`
	Items   []json.RawMessage `json:"items"`
}

// UnmarshalJSON accepts bd's numeric priority (0-4, 0 highest) and maps it
// 1:1 onto P1-P5 (n=0 -> P1, ..., n=4 -> P5) — a straight relabeling, not a
// bucketing: the previous version of this type collapsed bd's 5 real levels
// into 3 named ones ("high" covered both 0 and 1), which made a P0
// (critical) bead render identically to a P1 one. Clamped defensively to
// bd's documented 0-4 range even though bd itself never emits outside it.
func (p *Priority) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		switch {
		case n < 0:
			n = 0
		case n > 4:
			n = 4
		}
		*p = Priority(fmt.Sprintf("P%d", n+1))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*p = Priority(s)
	return nil
}

// jsonItems extracts the raw bead objects from bd --json output, accepting
// both the bare array (bd 1.x) and the versioned envelope (bd 2.0+).
func jsonItems(data []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty bd --json output")
	}
	if trimmed[0] == '{' {
		var env envelope
		if err := json.Unmarshal(trimmed, &env); err != nil {
			return nil, fmt.Errorf("parse bd envelope: %w", err)
		}
		if env.Version != 0 || env.Items != nil {
			return env.Items, nil
		}
		// Single bead object without a version key.
		return []json.RawMessage{trimmed}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, fmt.Errorf("parse bd list: %w", err)
	}
	return items, nil
}

// isEnvelope reports whether data is a bd 2.0+ versioned envelope.
func isEnvelope(data []byte) bool {
	var probe struct {
		Version int `json:"version"`
	}
	return json.Unmarshal(data, &probe) == nil && probe.Version != 0
}

// beadFromOutput parses the result of a create command: either a bead JSON
// object/array/envelope, or a bare issue ID (as printed by `bd q`).
func beadFromOutput(out []byte, title, prefix string) (*Bead, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("bd create: empty output")
	}
	if trimmed[0] == '[' || trimmed[0] == '{' {
		beads, err := parseBeads(trimmed, prefix)
		if err != nil {
			return nil, err
		}
		if len(beads) == 0 {
			return nil, fmt.Errorf("bd create: no bead in output")
		}
		return &beads[0], nil
	}
	now := time.Now().UTC()
	return &Bead{
		ID:        string(trimmed),
		BRN:       brnFromID(string(trimmed), prefix),
		Title:     title,
		Status:    normalizeStatus(BeadStatusOpen),
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// versionPattern matches the first semver in bd --version output.
var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

// parseVersion extracts the first semver from bd --version output such as
// "bd version 1.0.5 (Homebrew)".
func parseVersion(out string) (string, error) {
	m := versionPattern.FindString(out)
	if m == "" {
		return "", fmt.Errorf("no semver in bd version output %q", out)
	}
	return m, nil
}

// semver is a parsed major.minor.patch version.
type semver struct {
	major, minor, patch int
}

// parseSemver parses "v?major.minor.patch".
func parseSemver(s string) (semver, error) {
	var v semver
	if _, err := fmt.Sscanf(strings.TrimPrefix(s, "v"), "%d.%d.%d", &v.major, &v.minor, &v.patch); err != nil {
		return semver{}, fmt.Errorf("invalid semver %q", s)
	}
	return v, nil
}

// brnFromID maps a bd issue ID (e.g. "baron-a1b2c3") to a BRN. With an empty
// prefix the ID is the BRN; otherwise the prefix is prepended unless already
// present.
func brnFromID(id, prefix string) domain.BRN {
	if prefix == "" {
		return domain.BRN(id)
	}
	if strings.HasPrefix(id, prefix+"-") {
		return domain.BRN(id)
	}
	return domain.BRN(prefix + "-" + id)
}
