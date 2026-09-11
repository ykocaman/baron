package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

func (a *app) FireNow(ctx context.Context, personaID string) error {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return err
	}
	personas, err := a.loadPersonas()
	if err != nil {
		return err
	}
	p, ok := findPersona(personas, personaID)
	if !ok {
		return fmt.Errorf("no persona named %q", personaID)
	}
	allBeads, err := a.beads.List(ctx)
	if err != nil {
		return err
	}
	candidates := scopeCandidates(allBeads, p.Trigger.IssueTypes)
	if len(candidates) > 15 {
		candidates = candidates[:15]
	}

	var subj personaSubject
	if len(p.Trigger.On) > 0 && len(candidates) > 0 {
		// Event/transition persona manually fired: target the most relevant candidate bead and resolve its branch
		target := candidates[0]
		branch := domain.BranchName(target.IssueType, a.idOf(target.BRN))
		subj = personaSubject{
			brn:        string(target.BRN),
			branch:     branch,
			bead:       &target,
			candidates: candidates,
			reason:     "fired manually (target: " + string(target.BRN) + " [" + branch + "])",
		}
	} else {
		// Global/schedule persona (e.g. red-team) running sweep over candidates on main
		subj = personaSubject{
			candidates: candidates,
			reason:     "fired manually (" + strconv.Itoa(len(candidates)) + " candidates on main)",
		}
	}
	return a.launchPersona(ctx, cfg, agent.LoadCatalog(), p, subj)
}

// reconcilePersonaTriggers evaluates every enabled persona's trigger
// (docs/PRD/crew-mode.md §5) and fires the ones that are due — a bead
// reaching a state the persona's Trigger.On subscribes to, or a
// Trigger.Schedule cron expression coming due. There is no separate
// scheduler daemon: this piggybacks on Reconcile's own periodic cadence
// (whatever already calls Reconcile — the TUI's tick today, `baron watch`
// once roadmap.md §2 sıra 1 lands) the same way status/assignment/authority
// drift do. A manual-only persona (Trigger.Manual — no schedule, no
// transitions) never fires from here by construction: it has nothing in
// its own Trigger for fireOnTransitions/fireOnSchedule to match.
//
// Failures (a persona's agent isn't installed, tmux unavailable, a launch
// error) are warned and skipped, never fatal to the rest of Reconcile —
// same posture as every other best-effort step in this pass.
func (a *app) reconcilePersonaTriggers(ctx context.Context, cfg *store.Config, allBeads []store.Bead, cat *agent.Catalog) {
	personas, err := a.loadPersonas()
	if err != nil {
		a.warn("persona trigger: %v", err)
		return
	}

	state := a.loadPersonaState()
	now := time.Now()
	// beadTransitions always writes state.LastSeenStates for every bead it
	// sees, even when nothing transitioned (the first-observation seed
	// itself is a state change) — dirty must cover that, or a seed made
	// this pass never persists and every bead reads as "first observation"
	// again next time, permanently masking real transitions.
	dirty := len(allBeads) > 0

	transitions := a.beadTransitions(allBeads, &state)

	for _, p := range personas {
		if !p.Enabled {
			continue
		}
		if a.fireOnTransitions(ctx, cfg, cat, p, transitions, &state, now) {
			dirty = true
			continue // matched an event this pass — don't also cron-fire it
		}
		if a.fireOnSchedule(ctx, cfg, cat, p, allBeads, &state, now) {
			dirty = true
		}
	}

	if dirty {
		a.savePersonaState(state)
	}
}

// beadTransition is one bead's state change observed between two
// consecutive Reconcile passes.
type beadTransition struct {
	bead     store.Bead
	from, to string
}

// beadTransitions reports every bead whose domain state differs from what
// state.LastSeenStates recorded on the *previous* pass, updating
// LastSeenStates for every bead regardless. A bead with no prior entry
// (Reconcile's first-ever look at it) is only seeded, never reported as a
// transition — the "first observation isn't an event" rule
// reconcileStateDrift already established, so enabling a persona on a
// project full of already-merged beads doesn't fire it once per
// pre-existing bead on the very next pass.
func (a *app) beadTransitions(allBeads []store.Bead, state *personaState) []beadTransition {
	if state.LastSeenStates == nil {
		state.LastSeenStates = map[string]string{}
	}
	var out []beadTransition
	for i := range allBeads {
		bead := allBeads[i]
		id := a.idOf(bead.BRN)
		current := string(resolveDomainState(bead))
		prior, seen := state.LastSeenStates[id]
		if seen && current != prior {
			out = append(out, beadTransition{bead: bead, from: prior, to: current})
		}
		state.LastSeenStates[id] = current
	}
	return out
}

