package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/tool"
)

// ErrNoReadyWork is returned by BeadStore.Claim when the ready queue is
// empty — a distinct sentinel rather than a nil *Bead with a nil error, so
// callers can tell "nothing to claim" apart from a caller bug that forgot
// to check err.
var ErrNoReadyWork = errors.New("no ready work available")

// List returns every bead in the project, regardless of status. Uses bd
// list --all --json.
func (s *BeadStore) List(ctx context.Context) ([]Bead, error) {
	return s.listJSON(ctx, []string{"list", "--all", "--json"})
}

// ListReady returns beads in the ready queue. Uses bd ready --json.
func (s *BeadStore) ListReady(ctx context.Context) ([]Bead, error) {
	return s.listJSON(ctx, []string{"ready", "--json"})
}

// Claim atomically claims a bead from the ready queue. Uses bd ready --claim
// --json. Returns ErrNoReadyWork when the queue is empty.
func (s *BeadStore) Claim(ctx context.Context) (*Bead, error) {
	data, err := s.runJSON(ctx, []string{"ready", "--claim", "--json"})
	if err != nil {
		return nil, err
	}
	beads, err := parseBeads(data, s.brnPrefix)
	if err != nil {
		return nil, fmt.Errorf("bd ready --claim: %w", err)
	}
	if len(beads) == 0 {
		return nil, ErrNoReadyWork
	}
	return &beads[0], nil
}

// CreateBeadParams bundles BeadStore.Create's fields (kept as a struct, not
// positional parameters, to stay within this codebase's argument-count
// limit). Description, Acceptance, Priority, Parent and IssueType are
// passed through when non-empty; Parent may be a BRN ("baron-a") or bare
// id, either is stripped to the bare id bd expects. An empty Priority lets
// bd default. Tier is required — every bead must declare the capability
// class it needs (see Bead.Tier).
type CreateBeadParams struct {
	Title, Description, Acceptance string
	Priority                       Priority
	Parent, IssueType              string
	Tier                           agent.Tier
}

// Create creates a new bead. Uses bd create --json. Tier is set via bd
// create's own --metadata flag rather than a follow-up bd update, so
// creation stays a single call.
//
// This is enforced by BARON only: bd itself has no required-custom-field
// mechanism (--metadata is free-form, unvalidated), so a bead created by
// calling bd directly, bypassing baron, can still end up with no tier
// metadata — Bead.Tier() defaults that (and any unrecognized value) to
// agent.TierFast rather than reading as empty, so Reconcile's
// tier-resolution pass (reconcileTierAssignments) still resolves it like
// any other fast-tier bead instead of leaving it unassigned forever.
func (s *BeadStore) Create(ctx context.Context, p CreateBeadParams) (*Bead, error) {
	if p.Tier == "" {
		return nil, fmt.Errorf("bd create: tier is required")
	}
	args := []string{"create", p.Title, "--json"}
	if p.Description != "" {
		args = append(args, "--description", p.Description)
	}
	if p.Acceptance != "" {
		args = append(args, "--acceptance", p.Acceptance)
	}
	if p.Priority != "" {
		args = append(args, "--priority", p.Priority.bdValue())
	}
	if p.Parent != "" {
		parent := strings.TrimPrefix(p.Parent, s.brnPrefix+"-")
		args = append(args, "--parent", parent)
	}
	if p.IssueType != "" {
		args = append(args, "--type", p.IssueType)
	}
	metadata, err := json.Marshal(map[string]string{"tier": string(p.Tier)})
	if err != nil {
		return nil, fmt.Errorf("bd create: encode tier metadata: %w", err)
	}
	args = append(args, "--metadata", string(metadata))
	res, err := s.runner.Run(ctx, "bd", args, tool.Options{Dir: s.dir})
	if err != nil {
		return nil, fmt.Errorf("bd create: %w", err)
	}
	return beadFromOutput([]byte(res.Stdout), p.Title, s.brnPrefix)
}

