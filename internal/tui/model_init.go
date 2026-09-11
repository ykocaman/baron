package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

// New constructs the initial Model — styles, input widgets, and every
// zero-value field a screen expects populated before its first render.
func New(ctx context.Context, deps Deps) Model {
	st := newStyles(deps.Theme, deps.NoColor)

	ti := textinput.New()
	ti.Prompt = ":"
	ti.CharLimit = 500
	s := ti.Styles()
	s.Focused.Prompt = st.CmdPrompt
	s.Blurred.Prompt = st.CmdPrompt
	ti.SetStyles(s)

	si := textinput.New()
	si.Prompt = "/"
	si.CharLimit = 200
	s = si.Styles()
	s.Focused.Prompt = st.SearchPrompt
	s.Blurred.Prompt = st.SearchPrompt
	si.SetStyles(s)

	// The quick-prompt box is a single-line quick action ("claude ▸ brn-42
	// ▸ …"), not a multi-line brief — always anchored to one specific
	// bead, where a short prompt plus the bead's own comment thread (and
	// now the live chat's own back-and-forth) carries the rest of the
	// context. CharLimit is generous for a longer prompt; still one line.
	qi := textinput.New()
	qi.Placeholder = "prompt — enter to send"
	qi.CharLimit = 2000
	s = qi.Styles()
	s.Focused.Prompt = st.CmdPrompt
	s.Blurred.Prompt = st.CmdPrompt
	qi.SetStyles(s)

	return Model{
		deps:             deps,
		styles:           st,
		ctx:              ctx,
		screen:           screenDashboard,
		cmdInput:         ti,
		searchInput:      si,
		quickPromptInput: qi,
		panes:            make(map[string]*agentPane),
		diffPanes:        make(map[string]*agentPane),
		sessions:         make(map[string]*agentTerminal),
		diffSessions:     make(map[string]*agentTerminal),
		hunkSeen:         make(map[string]bool),
		spawning:         make(map[string]bool),
		reconciled:       make(map[string]bool),
		// Start on the Active tab (working/validating/retry), not Backlog.
		listTab:          1,
		listSubTab:       -1,
		promptBeadTab:    1,
		promptBeadSubTab: -1,
		detailVP:         viewport.New(),
	}
}

// Init loads the dashboard's data on startup. The alt screen and mouse mode
// are declared on the View (tea.View fields), not here.
func (m Model) Init() tea.Cmd {
	return tea.Batch(loadBeads(m.ctx, m.deps), loadRuns(m.deps), loadHeaderStats(m.deps, ""))
}

// headerStatsInterval is the refresh cadence for the right-hand header bar's
// project metrics (git state + tmux pane counts).
const headerStatsInterval = 10 * time.Second

// reconcileCooldown is the minimum time between Deps.Reconcile passes —
// deliberately well above headerStatsInterval, since beads reload on that
// cadence but Reconcile shells out to git/tmux/bd per bead it examines.
// There is no dedicated tea.Tick for this: it piggybacks on beadsLoadedMsg,
// which already fires on that schedule (plus after every mutation), so no
// second self-arming tick chain exists to duplicate — see Model.tickScheduled's
// doc comment for why a second one would be a real risk here.
const reconcileCooldown = 90 * time.Second

