// Package domain defines BARON's core domain primitives.
package domain

import "strings"

// BRN is a Bead Reference Number: [<prefix>-]<repo>-<seq>.
// It is a branded string type — distinct from plain string in signatures.
type BRN string

// Repo extracts the non-sequence part of a BRN (everything before the final
// -<seq> segment). With a configured prefix the prefix stays attached; callers
// that know the prefix strip it themselves.
func (b BRN) Repo() string {
	i := strings.LastIndexByte(string(b), '-')
	if i < 0 {
		return string(b)
	}
	return string(b)[:i]
}

// Seq extracts the sequence part from a BRN (the final -<seq> segment).
func (b BRN) Seq() string {
	i := strings.LastIndexByte(string(b), '-')
	if i < 0 {
		return ""
	}
	return string(b)[i+1:]
}

// String returns the string representation.
func (b BRN) String() string {
	return string(b)
}
