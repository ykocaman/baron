package store

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/tool"
)

// BeadStatus represents the state of a bead.
type BeadStatus string

const bdInProgressStatus = "in_progress"

// Priority is a bead's priority. bd's own native scale is an integer 0-4
// (0 highest — bd create --priority accepts "0-4 or P0-P4", default "2").
// BARON surfaces it 1-indexed instead, "P1".."P5" (P1 most urgent, P3 bd's
// own default, P5 least), because "high"/"medium"/"low" — this type's
// previous values — collide in a reader's head with agent tier names
// (fast/standard/expert) and, worse, actually lost information: the old
// UnmarshalJSON bucketed bd's 5 real levels into only 3 labels (0 and 1
// both read "high", 3 and 4 both read "low"), so a P0 (critical) bead was
// visually indistinguishable from a P1 one. P1-P5 is a straight 1:1 mapping
// onto bd's 5 levels — see bdValue/UnmarshalJSON — nothing is bucketed.
type Priority string

// Priority levels, 1-indexed onto bd's 0-4 native scale (P1 = bd 0, ...,
// P5 = bd 4). Not bd's own "P0-P4" spelling — that's an accepted alternate
// input format on bd's --priority flag, not what this type's values mean.
const (
	PriorityP1 Priority = "P1"
	PriorityP2 Priority = "P2"
	PriorityP3 Priority = "P3"
	PriorityP4 Priority = "P4"
	PriorityP5 Priority = "P5"
)

// Statuses bd reports for a bead.
const (
	BeadStatusOpen       BeadStatus = "open"
	BeadStatusAssigned   BeadStatus = "assigned"
	BeadStatusWorking    BeadStatus = "working"
	BeadStatusValidating BeadStatus = "validating"
	BeadStatusRetry      BeadStatus = "retry"
	BeadStatusBlocked    BeadStatus = "blocked"
	BeadStatusMergable   BeadStatus = "mergable"
	BeadStatusHumanQueue BeadStatus = "human_queue"
	BeadStatusMerged     BeadStatus = "merged"
	BeadStatusClosed     BeadStatus = "closed"
	BeadStatusCancelled  BeadStatus = "cancelled"
)

// bdStatusNames maps bd 1.1.2 statuses to BARON canonicals that differ;
// matching statuses (open, blocked, closed) pass through unchanged.
var bdStatusNames = map[string]BeadStatus{
	bdInProgressStatus: BeadStatusWorking,
	"deferred":         BeadStatusCancelled,
	"pinned":           BeadStatusOpen,
	"hooked":           BeadStatusWorking,
}

// baronStatusNames maps BARON states to the status bd accepts on write.
var baronStatusNames = map[BeadStatus]string{
	BeadStatusAssigned:   "open",
	BeadStatusWorking:    bdInProgressStatus,
	BeadStatusValidating: bdInProgressStatus,
	BeadStatusRetry:      bdInProgressStatus,
	BeadStatusMergable:   bdInProgressStatus,
	BeadStatusHumanQueue: "open",
	BeadStatusMerged:     "closed",
	BeadStatusCancelled:  "closed",
}

// normalizeStatus maps a status read from bd to the canonical BARON state.
// Unknown values pass through unchanged so future bd statuses stay visible.
func normalizeStatus(s BeadStatus) BeadStatus {
	if tr, ok := bdStatusNames[string(s)]; ok {
		return tr
	}
	return s
}

// bdStatus maps a canonical BARON state to the status bd accepts.
func bdStatus(s BeadStatus) string {
	if en, ok := baronStatusNames[s]; ok {
		return en
	}
	return string(s)
}

