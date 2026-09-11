// Package persona implements Crew Mode's persona data model: scheduled or
// triggered prompts that manage bead state directly — QA, clean-code
// review, red-team, and the reviewer/closer synchronous gate personas — see
// docs/PRD/crew-mode.md. (The interactive "Developer" persona was removed
// from the system — crew-mode.md's own changelog — in favor of Board
// Mode's lowercase 'p' quick prompt, which types straight into the active
// agent-CLI tab instead of dispatching a persona run; see
// internal/tui.Model.openQuickPromptOnSelected.) Personas are stored as one
// file per persona under ~/.config/baron/personas/, global (not
// per-project) so the same crew works across every project on the machine,
// matching how ~/.cache/baron/agents.json holds the machine-wide agent
// registry.
package persona

import (
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"
)

// TransitionRule matches a bead state change from From to To — "*" on
// either side matches any state, so {From:"*", To:"merged"} means "any
// bead that reaches merged," and {From:"working", To:"retry"} means "a run
// just bounced." A persona subscribes to bead-state events via a list of
// these (Trigger.On) instead of one hardcoded boolean — this is what lets
// a persona "position itself on the state machine" rather than poll a
// single fixed condition.
type TransitionRule struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to" yaml:"to"`
}

// Matches reports whether the transition from->to satisfies r.
func (r TransitionRule) Matches(from, to string) bool {
	return (r.From == "*" || r.From == from) && (r.To == "*" || r.To == to)
}

// wildcard is the TransitionRule "any state" marker.
const wildcard = "*"

// Source records where a persona definition came from, for baron persona
// update's diffing (see Install) and for the ingest safety rule: anything
// other than SourceBuiltin/SourceUser (i.e. an "installed:<name>" source)
// has its authority forced off regardless of what the file itself claims.
type Source string

// Built-in source values. An installed persona's Source is
// "installed:<name>" — not a constant, since <name> varies.
const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
)

// Model names which agent CLI and capability tier a persona runs under —
// the same registry/tier vocabulary task-agents use (internal/agent).
// Both empty means "let baron pick a default", same as a bead with no
// explicit assignee.
type Model struct {
	Agent string `json:"agent,omitempty" yaml:"agent,omitempty"`
	Tier  string `json:"tier,omitempty" yaml:"tier,omitempty"`
}

// Trigger is a persona's subscription — a cron schedule, a set of bead
// state transitions to react to, or both — plus a Scope filter that picks
// which beads it's actually pointed at when fired. Empty Schedule and
// empty On means manual-only: nothing but a human firing it in Prompt Mode
// ever launches it.
//
// Both trigger kinds resolve to the same thing, a subject bead set:
// a transition's subject is the one bead that just transitioned (see
// TransitionRule); a schedule's subject is every bead currently matching
// Scope. That's deliberate — the whole point is that "cron" and "event"
// aren't two different mechanisms, they're two ways of arriving at the
// same "here are the beads you're pointed at" call into a persona.
type Trigger struct {
	// Schedule is a standard 5-field cron expression ("0 9 * * *" = daily
	// at 09:00) evaluated against Trigger's own last-fire time. Empty means
	// no time-based firing.
	Schedule string `json:"schedule,omitempty" yaml:"schedule,omitempty"`
	// On is the set of bead-state transitions this persona reacts to. A
	// transition fires the persona at most once per reconcile pass even if
	// several rules match at once.
	On []TransitionRule `json:"on,omitempty" yaml:"on,omitempty"`
	// IssueTypes restricts both trigger kinds to beads of these issue
	// types (bug/task/feature/chore); empty means no restriction. This is
	// Trigger's Scope filter — deliberately just an issue-type list for
	// now rather than a general query language (see crew-mode.md §2's
	// explicit "don't build a rules engine" note).
	IssueTypes []string `json:"issue_types,omitempty" yaml:"issue_types,omitempty"`
	// MinIntervalMinutes debounces repeated transition fires — a persona
	// that reacts to a busy transition (e.g. any state change) won't fire
	// again within this many minutes of its last run, regardless of how
	// many more matching transitions land. 0 means no debounce.
	MinIntervalMinutes int `json:"min_interval_minutes,omitempty" yaml:"min_interval_minutes,omitempty"`
}

