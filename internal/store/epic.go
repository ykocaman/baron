package store

import "strings"

// EpicOpenChildren returns id's open descendants at any depth — a child's
// own child still counts against the top epic closing, not just its
// immediate parent ("açık alt bead'i ... olan epic kapanamaz"). Descent is
// walked via ParentOf per hop, so this recognizes a child linked either way
// bd carries the relationship: the explicit Parent field, or the dotted-ID
// convention ("<parent>.<n>", possibly nested) as ParentOf's own fallback.
// Checking only the dotted-ID prefix directly (this function's own
// original implementation) silently missed any child created with an
// explicit --parent whose ID bd didn't also nest — letting an epic with a
// real open child advance to mergable early.
func EpicOpenChildren(id string, all []Bead) []Bead {
	var open []Bead
	for _, b := range all {
		if !IsTerminalStatus(b.Status) && isDescendantOf(b, id, all) {
			open = append(open, b)
		}
	}
	return open
}

// isDescendantOf reports whether b descends from ancestorID at any depth,
// walking b's own parent chain one ParentOf hop at a time. The seen guard
// is a defensive cycle-break — bd's own data should never form a parent
// loop, but a chain walk that could spin forever on bad data would be a
// worse failure mode than silently reporting "not a descendant."
func isDescendantOf(b Bead, ancestorID string, all []Bead) bool {
	seen := map[string]bool{}
	cur := b
	for {
		parent, ok := ParentOf(cur, all)
		if !ok {
			return false
		}
		if parent.ID == ancestorID {
			return true
		}
		if seen[parent.ID] {
			return false
		}
		seen[parent.ID] = true
		cur = parent
	}
}

// IsTerminalStatus reports whether status means the bead is done — for
// epic-closure purposes, and for anywhere else a finished-vs-live split
// matters (e.g. the TUI sorting finished work to the bottom of a list).
// merged counts as terminal: a child that finished via a merge rests at
// "merged" (see customStatuses) rather than moving on to "closed", but
// it's just as done as one that was closed directly.
func IsTerminalStatus(s BeadStatus) bool {
	return s == BeadStatusClosed || s == BeadStatusCancelled || s == BeadStatusMerged
}

// ParentOf returns b's parent bead from all: the explicit Parent field (a
// BRN, bd's own hierarchy link) when set, else the dotted-ID convention
// ("<parent>.<n>", possibly nested) as a fallback — bd carries both, and
// not every hierarchy is expressed in the ID. ok is false when b has no
// parent, or the parent isn't present in all.
func ParentOf(b Bead, all []Bead) (Bead, bool) {
	if b.Parent != "" {
		for _, c := range all {
			if string(c.BRN) == b.Parent {
				return c, true
			}
		}
		return Bead{}, false
	}
	dot := strings.LastIndex(b.ID, ".")
	if dot <= 0 {
		return Bead{}, false
	}
	parentID := b.ID[:dot]
	for _, c := range all {
		if c.ID == parentID {
			return c, true
		}
	}
	return Bead{}, false
}