// fireOnTransitions looks for a transition this pass that matches p's
// Trigger.On rules (and IssueTypes scope), firing p against the FIRST
// match — at most once per persona per pass, even if several beads
// transitioned in a way p cares about; the rest will still be there (or
// have moved on) next pass. Reports whether it fired.
func (a *app) fireOnTransitions(ctx context.Context, cfg *store.Config, cat *agent.Catalog, p persona.Persona, transitions []beadTransition, state *personaState, now time.Time) bool {
	if len(p.Trigger.On) == 0 {
		return false
	}
	if debounced(state, p.ID, now, p.Trigger.MinIntervalMinutes) {
		return false
	}
	for _, t := range transitions {
		if !matchesTransition(p.Trigger.On, t.from, t.to) {
			continue
		}
		if !matchesIssueType(t.bead, p.Trigger.IssueTypes) {
			continue
		}
		branch := domain.BranchName(t.bead.IssueType, a.idOf(t.bead.BRN))
		subj := personaSubject{
			brn:    string(t.bead.BRN),
			branch: branch,
			bead:   &t.bead,
			reason: fmt.Sprintf("reacted to %s→%s", t.from, t.to),
		}
		if err := a.launchPersona(ctx, cfg, cat, p, subj); err != nil {
			a.warn("persona %s: %v", p.ID, err)
			continue
		}
		markRun(state, p.ID, now)
		return true
	}
	return false
}

// fireOnSchedule fires p once its Trigger.Schedule cron expression comes
// due, pointed at every bead currently matching its IssueTypes scope (all
// beads if unset) as candidates — the persona's own prompt decides what,
// if anything, is worth a comment. A persona's very first schedule
// observation only seeds LastRun (same "first observation isn't an event"
// rule beadTransitions applies) rather than firing immediately the moment
// it's enabled.
func (a *app) fireOnSchedule(ctx context.Context, cfg *store.Config, cat *agent.Catalog, p persona.Persona, allBeads []store.Bead, state *personaState, now time.Time) bool {
	if p.Trigger.Schedule == "" {
		return false
	}
	last := state.LastRun[p.ID]
	if last.IsZero() {
		markRun(state, p.ID, now)
		return true
	}
	if !cronDue(p.Trigger.Schedule, last, now) {
		return false
	}
	candidates := scopeCandidates(allBeads, p.Trigger.IssueTypes)
	subj := personaSubject{candidates: candidates, reason: "cron sweep (" + strconv.Itoa(len(candidates)) + " candidates)"}
	if err := a.launchPersona(ctx, cfg, cat, p, subj); err != nil {
		a.warn("persona %s: %v", p.ID, err)
		return false
	}
	markRun(state, p.ID, now)
	return true
}

// markRun records id's most recent fire time in state.LastRun.
func markRun(state *personaState, id string, now time.Time) {
	if state.LastRun == nil {
		state.LastRun = map[string]time.Time{}
	}
	state.LastRun[id] = now
}

// debounced reports whether id last ran within minIntervalMinutes of now —
// Trigger.MinIntervalMinutes' whole purpose: a persona reacting to a noisy
// transition (e.g. any state change) shouldn't refire on every single one
// in quick succession. 0 (or no prior run) never debounces.
func debounced(state *personaState, id string, now time.Time, minIntervalMinutes int) bool {
	if minIntervalMinutes <= 0 {
		return false
	}
	last, ok := state.LastRun[id]
	if !ok {
		return false
	}
	return now.Sub(last) < time.Duration(minIntervalMinutes)*time.Minute
}

// matchesTransition reports whether any rule in on matches the from->to
// transition.
func matchesTransition(on []persona.TransitionRule, from, to string) bool {
	for _, r := range on {
		if r.Matches(from, to) {
			return true
		}
	}
	return false
}

// matchesIssueType reports whether bead's issue type is in types — empty
// types means no restriction (matches everything).
func matchesIssueType(bead store.Bead, types []string) bool {
	if len(types) == 0 {
		return true
	}
	return slices.Contains(types, string(bead.IssueType))
}

// scopeCandidates filters allBeads down to those matching types (see
// matchesIssueType) — Trigger.IssueTypes' resolution for a schedule fire's
// candidate set.
func scopeCandidates(allBeads []store.Bead, types []string) []store.Bead {
	if len(types) == 0 {
		return allBeads
	}
	out := make([]store.Bead, 0, len(allBeads))
	for _, b := range allBeads {
		if matchesIssueType(b, types) {
			out = append(out, b)
		}
	}
	return out
}

// cronStandardParser parses the 5-field cron expressions Trigger.Schedule
// uses (plus the "@daily"/"@hourly"/... predefined schedules) — the one
// parser both persona.Persona.Validate and cronDue share, so a schedule
// that validates on save is guaranteed to also evaluate here.
var cronStandardParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// cronDue reports whether expr's next scheduled time at-or-after lastRun
// is at-or-before now. lastRun zero (never observed before) is never due
// — see fireOnSchedule's seed-first-pass comment.
func cronDue(expr string, lastRun, now time.Time) bool {
	sched, err := cronStandardParser.Parse(expr)
	if err != nil || lastRun.IsZero() {
		return false
	}
	return !sched.Next(lastRun).After(now)
}

// personaSubject is what one persona run is pointed at: either a single
// bead (brn set — an event reaction or a manual dispatch) or a list of
// candidate beads (a cron sweep with no single triggering bead). reason is
// a short human/audit-facing description of WHY this run fired, used both
// in the audit log and folded into the prompt.