// Manual reports whether t only ever fires from a human explicitly firing
// the persona (no schedule, no transition subscriptions) — any persona a
// user deliberately leaves trigger-less.
func (t Trigger) Manual() bool {
	return t.Schedule == "" && len(t.On) == 0
}

// summary renders a one-line human description of t for the Crew Roster —
// "cron 0 9 * * *", "on *→merged, *→closed", "manual", or a combination.
func (t Trigger) summary() string {
	var parts []string
	if t.Schedule != "" {
		parts = append(parts, "cron "+t.Schedule)
	}
	if len(t.On) > 0 {
		var rules strings.Builder
		for i, r := range t.On {
			if i > 0 {
				rules.WriteString(", ")
			}
			rules.WriteString(r.From + "→" + r.To)
		}
		parts = append(parts, "on "+rules.String())
	}
	if len(parts) == 0 {
		return "manual"
	}
	var out strings.Builder
	out.WriteString(parts[0])
	for _, p := range parts[1:] {
		out.WriteString(" + " + p)
	}
	return out.String()
}

// Summary is Trigger's exported one-line description — see summary.
func (t Trigger) Summary() string { return t.summary() }

// EventsText renders t.On back into the comma-separated "from->to" form
// ParseTransitionRules reads — the TUI edit form's round-trip text for the
// field summary() renders with "→" (not typeable in a plain text.Input),
// so editing pre-fills from this instead.
func (t Trigger) EventsText() string {
	parts := make([]string, len(t.On))
	for i, r := range t.On {
		parts[i] = r.From + "->" + r.To
	}
	return strings.Join(parts, ", ")
}

// ParseTransitionRules parses the TUI edit form's event-trigger field: a
// comma-separated list of "from->to" pairs ("*" on either side matches any
// state, same as TransitionRule.Matches — e.g. "*->merged, *->closed").
// Accepts "→" too, so a value round-tripped from EventsText's output or
// pasted from Summary's own rendering both parse. Blank input (after
// trimming) is not an error — it means "no event triggers," clearing
// Trigger.On entirely, same as leaving the field empty in the JSON file.
// Whitespace around each pair and each side of the arrow is trimmed; From/To
// presence is enforced by Persona.Validate (the "both non-empty" rule this
// deliberately doesn't duplicate), not here.
func ParseTransitionRules(s string) ([]TransitionRule, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var rules []TransitionRule
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sep := "->"
		if !strings.Contains(part, sep) {
			sep = "→"
		}
		fromTo := strings.SplitN(part, sep, 2)
		if len(fromTo) != 2 {
			return nil, fmt.Errorf("invalid event trigger %q: want \"from->to\" (\"*\" for any state)", part)
		}
		rules = append(rules, TransitionRule{
			From: strings.TrimSpace(fromTo[0]),
			To:   strings.TrimSpace(fromTo[1]),
		})
	}
	return rules, nil
}

// SkillsText renders p.Skills back into the comma-separated text the TUI
// edit form's Skills field round-trips through — ParseSkills' inverse, same
// pairing as EventsText/ParseTransitionRules.
func (p Persona) SkillsText() string {
	return strings.Join(p.Skills, ", ")
}

