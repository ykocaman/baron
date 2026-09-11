// Package tui implements BARON's terminal UI. It shares the exact
// command/domain layer the CLI uses: list/detail screens call the same
// store/provider methods CLI commands call, and the ":" command bar runs the
// identical cobra command tree via Deps.RunCommand — so every TUI action
// produces the same state change its CLI equivalent would.
package tui

import (
	"context"
	"regexp"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

// Model is the TUI's whole application state — Bubble Tea's single state
// value, threaded through Update (update.go) and View (view.go) by value,
// constructed once by New (model_init.go).
type Model struct {
	deps   Deps
	styles styles
	ctx    context.Context

	screen     screen
	prevScreen screen
	// formReturnScreen is screenForm's own "return to" target, set by
	// whichever key opened the form and read back by updateFormKey/
	// submitForm to restore m.screen once it closes. Deliberately not
	// prevScreen: that field is screenCrew's (togglePromptMode) and
	// screenHelp's ("?") own toggle bookkeeping — a form opened while
	// already inside Prompt Mode (any of the Beads/Personas-tab 'n'/'e'/'c'
	// keys) needs to return to screenCrew too, and every one of those
	// call sites used to write that into prevScreen directly, silently
	// overwriting the screenDashboard (or whatever) togglePromptMode had
	// stashed there to come back to on the next 'P'. Caught for real via
	// TestTUIPromptModeBeadDelivery: creating a bead from Prompt Mode's own
	// 'n' left 'P' a no-op afterward (prevScreen now read back as screenCrew,
	// its own current value), stranding every following keystroke against
	// Prompt Mode's key bindings instead of the dashboard's.
	formReturnScreen screen
	width            int
	height           int
	quitting         bool
	// tickScheduled guards the embedded-terminal frame tick to exactly one
	// self-sustaining chain. tea.Tick has no identity and no cancel: every
	// call arms an independent timer, and since the tickMsg handler re-arms
	// one tick per tickMsg it receives, a second chain is permanent and
	// doubles the frame rate for the rest of the process. termTick() is
	// called from afterSelect and tabCmd — i.e. on every bead navigation and
	// every tab switch — so without this guard the tick rate grew with the
	// number of keys the user had pressed: 20 navigations plus 10 tab
	// switches measured 34 concurrent chains (≈800 frames/sec), each one
	// running a full View() and (before the pump-side sentinel scan) a
	// whole-screen render per live session. That is the TUI freeze where
	// navigating between beads was itself the thing that caused it.
	// See TestFrameTickStaysSingleChainAcrossNavigation.
	tickScheduled bool
	// headerStatsTickScheduled is tickScheduled's twin for the header-stats
	// refresh chain (git status/diff, tmux list-panes, bd list — headerStatsTick).
	// headerStatsMsg re-arms it (update.go), but headerStatsMsg is produced by
	// every loadHeaderStats call, not just the tick's own: afterSelect fires
	// one on every bead navigation and Init fires one at startup, so without
	// this guard each of those independently started its own permanent
	// self-sustaining chain — the identical bug tickScheduled exists to
	// prevent for termTick, just on a 10s period instead of the frame rate.
	headerStatsTickScheduled bool
	// spawning marks a (bead, kind) whose session is being started but has not
	// registered yet. Spawning is asynchronous — the session only lands in
	// sessions/diffSessions when its agentSpawnedMsg arrives — so the
	// "already running?" check that guards embeddedSpawnCmd is blind for the
	// whole startup window. Switching tabs faster than that (the Diff tab
	// auto-starts hunk on open, so holding tab does it) launched a fresh
	// process every time: three concurrent hunk processes for a single bead
	// were found this way, each with its own daemon session.
	spawning map[string]bool
	// hunkSeen holds the ids of Hunk review comments already ingested (turned
	// into a bead comment and delivered to the agent), so reopening the Diff
	// tab or polling again never re-delivers one. Process-lifetime only: a
	// restart re-ingesting is bounded by the session-start cutoff in
	// pollHunkCommentsCmd, since a fresh process also starts a fresh session.
	hunkSeen map[string]bool
	// lastHunkPoll rate-limits that polling. It rides the existing frame tick
	// rather than arming a timer of its own — a second self-re-arming timer is
	// exactly the mistake tickScheduled exists to prevent.
	lastHunkPoll time.Time
	// hunkPollInFlight guards against the same overlap lastHunkPoll alone
	// can't prevent: it's stamped at dispatch time, and the hunk CLI call it
	// gates can outlast hunkPollInterval under load, letting the next frame
	// tick dispatch a second, concurrent poll on top of the first (see
	// reconcileInFlight's doc comment — same shape of bug, same fix).
	hunkPollInFlight bool
	confirming       string // non-empty: showing a yes/no confirm for this action (legacy, kept for tests)
	confirmAction    string // the action that triggered the confirm (quit, stop, merge)
	confirmResult    *bool  // pointer to capture confirm result
	err              error
	statusMsg        string
	// statusGen increments on every notify/notifyErr call; a pending
	// statusDismissCmd only clears the message when its own generation
	// still matches, so an older notice's timer can never cut a newer one
	// short (see notify).
	statusGen int

	beads []store.Bead

	// listTab/listCursor/listScroll drive the split-pane's left list.
	listTab    int
	listCursor int
	listScroll int
	// listSubTab narrows the active tab to one status bucket at a time
	// (boardColumns[listTab].statuses[listSubTab]) — -1 means "show every
	// bucket", what h/l/[/]/1-4 reset to. left/right step through buckets
	// one at a time (see subTabNext/subTabPrev), spilling into the
	// next/previous tab's first/last bucket once the current tab's are
	// exhausted, instead of jumping straight to a tab like h/l do.
	listSubTab int
	// subTabLanded guards the one-time auto-select of a starting sub-tab on
	// the first beadsLoadedMsg (see the beadsLoadedMsg case in update.go):
	// after that, -1 is a deliberate user choice (h/l/1-4 reset to it) and
	// must survive reloads instead of being clobbered back onto whichever
	// bucket happens to be first.
	subTabLanded bool

	detail store.Bead

	humanQueue []store.Bead

	runs []store.Run

	comments []store.Comment
	// commentsLoading is true from the moment a bead is selected until its
	// commentsLoadedMsg lands. Comments.Comments shells out to `bd comments`
	// (a fresh subprocess per call, unlike the bead list which is already in
	// memory), so it visibly trails the rest of the detail pane — without
	// this the pane would keep showing the PREVIOUS bead's comments during
	// that gap, reading as stale/wrong rather than as "still loading".
	commentsLoading bool

	// auditEvents holds the bead's check/gate/run history (audit log), shown
	// on the Audit tab. commentsExpanded toggles the Overview tab's comment
	// list between collapsed (last few) and full.
	auditEvents      []store.AuditEvent
	commentsExpanded bool

	cmdMode  bool
	cmdInput textinput.Model

	// Prompt Mode (`p`/`P`, screenCrew) — docs/PRD/crew-mode.md's v7
	// redesign: a 3-pane screen for editing beads and personas, either by
	// chatting with a live agent-CLI session (left) or by direct manual
	// intervention (right). promptFocus is which of the three panes 'tab'
	// last switched to; promptRightTab is which of the two right-side tabs
	// (Personas/Beads) governs both right-top and right-bottom at once —
	// switched with shift+left/shift+right regardless of focus.
	promptFocus    promptPane
	promptRightTab promptRightTab

	// --- Left pane: one live interactive agent-CLI session per active
	// agent, each its own tmux-attached pty. A registry fully separate from
	// m.sessions/m.diffSessions — never touches termKind/currentKind/
	// tabIndexFor, which stay exactly as Board Mode's Agent/Diff tabs left
	// them. promptAgentIDs is fetched via Deps.PromptAgentIDs on Prompt
	// Mode entry; promptAgentTab indexes it. Sessions spawn lazily (first
	// visit to that agent's tab), mirroring Board Mode's own lazy-spawn
	// pattern for its Agent tab.
	promptAgentIDs         []string
	promptAgentTab         int
	promptSessions         map[string]*agentTerminal // keyed by agent ID, not brn
	promptTermFocus        bool
	promptTermScrollOffset int // scroll position for left pane terminal (0 = live view)
	// promptTermTickScheduled guards a chain fully separate from
	// tickScheduled (see that field's doc comment for why a second chain
	// sharing an existing tick's identity is catastrophic) — this one
	// drives only the left pane's live sessions while Prompt Mode is open.
	promptTermTickScheduled bool

	// --- Beads tab: right-top is a compact flat list over m.beads with its
	// own cursor/scroll (never Board Mode's listCursor/listTab/listSubTab);
	// promptBeadTab filters beads by category (-1: All, 0: Backlog, 1: Active,
	// 2: Needs You, 3: Done);
	// right-bottom reuses detailContent(w)'s exact Overview rendering on a
	// value-copy of m with detailTab forced to 0, fed by the SAME m.detail/
	// m.comments fields Board Mode's own detail pane uses (see
	// boardDetailVisible) — a second, list-only renderer over shared data,
	// not a second data path.
	promptBeadCursor int
	promptBeadScroll int
	promptBeadTab    int
	promptBeadSubTab int

	// --- Personas tab: right-top is a status-first glyph+name list (same
	// personas/personaStatuses data Deps.Personas/Deps.PersonaStatuses
	// already provide) with its own cursor/scroll. promptPersonaOpenID is
	// "" when no row's work-outputs accordion is open, else the one
	// persona ID currently expanded — at most one at a time: opening a
	// second closes the first, and pressing 'l' again on the open one
	// closes it. promptPersonaActivity caches Deps.PersonaActivity
	// (which beads, and why) and promptPersonaOutput caches
	// Deps.PersonaOutput (a snapshot of the persona's own raw tmux pane —
	// what it actually said/did) per persona ID, both fetched lazily the
	// moment that persona's accordion first opens, not eagerly for the
	// whole roster on entry.
	personas                  []persona.Persona
	personaStatuses           map[string]PersonaStatus
	promptPersonaCursor       int
	promptPersonaScroll       int
	promptPersonaDetailScroll int
	promptPersonaOpenID       string
	promptPersonaActivity     map[string][]store.AuditEvent
	promptPersonaOutput       map[string]string

	// quickPromptMode/quickPromptInput back Board Mode's lowercase 'p' (v8,
	// docs/PRD/crew-mode.md §2.9) — a one-line box anchored to m.detail's
	// selected bead whose submitted text is typed straight into whichever
	// agent-CLI tab is currently active in Prompt Mode's left pane
	// (quickPromptCmd/agentTerminal.steer), not dispatched as a persona run
	// — the left pane's live chat is now the single "manual prompt" surface,
	// superseding the old Developer-persona dispatch this key used to open.
	// esc cancels back to whichever screen was active without leaving it.
	quickPromptMode  bool
	quickPromptInput textinput.Model
	quickPromptBRN   string

	// search (/ key): filters the active list at render time.
	searchMode  bool
	searchInput textinput.Model
	searchQuery string
	// searchRegex is the compiled form of searchQuery when it is a valid
	// regular expression; nil means fall back to case-insensitive substring.
	searchRegex *regexp.Regexp

	// form screen state (screenForm): a huh/v2 Form, same convention as
	// statusForm/modelForm/effortForm. formKind selects which one is open
	// and how submitForm reads the bound result pointers below.
	//
	// The results are *string, each pointing at its own local variable
	// captured when the form is built (see the "n"/"c" keys in
	// updateSplitKey) — never Value(&m.someField) directly. m is a value
	// receiver reassigned throughout Update (every "m = asModel(next)"), so
	// an address taken from a field on one copy of m goes stale the moment
	// a later copy replaces it; a form built that way would silently keep
	// writing into an orphaned struct nothing reads back. A local variable
	// escapes to its own independent heap slot the instant its address is
	// taken, so the pointer stored here stays valid across every
	// reassignment for the form's whole lifetime — same convention as
	// modelResult/effortResult/statusResult.
	formKind          formKind
	beadForm          *huh.Form
	formTitleResult   *string
	formTypeResult    *string
	formTierResult    *string
	formParentResult  *string
	formDescResult    *string
	formAcceptResult  *string
	formCommentResult *string
	// formPersonaScheduleResult/formPersonaEventsResult/
	// formPersonaSkillsResult/formPersonaEnabledResult back
	// formKindPersonaEdit (Prompt Mode's 'e' edit form) — Description reuses
	// formDescResult above, same as every other form kind does. Schedule is
	// the raw cron expression text (empty = no time-based trigger); Skills
	// is the raw comma-separated text persona.ParseSkills reads (empty = no
	// Skills attached). IssueTypes/MinIntervalMinutes stay JSON-file-only
	// for now, not editable from this form (crew-mode.md's deliberately
	// minimal first cut on trigger *scope*, as opposed to the triggers
	// themselves).
	//
	// Events used to be a single free-typed "from->to, from->to" string —
	// unselectable, unsearchable, and the exact arrow syntax
	// persona.ParseTransitionRules wants had to be typed from memory.
	// Caught by hand dogfooding: every seeded/default persona trigger
	// (internal/persona/defaults.go) uses {From: "*", To: X} exclusively,
	// so formPersonaEventsResult is now the selected subset of
	// personaEventPresetStates (a searchable/checkable huh.MultiSelect,
	// see openPersonaEditForm/openPersonaNewForm) — "fires when a bead
	// reaches X," combined back into that same arrow-syntax string at
	// submit (personaEventsText). formPersonaEventsCustom preserves any
	// rarer non-wildcard-From rule a persona picked up outside the TUI
	// (hand-edited JSON) so opening Edit and hitting submit can never
	// silently drop it — see splitPersonaEventRules.
	formPersonaScheduleResult  *string
	formPersonaEventsResult    *[]string
	formPersonaEventsCustom    []persona.TransitionRule
	formPersonaEventsCustomStr *string
	formPersonaSkillsResult    *string
	formPersonaTierResult      *string
	formPersonaAgentResult     *string
	formPersonaBDWriteResult   *string
	formPersonaBDActionsResult *[]string
	formPersonaEnabledResult   *string
	// editingPersonaID is which persona formKindPersonaEdit's submit
	// applies to — the form itself has no bead-shaped BRN to key off of
	// the way formKindEditBead uses m.detail. editingPersonaName is just
	// for the form's own title ("Edit Persona — Clean-code reviewer"), not
	// the raw ID every other roster/worklist row already avoids showing.
	editingPersonaID   string
	editingPersonaName string
	// formPersonaIDResult/formPersonaNameResult/formPersonaPromptResult back
	// formKindPersonaNew (Prompt Mode's Personas-tab 'n', the only persona
	// create path in the app) — the three fields formKindPersonaEdit has no
	// need for (an existing persona already has an ID/Name/Prompt).
	// Description/Schedule/Events/Skills/Enabled reuse the five
	// formPersona*Result pointers above verbatim. ID is pre-filled with a
	// slug auto-derived from Name (collision-suffixed against m.personas)
	// but stays editable, rather than requiring a hand-typed raw
	// filesystem/tmux-safe string.
	formPersonaIDResult     *string
	formPersonaNameResult   *string
	formPersonaPromptResult *string
	// pendingNewBead/pendingFocusBRN let a fresh work create land the
	// dashboard on the tab holding the new bead (see beadsLoadedMsg).
	pendingNewBead  bool
	pendingFocusBRN string

	// bead detail tab (see detailTabs in view.go) for Tab navigation
	detailTab int
	// detailVP scrolls the Overview/Audit tabs' content (detailContent) — a
	// single shared viewport since both tabs already reset to the top on
	// every switch (see syncDetailVP's callers), matching detailScroll's old
	// single-field semantics. Never touched by the Terminal/Diff tabs: a
	// live session's own scroll uses agentPane.offset (vt.Scrollback's
	// alt-screen limitation means there's rarely real scrollback there
	// regardless — see agentPane's doc comment), and a persisted summary
	// uses agentPane.vp instead.
	detailVP viewport.Model
	// helpScroll scrolls the help screen.
	helpScroll int

	// status menu (nil = closed)
	statusTargets []domain.BeadState // for dynamic options
	statusCursor  int                // legacy, kept for tests
	statusForm    *huh.Form
	statusResult  *domain.BeadState
	// modelChoices holds the assign-model picker items (real LLM names);
	// non-nil means the picker is open. It is the picker's full ranked order:
	// modelRecent + modelPopular + the rest of Deps.Models(), deduped.
	modelChoices []string
	modelCursor  int
	// modelRecent/modelPopular are the cache-ranked leading sections of
	// modelChoices, rendered with section headers while the search is empty.
	modelRecent  []string
	modelPopular []string
	// modelSearch filters modelChoices live as the user types in the picker.
	modelSearch string
	// pendingAssignBRN is the bead the open model picker targets.
	pendingAssignBRN string
	// modelLoading is true while the picker is open but Deps.Models is
	// still being fetched in the background (modelsLoadedMsg): the picker
	// renders a loading state until the list lands.
	modelLoading bool
	// modelForm is the huh/v2 Form driving the model picker overlay.
	// Non-nil when the model picker is open. modelResult is the bound
	// value receiving the selected model.
	modelForm   *huh.Form
	modelResult *string
	// modelOptions holds the dynamic huh options for the model picker,
	// populated by modelsLoadedMsg when Deps.Models returns.
	modelOptions []huh.Option[string]
	// effortChoices holds the follow-up effort-level picker's items
	// (Deps.EffortChoices for the just-picked model); non-nil means that
	// overlay is open, on top of the model picker's own selection.
	// pendingAssignModel is the model picker's already-chosen item,
	// held here while effort is picked (or skipped) before the actual
	// `work assign` command runs.
	effortChoices      []string
	effortCursor       int
	pendingAssignModel string
	// effortForm is the huh/v2 Form driving the effort picker overlay.
	// Non-nil when the effort overlay is open. effortResult is the bound
	// value receiving the selected effort level.
	effortForm   *huh.Form
	effortResult *string
	// runAfterAssign: picker assign success chains into an automatic run
	// (pressing 'r' on an unassigned bead opens the picker first).
	runAfterAssign string
	// pendingRunAfterAssign holds the BRN runAfterAssign resolved to once the
	// assign command itself succeeds, until the beadsLoadedMsg it triggered
	// reloads m.beads with the new assignee in it. embeddedSpawnCmd reads
	// m.beads (not a fresh store fetch — see its own doc comment), so
	// spawning it straight from commandRanMsg's handler would still see the
	// bead unassigned: m.beads is whatever the dashboard last loaded, from
	// before this assign happened, and the reload commandRanMsg itself
	// kicks off hasn't landed yet. Caught as a real bug, not a hypothetical:
	// spawnAgentTerminal's own "bead.Assignee == ''" guard fired right after
	// a successful assign, failing the run with "no agent assigned" even
	// though bd itself now shows the bead assigned.
	pendingRunAfterAssign string

	// huhForm holds an active huh/v2 Form being driven by the test harness
	// (and later by real key input). Nil when no form is active.
	huhForm *huh.Form

	// sessions holds the per-bead embedded agent terminals (the Agent tab);
	// diffSessions is the same thing for the Diff tab (hunk diff --watch).
	// A session is the child process + its pty + the vt emulator rendering
	// the child's screen; see terminal.go. Two separate maps rather than one
	// composite-keyed map so every existing agent-only call site (spawn,
	// reconcile, persisted-summary restore) keeps operating on exactly the
	// registry it always has — see sessionsFor/allSessions for the
	// kind-generic operations (resize, frame tick) that must span both.
	sessions     map[string]*agentTerminal
	diffSessions map[string]*agentTerminal
	// termFocus forwards the keyboard to the focused terminal (t toggles,
	// shift+esc gives it back — a bare esc forwards to the agent instead,
	// since esc-esc is opencode's own gesture to interrupt a running turn
	// and must never be intercepted); termZoom is the terminal's
	// fullscreen state (z). Both reflect whichever kind the active detail
	// tab hosts (see currentKind) — refreshed on every tab switch by
	// syncTermFocus.
	termFocus bool
	termZoom  bool
	// liveBRN is the bead whose agent output the Terminal tab shows; panes
	// holds per-BRN run summaries (restored from disk) and the
	// transcript-based fallback content for beads without a session.
	// diffPanes is the Diff tab's own scroll-offset state — kept separate
	// since a diff session has no persisted-summary concept (see
	// spawnDiffTerminal's doc comment).
	liveBRN   string
	panes     map[string]*agentPane
	diffPanes map[string]*agentPane
	// reconciled marks beads already checked once this process for the
	// "working with no in-process session" gap (see the beadsLoadedMsg
	// case's AgentHost.Alive check) — without this guard, every beads
	// reload would re-issue the same tmux liveness check for a bead
	// that's mid-reconcile or already resolved.
	reconciled map[string]bool
	// lastReconcile is when Deps.Reconcile last ran (zero value = never,
	// so the first beadsLoadedMsg after startup always triggers one).
	// Unlike reconciled above, this gates the whole pass by time, not by
	// bead: Reconcile shells out to git/tmux/bd per bead it looks at, and
	// beads reload far more often than once a minute (every mutation,
	// plus the 10s header-stats tick), so a plain "run on every load"
	// would hammer those subprocesses continuously.
	lastReconcile time.Time
	// reconcileInFlight is true from the moment a Reconcile pass is
	// dispatched until its reconcileMsg comes back. lastReconcile alone is
	// not enough to prevent overlap: it's stamped at dispatch time, not
	// completion, so a pass that runs longer than reconcileCooldown (every
	// bead's own git/tmux/bd calls, on a project with enough beads) would
	// otherwise let the next beadsLoadedMsg dispatch a second, fully
	// concurrent pass on top of the first — each shelling out to the same
	// subprocesses independently, compounding without bound the longer a
	// session runs. This flag is what actually prevents that.
	reconcileInFlight bool

	// headerStats backs the header's right-aligned project metrics
	// (see loadHeaderStats); headerStats.Branch == "" means unknown/unavailable and
	// the header omits that side entirely.
	headerStats HeaderStats

	// deleteConfirmBRN/deleteConfirmAt implement the double-press ctrl+D
	// delete gesture: the first press records the BRN and the timestamp;
	// the second press within 2 s confirms the close.
	deleteConfirmBRN string
	deleteConfirmAt  time.Time

	// deleteConfirmPersonaID is the Personas tab's own ctrl+D double-press
	// gesture, same shape as deleteConfirmBRN/deleteConfirmAt (reusing
	// deleteConfirmAt for the timestamp — the two gestures are never armed
	// at once, since they live on different tabs/screens) but a separate
	// field so a persona ID and a bead BRN can never collide in the same
	// string.
	deleteConfirmPersonaID string

	// shellOutput holds the lines from the last :cmd result (both baron CLI
	// output and raw bash commands) for the inline output panel that floats
	// above the footer. shellOutputScroll is the scroll position (0 = show
	// the last/newest lines). nil means the panel is hidden.
	shellOutput       []string
	shellOutputScroll int

	// shellHistory maintains previous shell commands for up/down navigation
	// in cmdMode. shellHistIdx tracks the current history position (-1 = new).
	shellHistory []string
	shellHistIdx int

	// autoOffBRNs holds the BRNs a human has explicitly opted out of
	// automatic reconciliation for via '+'/'-' (see updateKey) — presence
	// with true means "leave this bead alone," matching how skipBRNs
	// already suppresses every per-state Reconcile step for a bead with a
	// live TUI session (internal/cli/reconcile.go). Session-local only
	// (never persisted): a fresh TUI process starts every bead back under
	// general.auto_start's own default, same as autoOffBRNs never existing
	// at all. Absence (the zero value, nil map) means every bead still
	// auto-reconciles, unchanged from before this field existed.
	autoOffBRNs map[string]bool
}

// agentPane is one bead's run-summary state. summary is the one-shot
// `baron run` stdout restored from disk (WriteRunSummary); the fallback
// Agent-tab content for a bead without a session. done means the run
// finished: the summary is never overwritten.
