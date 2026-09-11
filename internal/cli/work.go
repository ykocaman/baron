package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// createResult is the JSON shape of `baron work create`.
type createResult struct {
	BRN   domain.BRN `json:"brn"`
	ID    string     `json:"id"`
	Title string     `json:"title"`
	State string     `json:"state"`
}

// CreateBeadInput bundles CreateBead's raw string fields — Priority and
// Tier are still unparsed strings here (parsePriority/parseTier run inside
// CreateBead itself) — kept as a struct to stay within this codebase's
// argument-count limit.
type CreateBeadInput struct {
	Title, Description, Accept, Priority, IssueType, Parent, Tier string
}

// CreateBead opens a new bead, cobra-free: no flag parsing, no --json
// branching, no accept-criteria warning (the caller — the CLI's own RunE,
// or the TUI's new-bead form — decides whether and how to surface that;
// it isn't this method's call to make, unlike EditBead/AssignBead/CloseBead
// which already follow this split).
func (a *app) CreateBead(ctx context.Context, in CreateBeadInput) (createResult, error) {
	prio, err := parsePriority(in.Priority)
	if err != nil {
		return createResult{}, err
	}
	t, err := parseTier(in.Tier)
	if err != nil {
		return createResult{}, err
	}
	bead, err := a.beads.Create(ctx, store.CreateBeadParams{
		Title:       in.Title,
		Description: in.Description,
		Acceptance:  in.Accept,
		Priority:    prio,
		Parent:      in.Parent,
		IssueType:   in.IssueType,
		Tier:        t,
	})
	if err != nil {
		return createResult{}, err
	}
	return createResult{BRN: bead.BRN, ID: bead.ID, Title: bead.Title, State: string(bead.Status)}, nil
}

// parseTier validates a --tier flag value against the known tiers. Tier is
// mandatory — every bead must declare the capability class it needs — so
// unlike parsePriority, an empty value is itself an error.
func parseTier(s string) (agent.Tier, error) {
	if agent.IsTier(agent.Tier(s)) {
		return agent.Tier(s), nil
	}
	return "", usagef("--tier is required: want one of guru, expert, standard, fast, free (got %q)", s)
}

// parsePriority maps a --priority flag value to a store.Priority (P1-P5,
// case-insensitive — "p1" and "P1" both work). An empty value means "let bd
// decide", which is bd's own default (P3 in BARON's scale, 2 on bd's own
// 0-4 one — see store.Priority's doc comment).
func parsePriority(s string) (store.Priority, error) {
	if s == "" {
		return "", nil
	}
	switch store.Priority(strings.ToUpper(s)) {
	case store.PriorityP1, store.PriorityP2, store.PriorityP3, store.PriorityP4, store.PriorityP5:
		return store.Priority(strings.ToUpper(s)), nil
	}
	return "", usagef("invalid priority %q: want P1, P2, P3, P4, or P5", s)
}

// findBead resolves a BRN to its bead. Beads has no get-by-id call, so this
// scans List. raw is the pre-parseBRNArg argument; it also matches the
// canonical "BRN-" form when the configured brnPrefix differs from it.
func (a *app) findBead(ctx context.Context, brn domain.BRN, raw string) (store.Bead, error) {
	beads, err := a.beads.List(ctx)
	if err != nil {
		return store.Bead{}, err
	}
	for _, b := range beads {
		if b.BRN == brn || string(b.BRN) == "BRN-"+raw {
			return b, nil
		}
	}
	return store.Bead{}, fmt.Errorf("bead %s not found", brn)
}

// brnShape matches [<prefix>-]<repo>-<id> with any alphanumeric id suffix. bd
// issue id shapes vary between versions (6-char hex in v1, shorter in v2), and
// the store derives BRNs by prefixing without validating, so the CLI accepts
// any well-formed shape rather than domain.BRN's stricter pattern.
// bd IDs use the form <repo>-<hash> where repo may contain underscores; an
// optional dotted integer suffix carries bd's hierarchical parent-child ids
// ("Epic ve alt görevler hierarchical ID ile taşınır"),
// e.g. baron-obq.1 for the first child of epic baron-obq.
var brnShape = regexp.MustCompile(`^[a-z0-9_]+(?:[-_][a-z0-9_]+)*-[a-z0-9]+(?:\.[0-9]+)*$`)

// parseBRNArg validates a BRN argument. Bare bd ids like "baron-a1b2c3" are
// accepted and prefixed with the configured prefix when set.
func (a *app) parseBRNArg(s string) (domain.BRN, error) {
	prefixed := a.brnPrefix + "-"
	if a.brnPrefix != "" && !strings.HasPrefix(s, prefixed) {
		s = prefixed + s
	}
	if !brnShape.MatchString(s) {
		return "", usagef("invalid bead reference %q: want %s<repo>-<id>", s, prefixed)
	}
	return domain.BRN(s), nil
}

// idOf strips the configured prefix, recovering the bd issue id.
func (a *app) idOf(brn domain.BRN) string {
	return strings.TrimPrefix(string(brn), a.brnPrefix+"-")
}

// loadAgents populates the agent registry from the machine-wide cache
// (~/.cache/baron/agents.json), then applies the project's [[agents]]
// overrides. A missing cache is not an error: `baron init` and `baron
// doctor` write it, and every consumer falls back to probing on demand.
func (a *app) loadAgents() {
	reg, _, err := agent.LoadAgents()
	if err != nil {
		a.warn("agent cache: %v", err)
	}
	a.agents = reg
	a.applyAgentOverrides()
}

// applyAgentOverrides merges config [[agents]] overrides into the registry.
// Config entries take precedence over discovered ones; a missing config
// leaves the registry as loaded.
func (a *app) applyAgentOverrides() {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return
	}
	a.agents.MergeConfig(cfg.Agents)
}

// configAgents returns the project's [[agents]] blocks, nil when the config
// can't be read — the shape doctor.Run and agent.Refresh take.
func (a *app) configAgents() []agent.ConfigAgent {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return nil
	}
	return cfg.Agents
}
