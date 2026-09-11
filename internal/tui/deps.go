package tui

import (
	"context"
	"time"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/hunk"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

// Deps is every external dependency the TUI reads/writes through — stores,
// composed action callbacks, and process/host handles, supplied by the CLI
// package (which constructs them from the same app used for every CLI
// command) — never touched directly, so tests can substitute fakes/stubs
// field by field (a nil callback is a documented no-op at each call site,
// not a panic).
type Deps struct {
	Beads      *store.BeadStore
	Runs       *store.RunStore
	Audit      *store.AuditStore
	LoadConfig func(dir string) (*store.Config, error)
	Dir        string
	// Models returns the selectable agent names from BARON's model
	// registry (e.g. "claude", "codex", "opencode" — the doctor-probed
	// active entries plus any [[models]] config overrides;
	// For the assign-model picker. Nil -> picker shows a hint to run
	// the assign command manually.
	Models func() ([]string, error)
	// EffortChoices returns the valid reasoning-effort levels for a
	// picker item Models returned (a bare agent name like "claude" or an
	// "agent/llm" pair like "opencode-go/deepseek-v4-flash"), most-common
	// last for a sane list default; nil/empty means that choice has no
	// selectable effort (the assign proceeds straight through, same as
	// today). Nil -> the effort step never appears.
	EffortChoices func(picked string) ([]string, error)
	NoColor       bool
	Theme         string
	Version       string
	// BaseBranch is the branch merges target (general.base_branch), used to
	// compute a bead's branch name for the Overview tab's inline "ready to
	// merge" line and as the diff target for a bead's own Diff tab.
	BaseBranch string
	// Worktrees backs the embedded Agent-tab terminal: pressing 'r' creates
	// (or reuses) the bead's worktree and spawns the agent inside it. Nil ->
	// the terminal falls back to the main checkout (deps.Dir).
	Worktrees *domain.WorktreeManager
	// ReadRunSummary returns a bead's persisted one-shot run summary (the
	// `baron run` stdout: agent echo + gate report), nil with no error when
	// none exists yet. Restoring it after a restart keeps the gate result
	// visible even though neither the tmux pane nor the agent log ever
	// contains the gate text. Nil -> summaries are never restored.
	ReadRunSummary func(brn string) ([]string, error)
	// WriteRunSummary persists a bead's run summary for the next session.
	// Nil -> summaries are never persisted.
	WriteRunSummary func(brn string, lines []string) error
	// HeaderStats returns the project metrics for the right-hand header bar:
	// git branch/changes, diff counts, tmux pane counts, and short path. brn
	// scopes branch/changes/diff to that bead's own worktree when it has one
	// (falling back to the main checkout otherwise); "" always means the main
	// checkout. The TUI re-fetches it every headerStatsInterval (10s) while
	// running, and immediately whenever the selected bead changes.
	HeaderStats func(brn string) (HeaderStats, error)
	// AgentHost hosts each bead's agent process independently of BARON's
	// own lifetime (see the AgentHost interface in terminal.go) — today a
	// tmux window in a shared session, wired up whenever config tui.tmux
	// != "never" and tmux is installed. Nil falls back to a bare pty with
	// no persistence across a BARON restart.
	AgentHost AgentHost
	// HunkComments returns the review comments on the live Hunk session for a
	// worktree. Wired only when the hunk CLI is installed; nil disables the
	// hunk->bead comment path entirely. A func rather than a client, so the
	// TUI never owns the subprocess plumbing — the same shape as every other
	// external tool it reaches through Deps.
	HunkComments func(repo string) ([]hunk.Comment, error)
	// Reconcile scans every non-terminal bead for drift between its
	// recorded status and observable reality (a dead agent process, a
	// stalled gate, dependencies that cleared) and lands each one
	// somewhere safe within the state machine — see
	// internal/cli/reconcile.go for the full per-state policy. skipBRNs
	// excludes beads the caller already knows are live; the TUI passes its
	// own m.sessions/m.spawning plus every "working" bead, since that
	// state's reconciliation (including reattaching a live-but-unattached
	// view) is fully owned by reconcileWorkingCmd instead — Reconcile has
	// no notion of TUI panes to reattach into. Nil -> no periodic
	// reconciliation runs (only the working-specific check above still
	// does).
	Reconcile func(ctx context.Context, skipBRNs map[string]bool) (ReconcileSummary, error)
	// CreateBead opens a new bead — the typed equivalent of what used to be
	// built as `["work","create",...]`-style argv and sent through
	// RunCommand's cobra dispatch (see internal/cli/work.go's CreateBead).
	// Returns "created <brn>: <title>", the same line the CLI command used
	// to print — createdBRN's parsing (for landing the dashboard on the new
	// bead's tab) needs no change beyond who's called.
	CreateBead func(title, description, accept, priority, issueType, parent, tier string) (string, error)
	// ChangeStatus, Assign, CloseBead, Comment are the typed equivalents of
	// what used to be built as `["work","status",...]`-style argv and sent
	// through RunCommand's cobra dispatch — see internal/cli/work_status.go
	// (ChangeStatus), work_mutate.go (AssignBead/CloseBead/AddComment). Each
	// returns the same one-line confirmation text the CLI command used to
	// print, so the existing runAction/runCommand-driven message flow
	// (actionDoneMsg/commandRanMsg) needs no change beyond who's called.
	ChangeStatus func(brn, target string) (string, error)
	// Assign assigns brn to a model: callers (assignCmd) always pass
	// agentName empty and the picker's chosen catalog ID whole as modelID,
	// letting AssignBead's own catalog-based resolveAssignment figure out
	// which agent CLI actually runs it — that mapping isn't recoverable
	// from the ID string alone (see AssignBead's doc comment), so the TUI
	// must never try to split/guess it itself. Confirmation-free: the TUI's
	// own picker interaction already is the human's approval, same as the
	// old string-arg path relied on RunCommand's forced non-TTY
	// auto-confirm for.
	Assign func(brn, agentName, modelID, effort string) (string, error)
	// CloseBead closes brn, confirmation-free for the same reason Assign is
	// — whatever screen triggers this (the detail view's 'c', the merge
	// flow) is itself the confirmation.
	CloseBead func(brn string) (string, error)
	// Comment adds text as a comment on brn.
	Comment func(brn, text string) (string, error)
	// EditBead updates brn's title/description in place — see
	// internal/cli/work_mutate.go's EditBead. The backend has supported this
	// since before the TUI existed (`bd update`/`baron work edit`); until
	// this field the TUI itself had no path to it at all, so a typo made at
	// 'n' (new bead) had no in-app fix short of dropping to a shell.
	EditBead func(brn, title, description string) (string, error)
	// Merge merges brn's branch locally the moment 'm' is pressed on a
	// mergable bead — no confirmation screen, the same way Assign/CloseBead
	// are confirmation-free: runMerge (internal/cli/merge.go) re-runs its
	// own authoritative preflight/config checks before touching anything,
	// so there is nothing left for the TUI to gather approval on first. A
	// conflict or forbidden-config failure surfaces as a toast (see
	// commandRanMsg) with instructions to resolve and retry. Unlike
	// ChangeStatus/Assign/CloseBead/Comment, the underlying pipeline still
	// prints internally; the returned string is whatever it wrote, captured
	// rather than left to leak into the TUI's own rendering.
	Merge func(brn string) (string, error)
	// Resume runs the post-agent pipeline for a bead whose agent already
	// ran outside baron's own launch loop (the TUI's embedded terminal
	// exiting) — see internal/cli's ResumeRun. Also still prints
	// internally; same capture story as Merge.
	Resume func(brn string) (string, error)
	// IdleResume is Resume's idle-triggered sibling — the same ask-check/
	// validate/gate pipeline, but fired by checkIdleAgents (terminal.go)
	// noticing a bead-run session has gone quiet at its own input prompt
	// rather than by the process actually exiting. Unlike Resume, a bead
	// found idle with no diff and no new commits at all doesn't stay
	// "working" silently — it lands in human_queue, since nothing having
	// happened is exactly the case a human needs to notice (an agent that
	// wandered off without doing the work). Nil disables idle detection
	// outright, same as IdleThreshold<=0.
	IdleResume func(brn string) (string, error)
	// IdleThreshold is how long a bead-run session's pty must produce no
	// new bytes at all (lastActivity) before checkIdleAgents treats it as
	// a candidate — sourced once at wiring time from
	// general.silent_death_threshold (internal/cli/tui.go), the same
	// config value the headless silent-death watchdog already uses, on
	// purpose: a quiet test suite can legitimately run for many minutes,
	// so this is deliberately not a short value. <=0 disables idle
	// detection outright, same as IdleResume==nil.
	IdleThreshold time.Duration
	// Personas returns Prompt Mode's current persona set (P key — see
	// docs/PRD/crew-mode.md). Nil -> the Personas tab shows a hint instead
	// of a persona list.
	Personas func() ([]persona.Persona, error)
	// UpdatePersona edits a persona's prompt, description, cron schedule,
	// event/state-machine-transition triggers (f.Events, in
	// persona.ParseTransitionRules' "from->to, from->to" text form — "*"
	// for any state), Skills (f.Skills, in persona.ParseSkills' comma-separated
	// text form — npm package specs, catalog skill names, or plugin URLs),
	// and enabled flag (Prompt Mode's 'e' edit form) — content/metadata
	// only, never Model/Source/IssueTypes. f.ID names which persona to
	// update. Where the write actually lands (a fresh user-layer fork, an
	// existing one, or just a project-local enabled toggle) is
	// internal/cli.UpdatePersonaByID's own decision — see its doc comment.
	// Nil -> 'e' is a no-op.
	UpdatePersona func(f persona.FormFields) error
	// DeletePersona removes a user-layer persona (a real file under
	// ~/.config/baron/personas) — Prompt Mode's Personas-tab delete key.
	// Errors for a repo/embedded built-in with no local file to remove
	// (disable it instead). Nil -> the delete key is a no-op.
	DeletePersona func(id string) error
	// CreatePersona creates a brand-new persona — Prompt Mode's Personas-tab
	// 'n' (internal/cli's CreatePersonaByFields). f.ID is the form's own ID
	// field verbatim ("leave blank to auto-generate"): blank derives the id
	// from f.Name, non-blank pins the user's own typed text, both run through
	// the same slug/collision-suffix resolution on the backend side (see
	// CreatePersonaByFields). Nil -> 'n' on the Personas tab is a no-op.
	CreatePersona func(f persona.FormFields) (string, error)
	// FireNow manually runs persona id right now, against its own configured
	// IssueTypes scope, ignoring Schedule timing/debounce — the Personas
	// tab's 'r' key. Nil -> no-op.
	FireNow func(id string) error
	// PersonaActivity returns persona id's recent audit trail — every bead
	// it has been pointed at and why, newest first (internal/cli's
	// PersonaActivity) — Prompt Mode's worklist, and the direct answer to
	// "where are the beads while looking at a persona": right here, as
	// what this persona has done to the board. Nil -> the worklist shows a
	// hint instead.
	PersonaActivity func(id string) ([]store.AuditEvent, error)
	// PersonaOutput returns a snapshot of persona id's own tmux pane (its
	// "persona-<id>" window, the same one launchPersona/FireNow spawn) —
	// the accordion's "what did it actually say/do" complement to
	// PersonaActivity's "which beads, and why" audit trail. "" with a nil
	// error means the persona has never run, or its window has since
	// closed — shown as a hint, not an error. Nil -> the accordion shows
	// only the audit trail, no output section.
	PersonaOutput func(id string) (string, error)
	// PersonaStatuses reports every listed persona's live run state in one
	// bulk fetch (internal/cli's PersonaStatuses) — Prompt Mode's roster
	// rows are status-first (running/idle/off + elapsed), not
	// configuration-first; this is what makes that possible. Fetched once
	// on entry, never continuously polled. A missing key means "not
	// running" (idle/off) just as much as an explicit false — the map is
	// sparse by construction (PersonaStatuses only populates running
	// entries). Nil -> every row renders idle/off.
	PersonaStatuses func(ids []string) (map[string]PersonaStatus, error)
	// PromptAgentIDs returns the active agent CLI names for Prompt Mode's
	// left-pane tabs (e.g. "claude", "opencode" — whichever CLIs the
	// machine's agent registry probed as active). Nil/empty -> the left
	// pane shows a "no active agent CLI" hint instead of tabs.
	PromptAgentIDs func() ([]string, error)
	// PromptAgentEnsure starts (or confirms already-running) agentID's
	// interactive, bd-scoped chat session in the main checkout at its
	// current branch — never a bead worktree, never task-agent BEADS_DB
	// isolation (that would silently break the whole point of this pane:
	// a human editing beads through it). Returns the tmux window name to
	// pass to Deps.AgentHost.AttachArgv for the actual pty attach — this
	// deliberately does NOT go through AgentHost.EnsureRunning, whose
	// concrete implementation (internal/cli's tmuxAgentHost) unconditionally
	// wraps argv in task-agent env. Idempotent: safe to call every time a
	// left-pane tab becomes visible. Nil -> the left pane shows a "not
	// wired" hint instead of tabs.
	PromptAgentEnsure func(agentID string) (window string, started bool, err error)
}

// PersonaStatus is one persona's live run state, as reported by
// Deps.PersonaStatuses — Running is a real tmux-checked fact, Since is
// BARON's own best guess at when that run started (the zero time when
// Running is false).