// Bead represents a work item.
type Bead struct {
	ID                 string     `json:"id"`
	BRN                domain.BRN `json:"brn"`
	Title              string     `json:"title"`
	Description        string     `json:"description,omitempty"`
	AcceptanceCriteria string     `json:"acceptance_criteria,omitempty"`
	Priority           Priority   `json:"priority,omitempty"`
	Status             BeadStatus `json:"status"`
	IssueType          string     `json:"issue_type,omitempty"` // "epic", "task", "bug", ...
	Assignee           string     `json:"assignee,omitempty"`
	Tags               []string   `json:"tags,omitempty"`
	DependencyType     string     `json:"dependency_type,omitempty"` // set by bd dep list
	// Parent is the parent bead's BRN when this bead is a hierarchical child
	// (bd keeps both a dotted id and a parent field). Dependencies lists the
	// beads this one depends on, each tagged with its link type (blocks,
	// related, parent-child, discovered-from) — a blocks link means the
	// dependency is blocking this bead.
	Parent       string    `json:"parent,omitempty"`
	Dependencies []DepLink `json:"dependencies,omitempty"`
	Comments     []Comment `json:"comments,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// Metadata is bd's free-form custom key/value store (bd update
	// --set-metadata k=v), read back verbatim from bd show/list --json.
	// BARON uses two keys: "model" (the model the assigned agent should
	// run, e.g. "opus" or "opencode-go/deepseek-v4-flash") and "effort"
	// (reasoning-effort level, e.g. "high") — kept separate from Assignee
	// (which agent CLI, e.g. "claude") rather than crammed into one
	// compound string, so the agent, the model and the effort are each
	// their own field. See Model/Effort below.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Model is the model the assigned agent should run (metadata key "model"),
// e.g. "opus" for claude or "opencode-go/deepseek-v4-flash" for opencode.
// "" means the agent's own default model.
//
// The legacy "llm" key is still read so beads assigned before the key was
// renamed keep running the model they were assigned; nothing writes it any
// more (see SetModelMetadata, which clears it).
func (b Bead) Model() string {
	if m := b.Metadata["model"]; m != "" {
		return m
	}
	return b.Metadata["llm"]
}

// Effort is the assigned agent's reasoning-effort level (metadata key
// "effort"), e.g. "high". "" means the agent's own default effort.
func (b Bead) Effort() string { return b.Metadata["effort"] }

// Tier is the capability class this bead requires (metadata key "tier"),
// e.g. "standard" — set at creation and mandatory, unlike Model/Effort.
// Assignee holds the real agent CLI name once resolved (or "" before
// then); the tier request itself lives here, separately, so the
// reconciler's resolver (reconcileTierAssignments) still knows what a
// bead needs even after Assignee and Model have been filled in — and so a
// 429/rate-limit reassignment (reassignOnRateLimit) always knows which
// tier to stay within, without having to reverse-engineer it from
// whatever model happened to get picked.
//
// Missing or unrecognized metadata (a bead created via bare `bd`, bypassing
// baron's mandatory tier field, or a stale/typo'd value) defaults to
// agent.TierFast — the cheapest/quickest tier — rather than returning "".
// This is a real default, not just a display fallback: reconcileTierAssignments
// used to special-case tier == "" as "nothing for the resolver to act on,"
// leaving such a bead unassigned forever; defaulting here means that branch
// is now unreachable and those beads resolve like any other fast-tier bead.
func (b Bead) Tier() agent.Tier {
	t := agent.Tier(b.Metadata["tier"])
	if !agent.IsTier(t) {
		return agent.TierFast
	}
	return t
}

// DepLink is one dependency edge from a bead to another (bd dep add).
type DepLink struct {
	IssueID     string `json:"issue_id,omitempty"`
	DependsOnID string `json:"depends_on_id,omitempty"`
	Type        string `json:"type,omitempty"` // blocks, related, parent-child, discovered-from
}

// Comment is a comment on a bead, as reported by bd comments --json.
type Comment struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// DomainState maps a bead to its domain.BeadState for transition validation.
// bd has no "assigned" status of its own — `bd assign` only sets the assignee
// field, leaving status at "open" — so a bead with an assignee but status
// "open" is domain-assigned even though its status column still reads "open".
func (b Bead) DomainState() domain.BeadState {
	if b.Status == BeadStatusOpen && b.Assignee != "" {
		return domain.BeadStateAssigned
	}
	return domain.BeadState(b.Status)
}

// Runner executes subprocess commands with tool.Run semantics. tool.Run
// satisfies it via RunnerFunc.
type Runner interface {
	Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error)
}

// RunnerFunc adapts a function to Runner, e.g. RunnerFunc(tool.Run).
type RunnerFunc func(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	return f(ctx, name, args, opts)
}

// BeadStore manages beads via the bd CLI.
type BeadStore struct {
	runner Runner
	dir    string // project directory
	// brnPrefix is the configurable BRN prefix (general.brn_prefix, default
	// empty). Empty yields bare bd issue IDs as BRNs.
	brnPrefix string
	// envelope is set once bd 2.0+ envelope output is observed; later calls
	// request BD_JSON_ENVELOPE=1 so bd emits the versioned envelope directly.
	envelope atomic.Bool
	// customOnce guards the one-time registration of BARON's custom statuses.
	customOnce sync.Once
	// customErr carries the ensureCustomStatuses outcome.
	customErr error
}

// NewBeadStore creates a store that runs bd in the given directory.
func NewBeadStore(runner Runner, dir string) *BeadStore {
	return &BeadStore{runner: runner, dir: dir}
}

// SetBRNPrefix sets the prefix used when deriving BRNs from bd issue IDs.
func (s *BeadStore) SetBRNPrefix(prefix string) {
	s.brnPrefix = prefix
}

// List returns all beads, including closed ones. Uses bd list --all --json
// so baron's client-side filters (status, TUI dashboard) see the full set.