// bdValue maps a P1-P5 priority to bd's own numeric scale (0-4, 0 highest):
// a straight n-1. An unrecognized value falls back to bd's own default (2 =
// P3), same as parsePriority already only lets valid P1-P5 through.
func (p Priority) bdValue() string {
	switch p {
	case PriorityP1:
		return "0"
	case PriorityP2:
		return "1"
	case PriorityP3:
		return "2"
	case PriorityP4:
		return "3"
	case PriorityP5:
		return "4"
	default:
		return "2"
	}
}

// Assign assigns a bead to a real agent CLI name. Uses bd assign.
func (s *BeadStore) Assign(ctx context.Context, id, assignee string) error {
	return s.runSimple(ctx, "assign", id, assignee)
}

// SetModelMetadata sets or clears the bead's model/effort custom-metadata
// (bd update --set-metadata / --unset-metadata) in one bd call. A nil
// pointer leaves that key untouched; a pointer to "" clears it; any other
// pointer sets it. This is deliberately two named parameters, not a
// generic map, so call sites (and the bd argument order, for tests) stay
// deterministic — bd's custom-metadata is a general mechanism, but BARON
// only ever writes these two keys (see Bead.Model/Bead.Effort).
//
// Writing the model also clears the legacy "llm" key, so a bead assigned
// before the rename can't keep a stale second answer that Bead.Model would
// fall back to once the new key is cleared.
func (s *BeadStore) SetModelMetadata(ctx context.Context, id string, model, effort *string) error {
	args := []string{"update", id}
	args = appendMetadataArg(args, "model", model)
	if model != nil {
		empty := ""
		args = appendMetadataArg(args, "llm", &empty)
	}
	args = appendMetadataArg(args, "effort", effort)
	if len(args) == 2 {
		return nil // nothing to change
	}
	return s.runSimple(ctx, args...)
}

// SetTierMetadata reassigns the capability tier a bead requires (bd update
// --set-metadata tier=...). Unlike SetModelMetadata's model/effort pair,
// tier is mandatory, so this never unsets it — reassigning always means
// picking a different tier, not clearing the requirement.
func (s *BeadStore) SetTierMetadata(ctx context.Context, id string, tier agent.Tier) error {
	if tier == "" {
		return fmt.Errorf("bd update: tier is required")
	}
	return s.runSimple(ctx, "update", id, "--set-metadata", "tier="+string(tier))
}

// appendMetadataArg appends the bd flag for one --set-metadata/--unset-metadata
// change, or returns args unchanged when v is nil (leave untouched).
func appendMetadataArg(args []string, key string, v *string) []string {
	if v == nil {
		return args
	}
	if *v == "" {
		return append(args, "--unset-metadata", key)
	}
	return append(args, "--set-metadata", key+"="+*v)
}

// Comment adds a comment to a bead. Uses bd comment.
func (s *BeadStore) Comment(ctx context.Context, id, comment string) error {
	return s.runSimple(ctx, "comment", id, comment)
}

// Comments returns the comments on a bead. Uses bd comments --json. id may
// be a bare bd issue ID or a full BRN; the prefix is stripped when set.
func (s *BeadStore) Comments(ctx context.Context, id string) ([]Comment, error) {
	if s.brnPrefix != "" && strings.HasPrefix(id, s.brnPrefix+"-") {
		id = strings.TrimPrefix(id, s.brnPrefix+"-")
	}
	data, err := s.runJSON(ctx, []string{"comments", id, "--json"})
	if err != nil {
		return nil, fmt.Errorf("bd comments: %w", err)
	}
	comments, err := parseComments(data)
	if err != nil {
		return nil, fmt.Errorf("bd comments: %w", err)
	}
	return comments, nil
}

// Close closes a bead. Uses bd close.
func (s *BeadStore) Close(ctx context.Context, id string) error {
	return s.runSimple(ctx, "close", id)
}

// Status updates a bead's status. Uses bd update --status.
//
// bd's built-in vocabulary is a subset of BARON's (baronStatusNames collapses
// working to "in_progress", merged/cancelled to "closed"), so BARON's own
// states register as bd custom statuses (bd config set status.custom) and are
// written literally — they round-trip verbatim instead of collapsing onto
// in_progress/open and reading back as "working".
func (s *BeadStore) Status(ctx context.Context, id string, status BeadStatus) error {
	if isCustomStatus(status) {
		if err := s.ensureCustomStatuses(ctx); err != nil {
			return err
		}
		return s.runSimple(ctx, "update", id, "--status", string(status))
	}
	return s.runSimple(ctx, "update", id, "--status", bdStatus(status))
}

