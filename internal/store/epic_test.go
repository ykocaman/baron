package store

import "testing"

func TestEpicOpenChildrenNone(t *testing.T) {
	all := []Bead{
		{ID: "baron-obq", Status: BeadStatusOpen},
		{ID: "baron-obq.1", Status: BeadStatusClosed},
		{ID: "baron-obq.2", Status: BeadStatusCancelled},
	}
	got := EpicOpenChildren("baron-obq", all)
	if len(got) != 0 {
		t.Errorf("EpicOpenChildren() = %v, want none (all children terminal)", got)
	}
}

func TestEpicOpenChildrenSome(t *testing.T) {
	all := []Bead{
		{ID: "baron-obq", Status: BeadStatusOpen},
		{ID: "baron-obq.1", Status: BeadStatusClosed},
		{ID: "baron-obq.2", Status: BeadStatusWorking},
	}
	got := EpicOpenChildren("baron-obq", all)
	if len(got) != 1 || got[0].ID != "baron-obq.2" {
		t.Errorf("EpicOpenChildren() = %v, want [baron-obq.2]", got)
	}
}

func TestEpicOpenChildrenExcludesUnrelatedBeads(t *testing.T) {
	all := []Bead{
		{ID: "baron-obq", Status: BeadStatusOpen},
		{ID: "baron-other", Status: BeadStatusOpen},
		// A different epic sharing a numeric suffix must not match "baron-obq.".
		{ID: "baron-obqx.1", Status: BeadStatusOpen},
	}
	got := EpicOpenChildren("baron-obq", all)
	if len(got) != 0 {
		t.Errorf("EpicOpenChildren() = %v, want none (no real children present)", got)
	}
}

func TestEpicOpenChildrenNested(t *testing.T) {
	all := []Bead{
		{ID: "baron-obq", Status: BeadStatusOpen},
		{ID: "baron-obq.1", Status: BeadStatusClosed},
		{ID: "baron-obq.1.1", Status: BeadStatusWorking},
	}
	got := EpicOpenChildren("baron-obq", all)
	if len(got) != 1 || got[0].ID != "baron-obq.1.1" {
		t.Errorf("EpicOpenChildren() = %v, want the open grandchild baron-obq.1.1", got)
	}
}

// TestEpicOpenChildrenExplicitParentField: a child bd linked via the
// explicit Parent field rather than a dotted ID (bd doesn't always nest
// the ID just because --parent was given) must still count as open — the
// bug this guards against let tryAdvanceParent (internal/cli/work_mutate.go)
// mark an epic mergable while a real open child, unrelated by ID alone,
// was still pending.
func TestEpicOpenChildrenExplicitParentField(t *testing.T) {
	all := []Bead{
		{ID: "obq", BRN: "baron-obq", Status: BeadStatusOpen},
		{ID: "xyz", BRN: "baron-xyz", Parent: "baron-obq", Status: BeadStatusWorking},
	}
	got := EpicOpenChildren("obq", all)
	if len(got) != 1 || got[0].ID != "xyz" {
		t.Errorf("EpicOpenChildren() = %v, want the Parent-field-linked child baron-xyz", got)
	}
}

// TestEpicOpenChildrenExplicitParentFieldGrandchild: the explicit-Parent-
// field case must also chain through multiple hops, same as the dotted-ID
// case does.
func TestEpicOpenChildrenExplicitParentFieldGrandchild(t *testing.T) {
	all := []Bead{
		{ID: "obq", BRN: "baron-obq", Status: BeadStatusOpen},
		{ID: "mid", BRN: "baron-mid", Parent: "baron-obq", Status: BeadStatusClosed},
		{ID: "leaf", BRN: "baron-leaf", Parent: "baron-mid", Status: BeadStatusWorking},
	}
	got := EpicOpenChildren("obq", all)
	if len(got) != 1 || got[0].ID != "leaf" {
		t.Errorf("EpicOpenChildren() = %v, want the open grandchild baron-leaf", got)
	}
}
