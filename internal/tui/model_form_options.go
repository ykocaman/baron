package tui

import (
	"sort"
	"strings"

	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/store"
)

func (m Model) newBeadFormTypeOptions() []huh.Option[string] {
	types := []string{"task", "feature", "bug", "epic", "chore", "decision"}
	opts := make([]huh.Option[string], len(types))
	for i, t := range types {
		label := m.styles.typeStyle(t).Render(typeGlyph(t) + " " + t)
		opts[i] = huh.NewOption(label, t)
	}
	return opts
}

// newBeadFormTierOptions returns the huh.Options for the new-bead form's
// tier select field, in ascending-then-guru order (free/fast/standard/
// expert/guru) — "fast", the default, is deliberately the second entry,
// not the first (free), so it doesn't read as "the bottom of the list."
func (m Model) newBeadFormTierOptions() []huh.Option[string] {
	tiers := []agent.Tier{agent.TierFree, agent.TierFast, agent.TierStandard, agent.TierExpert, agent.TierGuru}
	opts := make([]huh.Option[string], len(tiers))
	for i, t := range tiers {
		opts[i] = huh.NewOption(string(t), string(t))
	}
	return opts
}

// newBeadFormParentOptions returns the huh.Options for the new-bead form's
// parent-epic select field: a leading "(none)" choice (the field is
// optional — most new beads are not an epic's child) followed by every
// active epic, newest-updated first, displayed as "BRN · first words of the
// description" so the option list is legible without a live preview pane.
// Terminal epics (closed/cancelled/merged) can't take children and are
// excluded — the field is explicitly "for an epic's child". No "self" to
// exclude here: this options the parent of a bead that doesn't exist yet,
// so even the epic currently selected on the board (the common case this
// form prefills from) is always a valid candidate.
func (m Model) newBeadFormParentOptions() []huh.Option[string] {
	var epics []store.Bead
	for _, b := range m.beads {
		if b.IssueType != "epic" {
			continue
		}
		if store.IsTerminalStatus(b.Status) {
			continue
		}
		epics = append(epics, b)
	}
	sort.SliceStable(epics, func(i, j int) bool { return epics[i].UpdatedAt.After(epics[j].UpdatedAt) })
	opts := make([]huh.Option[string], 0, len(epics)+1)
	opts = append(opts, huh.NewOption("(none)", ""))
	for _, b := range epics {
		disp := string(b.BRN)
		if d := firstWords(strings.TrimSpace(b.Description), 7); d != "" {
			disp += " · " + d
		}
		opts = append(opts, huh.NewOption(disp, string(b.BRN)))
	}
	return opts
}

// firstWords returns the first n whitespace-separated words of s.
func firstWords(s string, n int) string {
	words := strings.Fields(s)
	if len(words) > n {
		words = words[:n]
	}
	return strings.Join(words, " ")
}

// humanQueueOf filters beads to those parked in the human queue.
func humanQueueOf(beads []store.Bead) []store.Bead {
	out := make([]store.Bead, 0)
	for _, b := range beads {
		if b.Status == store.BeadStatusHumanQueue {
			out = append(out, b)
		}
	}
	return out
}