// messages carry async fetch results back into Update.
type (
	beadsLoadedMsg struct {
		beads []store.Bead
		err   error
	}
	// reconcileMsg delivers one Deps.Reconcile pass's result — see
	// reconcileCmd.
	reconcileMsg struct {
		summary ReconcileSummary
		err     error
	}
	runsLoadedMsg struct {
		runs []store.Run
		err  error
	}
	commentsLoadedMsg struct {
		comments []store.Comment
		err      error
	}
	auditEventsLoadedMsg struct {
		events []store.AuditEvent
		err    error
	}
	// modelsLoadedMsg delivers the async Deps.Models fetch for the
	// assign-model picker, so opening the picker never blocks the update
	// loop on provider subprocess spawning (opencode models, ...).
	modelsLoadedMsg struct {
		models []string
		err    error
	}
	commandRanMsg struct {
		output string
		err    error
	}
	actionDoneMsg struct {
		msg string
		err error
	}
	// tickMsg drives the embedded terminal's re-render while a session is
	// active (the output pump fills the emulator off-loop; the tick just
	// keeps View being called so the emulator's frame lands on screen).
	tickMsg struct{}
	// statusExpiredMsg clears a transient status message after its timeout.
	// gen ties it to the notify/notifyErr call that scheduled it, so a
	// stale timer from an earlier notice can never dismiss a newer one.
	statusExpiredMsg struct{ gen int }
	// headerStatsMsg delivers the async Deps.HeaderStats fetch for the header's
	// right-hand project metrics.
	headerStatsMsg struct {
		stats HeaderStats
		err   error
	}
	// headerStatsTickMsg fires every headerStatsInterval to re-fetch the
	// header's project metrics. It is re-armed only from headerStatsMsg, so
	// at most one header-stats tick chain can exist.
	headerStatsTickMsg struct{}
	// shellRanMsg delivers the output of a bare bash command typed into the
	// : command bar. The output lands in the inline output panel instead of
	// a toast, so multi-line results are fully readable.
	shellRanMsg struct {
		lines []string
		err   error
	}
	// quickPromptSentMsg delivers the outcome of Board Mode's lowercase 'p'
	// — typing text into whichever agent-CLI tab is active in Prompt
	// Mode's left pane (quickPromptCmd) — a one-line toast, not an inline
	// panel: the text lands directly in that session's own live pty, there
	// is nothing else to show inline.
	quickPromptSentMsg struct {
		agentID string
		brn     string
		err     error
	}
	// personaUpdatedMsg delivers the outcome of the Personas tab's edit
	// form (Deps.UpdatePersona) — unlike quickPromptSentMsg it also carries
	// a freshly re-fetched persona list on success, so the edited
	// description/schedule/enabled show up in the list immediately instead
	// of staying stale until the roster is closed and reopened.
	personaUpdatedMsg struct {
		id       string
		personas []persona.Persona
		err      error
	}
	// fireNowMsg delivers the outcome of the Personas tab's 'r' (fire now)
	// — a manual, scope-based fire ignoring schedule timing (Deps.FireNow).
	fireNowMsg struct {
		id  string
		err error
	}
	// personaActivityMsg delivers a fetch of Deps.PersonaActivity — the
	// Personas tab's accordion (promptPersonaActivity), fetched lazily the
	// first time a persona's row is expanded with 'l'.
	personaActivityMsg struct {
		id     string
		events []store.AuditEvent
		err    error
	}
	// personaOutputMsg delivers a fetch of Deps.PersonaOutput — the
	// Personas tab's accordion (promptPersonaOutput), fetched lazily
	// alongside personaActivityMsg the first time a persona's row is
	// expanded with 'l'.
	personaOutputMsg struct {
		id     string
		output string
		err    error
	}
	// personaStatusesMsg delivers a bulk fetch of Deps.PersonaStatuses —
	// every Personas-tab row's live running/idle/off state, fetched once on
	// Prompt Mode entry.
	personaStatusesMsg struct {
		statuses map[string]PersonaStatus
		err      error
	}
	// personaCreatedMsg delivers the outcome of the Personas tab's 'n' (new
	// persona, Deps.CreatePersona) — mirrors personaUpdatedMsg: carries a
	// freshly re-fetched persona list on success so the new row appears
	// immediately.
	personaCreatedMsg struct {
		id       string
		personas []persona.Persona
		err      error
	}
	// promptAgentsLoadedMsg delivers a fetch of Deps.PromptAgentIDs — the
	// left pane's tab bar, fetched once on Prompt Mode entry.
	promptAgentsLoadedMsg struct {
		ids []string
		err error
	}
	// promptAgentSpawnedMsg delivers the outcome of spawning agentID's
	// left-pane live session — see spawnPromptAgentTerminal.
	promptAgentSpawnedMsg struct {
		agentID string
		t       *agentTerminal
		err     error
	}
	// promptAgentExitMsg fires when a left-pane session's process exits.
	promptAgentExitMsg struct {
		agentID string
		err     error
	}
	// promptTermTickMsg drives the left pane's live sessions' re-render —
	// see promptTermTick. Fully separate identity from tickMsg.
	promptTermTickMsg struct{}
	// deleteConfirmExpiredMsg fires 2 s after the first ctrl+D press to
	// cancel the pending delete confirmation automatically.
	deleteConfirmExpiredMsg struct{}
)
