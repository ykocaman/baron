package tui

import (
	"time"

	"charm.land/bubbles/v2/viewport"

	"github.com/baron-cli/baron/internal/domain"
)

// screen identifies which of the TUI's views is active.
type screen int

const (
	screenDashboard screen = iota
	screenForm
	screenHelp
	// screenCrew is Prompt Mode (`p`/`P` from Board Mode) —
	// docs/PRD/crew-mode.md's v6 redesign: a full-height mode SWAP, not an
	// overlay or a drawer — Board Mode's own m.width/m.height are never
	// touched, so switching costs zero terminal resize (no SIGWINCH to any
	// live agent pty). Left: the persona roster, status-first (running/
	// idle/off). Right, stacked: the selected persona's worklist (which
	// beads it has touched) over that worklist bead's thread (title +
	// description + full comment history — no tabs, no Terminal/Diff,
	// deliberately lighter than Board Mode's own detail pane, which is
	// "check status"; this one is "read/manage content"). 'o' hands a
	// worklist bead off to Board Mode when the human wants the operational
	// view (terminal/diff/gate) back.
	screenCrew
)

// promptPane is which of Prompt Mode's three panes 'tab' has last switched
// to — see updatePromptKey. v7 (docs/PRD/crew-mode.md): left is a live
// tmux-attached agent-CLI chat, right-top is a list (Personas or Beads,
// see promptRightTab), right-bottom is that list selection's detail.
type promptPane int

const (
	promptPaneLeft promptPane = iota
	promptPaneRightTop
	promptPaneRightBottom
)

// promptRightTab is which of Prompt Mode's two switchable right-side tabs
// governs both right-top and right-bottom at once — switched with
// shift+left/shift+right regardless of which pane has focus.
type promptRightTab int

const (
	promptTabBeads promptRightTab = iota
	promptTabPersonas
)

type formKind int

const (
	formKindNewBead formKind = iota
	formKindComment
	formKindEditBead
	// formKindPersonaEdit is Prompt Mode's 'e' edit form (description,
	// interval trigger minutes, enabled) — see Deps.UpdatePersona.
	formKindPersonaEdit
	// formKindPersonaNew is Prompt Mode's Personas-tab 'n' — creates a
	// brand-new persona (id/name/prompt plus the same description/
	// schedule/events/skills/enabled fields formKindPersonaEdit already
	// owns) via Deps.CreatePersona.
	formKindPersonaNew
	// formKindPromptNewBead is Prompt Mode's Beads-tab 'n' — deliberately
	// NOT formKindNewBead: that case arms m.pendingNewBead, which jumps
	// Board Mode's list cursor the next time screenDashboard is visible.
	// Prompt Mode has its own list/cursor and must not silently arm a
	// Board Mode side effect that has nothing to do with it.
	formKindPromptNewBead
)

// PersonaStatus is one persona's live run state, as reported by
// Deps.PersonaStatuses — Running is a real, tmux-checked fact, Since is
// the zero time when Running is false.
type PersonaStatus struct {
	Running bool
	Since   time.Time
}

// ReconcileSummary is Deps.Reconcile's result: what it changed (or, for the
// off-by-default retry case, what it found needing a manual relaunch).
type ReconcileSummary struct {
	Actions []ReconcileActionSummary
}

// ReconcileActionSummary is one bead Reconcile looked at. From == To means
// nothing was changed but the bead needs attention (e.g. a "retry" bead
// with gate.auto_retry disabled) rather than a real transition.
type ReconcileActionSummary struct {
	BRN    domain.BRN
	From   domain.BeadState
	To     domain.BeadState
	Reason string
}

// HeaderStats holds the metrics for the top-right header bar.
type HeaderStats struct {
	Branch         string
	ChangedFiles   int
	DiffInserted   int
	DiffDeleted    int
	DeletedFiles   int
	UntrackedFiles int
	// Agents is the total number of BARON-launched agent terminals running
	// in tmux (deduped across the grouped baron-view-* viewer sessions).
	Agents    int
	ShortPath string
}

// mergeTarget is a bd bead's branch ready for a local `git merge` — shown
// inline in the Overview tab's "Ready to Merge" section (see
// mergeTargetForBead). Branch/Base are always derivable synchronously from
// the bead and the configured base branch, so there is nothing to fetch
// before showing it.
type mergeTarget struct {
	BRN    string
	Title  string
	Branch string
	Base   string
}

// Model is the root Bubble Tea model.

type agentPane struct {
	summary []string
	// offset is how far back a session's pane is scrolled, in lines, 0 being
	// the live view. It indexes the host-captured pane history (see
	// paneHistoryCmd), not the emulator: a session's backlog lives in tmux or
	// inside the child, never in BARON.
	offset int
	// vp is the persisted-summary fallback's own scroll state (no live
	// session — see syncPaneContent), independent of offset above since
	// the two never apply to the same render at once (viewSessionPane
	// picks one or the other based on whether a session is live).
	vp     viewport.Model
	replay bool
	done   bool
	err    string
}

// panesFor returns the scroll/summary-pane registry for kind — m.panes (the
// Agent tab) or m.diffPanes (the Diff tab) — the panes counterpart of
// terminal_sessions.go's sessionsFor.