// customStatuses are BARON states bd has no built-in for; registered via
// bd config set status.custom so they round-trip verbatim. working is
// excluded: bdStatus maps it to "in_progress" and normalizeStatus restores
// it, so it needs no registration. merged is included so a merged bead
// rests at "merged" instead of collapsing into bd's own "closed" and
// becoming indistinguishable from a bead closed without ever being merged.
// cancelled needs the same treatment for the same reason: bdStatus mapped
// it to "closed" with no reverse entry in bdStatusNames, so a cancelled
// bead read back as plain "closed" — indistinguishable from a real close,
// and reachable again via closed's own reopen edge, silently defeating the
// "cancelled has no exit" rule the state machine documents
// (allowedTransitions[BeadStateCancelled] is empty).
var customStatuses = []string{"validating", "retry", "mergable", "human_queue", "merged", "cancelled"}

// isCustomStatus reports whether status is one of BARON's custom statuses.
func isCustomStatus(s BeadStatus) bool {
	return slices.Contains(customStatuses, string(s))
}

// bdStatusCustomKey is bd's config key for BARON's custom status list.
const bdStatusCustomKey = "status.custom"

// ensureCustomStatuses registers BARON's custom statuses in the project's bd
// config on first use. Runs once per process (customOnce); a missing config
// get/set returns the error. Also covers the upgrade case: a project whose
// status.custom was set by an older BARON build (missing an entry this
// build now expects, e.g. "merged") gets re-registered with the full
// current list, so that entry doesn't silently collapse back to bd's own
// status on write.
func (s *BeadStore) ensureCustomStatuses(ctx context.Context) error {
	s.customOnce.Do(func() {
		res, err := s.runner.Run(ctx, "bd", []string{"config", "get", bdStatusCustomKey}, tool.Options{Dir: s.dir})
		if err != nil {
			s.customErr = err
			return
		}
		if strings.Contains(res.Stdout, "not set") {
			s.customErr = s.setCustomStatuses(ctx)
			return
		}
		have := map[string]bool{}
		for c := range strings.SplitSeq(strings.TrimSpace(res.Stdout), ",") {
			have[strings.TrimSpace(c)] = true
		}
		for _, c := range customStatuses {
			if !have[c] {
				s.customErr = s.setCustomStatuses(ctx)
				break
			}
		}
	})
	return s.customErr
}

// setCustomStatuses writes the full current custom-status list to bd's
// config, overwriting whatever was there before.
func (s *BeadStore) setCustomStatuses(ctx context.Context) error {
	return s.runSimple(ctx, "config", "set", bdStatusCustomKey, strings.Join(customStatuses, ","))
}

// Edit updates a bead's title and/or description via bd update.
func (s *BeadStore) Edit(ctx context.Context, id, title, description string) error {
	args := []string{"update", id}
	if title != "" {
		args = append(args, "--title", title)
	}
	if description != "" {
		args = append(args, "--description", description)
	}
	return s.runSimple(ctx, args...)
}

// DepAdd links dep as a dependency of id. Uses bd dep add; the default
// dependency type is "blocks", which is passed explicitly only when overridden.
func (s *BeadStore) DepAdd(ctx context.Context, id, dep, depType string) error {
	args := []string{"dep", "add", id, dep}
	if depType != "" && depType != "blocks" {
		args = append(args, "--type", depType)
	}
	return s.runSimple(ctx, args...)
}

// Deps returns the beads that block id. Uses bd dep list --json.
func (s *BeadStore) Deps(ctx context.Context, id string) ([]Bead, error) {
	data, err := s.runJSON(ctx, []string{"dep", "list", id, "--json"})
	if err != nil {
		return nil, err
	}
	beads, err := parseBeads(data, s.brnPrefix)
	if err != nil {
		return nil, fmt.Errorf("bd dep list: %w", err)
	}
	return beads, nil
}

// Version returns the bd version.