// ParseSkills parses the TUI edit form's Skills field: a comma-separated
// list of npm package specs ("pkg", "pkg@1.2.3", "@scope/pkg@1.0.0") or
// plugin zip URLs. Blank input (after trimming) is not an error — it clears
// Skills entirely, same as ParseTransitionRules' empty-string case.
func ParseSkills(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// validateSchedule reports whether expr parses as a standard 5-field cron
// expression (also accepting the "@daily"/"@hourly"/... predefined
// schedules cron.ParseStandard supports).
func validateSchedule(expr string) error {
	_, err := cron.ParseStandard(expr)
	return err
}

// Authority describes what a persona is allowed to do to bead state.
// BDWrite is the real, technically-enforced boundary (see
// internal/domain.TaskAgentEnv — a persona with BDWrite false is launched
// with the same BEADS_DB isolation a task-agent gets); Actions is a
// finer-grained, prompt-and-audit-enforced convention on top of that (see
// crew-mode.md §3's "not a technical wall yet" note) — bd has no verb-level
// ACL, so a persona with BDWrite true but Actions restricted to
// ["comment"] is trusted, not sandboxed, to stay within that list.
type Authority struct {
	BDWrite bool     `json:"bd_write" yaml:"bd_write"`
	Actions []string `json:"actions,omitempty" yaml:"actions,omitempty"`
}

// Persona is one Crew Mode crew member.
type Persona struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Prompt      string    `json:"prompt"`
	Model       Model     `json:"model"`
	Trigger     Trigger   `json:"trigger"`
	Authority   Authority `json:"authority"`
	Enabled     bool      `json:"enabled"`
	Source      Source    `json:"source"`
	// Skills are Claude Code Skills to expose to this persona's session, on
	// top of whatever the prompt itself says — an npm package spec
	// ("pkg" or "pkg@1.2.3", fetched and staged locally) or a plugin zip
	// URL, one per entry. Only the "claude" agent can actually load these
	// (via --plugin-dir/--plugin-url, scoped to this one launch — see
	// internal/cli.personaSkillExtras); every other agent CLI ignores
	// Skills entirely, same as Authority.Actions being claude/opencode-only
	// today. Nil/empty means "just the prompt," the same as before this
	// field existed.
	Skills []string `json:"skills,omitempty"`
}

// FormFields carries a persona edit/create form's raw, un-parsed field
// values across the TUI -> cli boundary — not Persona itself, since
// Trigger.On/Skills are typed ([]TransitionRule/[]string) while a form
// still holds raw text for both (Events/Skills below); cli does the
// parsing (see ParseTransitionRules/ParseSkills). Kept as one struct, not
// positional parameters, to stay within this codebase's argument-count
// limit.
type FormFields struct {
	ID, Name, Prompt, Description string
	Schedule, Events, Skills      string
	ModelTier, ModelAgent         string
	BDWrite                       bool
	BDActions                     []string
	Enabled                       bool
}

// FormFields renders p back into its own edit form's raw-text shape — for
// a caller that already holds a live Persona and needs to submit a
// form-shaped edit built from it (e.g. a plain enabled-flag toggle)
// without re-deriving each field by hand.
func (p Persona) FormFields() FormFields {
	return FormFields{
		ID:          p.ID,
		Name:        p.Name,
		Prompt:      p.Prompt,
		Description: p.Description,
		Schedule:    p.Trigger.Schedule,
		Events:      p.Trigger.EventsText(),
		Skills:      p.SkillsText(),
		ModelTier:   p.Model.Tier,
		ModelAgent:  p.Model.Agent,
		BDWrite:     p.Authority.BDWrite,
		BDActions:   p.Authority.Actions,
		Enabled:     p.Enabled,
	}
}

// Validate reports whether p is well-formed enough to store and launch.
// It does not check that Model.Agent/Tier resolve to anything real — that
// depends on machine state (internal/agent's registry) the persona package
// itself has no business knowing about.
func (p Persona) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("persona id is empty")
	}
	if p.Name == "" {
		return fmt.Errorf("persona %q: name is empty", p.ID)
	}
	if p.Prompt == "" {
		return fmt.Errorf("persona %q: prompt is empty", p.ID)
	}
	if p.Trigger.Schedule != "" {
		if err := validateSchedule(p.Trigger.Schedule); err != nil {
			return fmt.Errorf("persona %q: invalid cron schedule %q: %w", p.ID, p.Trigger.Schedule, err)
		}
	}
	for _, r := range p.Trigger.On {
		if r.From == "" || r.To == "" {
			return fmt.Errorf("persona %q: transition rule needs both from and to (use %q for any state)", p.ID, wildcard)
		}
	}
	return nil
}

// Installed reports whether p came from `baron persona install` (source
// "installed:<name>", not the literal SourceBuiltin/SourceUser values) —
// the ingest safety rule (crew-mode.md §6) checks this to decide whether
// the file's own authority can be trusted at all (it can't).
func (p Persona) Installed() bool {
	return p.Source != SourceBuiltin && p.Source != SourceUser
}
