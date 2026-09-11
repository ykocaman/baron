package tui

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

var (
	errEmptyTitle   = errors.New("title is required")
	errEmptyTier    = errors.New("tier is required")
	errEmptyComment = errors.New("comment text is required")
)

// formWidth is passed to every huh.Form via WithWidth so its fields wrap to
// the same width the form actually renders at — m.styles.OverlayBox is a
// fixed 76 columns wide with Padding(1,2) and a 1-column border each side
// (76 - 2 - 4 = 70 of real content room). Left unset, huh defaults every
// field to defaultWidth (80, huh/v2's own form.go) — 10 columns wider than
// the box actually has. Caught for real on the persona edit form: the
// Event-triggers field's title text (the longest in the app) wrapped past
// column 70, and the wrapped tail landed on the same visual line as the
// field's own value, reading as if the input already contained garbled
// duplicate text. Every huh.NewForm(...) call in this file must chain
// .WithWidth(formWidth) for the same reason, not just that one field — in
// practice that means building overlay forms through newOverlayForm below
// rather than calling huh.NewForm directly.
const formWidth = 70

// fieldLabelDescription is the huh.Title used by every form's free-text
// description field (new bead, edit bead, persona editor).
const fieldLabelDescription = "Description"

// newOverlayForm builds one of BARON's overlay huh.Forms with the shared
// conventions every one of them needs: no real stdin/stdout (BARON's own
// View renders the form, never huh's default terminal renderer) and no
// built-in help/error chrome (BARON renders its own, in its own style).
func newOverlayForm(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).
		WithWidth(formWidth).
		WithInput(strings.NewReader("")).
		WithOutput(&bytes.Buffer{}).
		WithShowHelp(false).
		WithShowErrors(false)
}

// asModel asserts tm is the concrete Model. Every tea.Model this package
// hands back to itself — Update's own result, and every internal handler
// that boxes its Model receiver the same way — is always a Model by
// construction, but the tea.Model interface forces callers to unbox it.
// Centralizing that unboxing here turns a broken invariant into a clear
// panic message instead of a bare "interface conversion" one at whichever
// call site happened to hit it.
func asModel(tm tea.Model) Model {
	m, ok := tm.(Model)
	if !ok {
		panic(fmt.Sprintf("tui: expected Model, got %T", tm))
	}
	return m
}

// Update implements tea.Model: it dispatches every message (key, mouse,
// window resize, and the async load/command results below) to the handler
// for the model's current screen or overlay.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeSessions()
		m.resizePromptSessions()
		return m, nil

	case beadsLoadedMsg:
		return m.handleBeadsLoadedMsg(msg)

	case reconcileWorkingMsg:
		return m.handleReconcileWorkingMsg(msg)

	case reconcileMsg:
		return m.handleReconcileMsg(msg)

	case runsLoadedMsg:
		if msg.err == nil {
			m.runs = msg.runs
		}
		return m, nil

	case headerStatsMsg:
		return m.handleHeaderStatsMsg(msg)

	case headerStatsTickMsg:
		// Clearing headerStatsTickScheduled first hands the chain's ownership
		// back to headerStatsTick (via the headerStatsMsg this fetch produces),
		// mirroring tickMsg/tickScheduled exactly.
		m.headerStatsTickScheduled = false
		return m, tea.Batch(loadHeaderStats(m.deps, string(m.detail.BRN)), loadBeads(m.ctx, m.deps))

	case commentsLoadedMsg:
		return m.handleCommentsLoadedMsg(msg)

	case auditEventsLoadedMsg:
		return m.handleAuditEventsLoadedMsg(msg)

	case modelsLoadedMsg:
		return m.handleModelsLoadedMsg(msg)

	case commandRanMsg:
		return m.handleCommandRanMsg(msg)

	case actionDoneMsg:
		return m.handleActionDoneMsg(msg)

	case shellRanMsg:
		return m.handleShellRanMsg(msg)

	case quickPromptSentMsg:
		return m.handleQuickPromptSentMsg(msg)

	case fireNowMsg:
		return m.handleFireNowMsg(msg)

	case personaUpdatedMsg:
		return m.handlePersonaUpdatedMsg(msg)

	case personaActivityMsg:
		return m.handlePersonaActivityMsg(msg)

	case personaOutputMsg:
		return m.handlePersonaOutputMsg(msg)

	case personaStatusesMsg:
		return m.handlePersonaStatusesMsg(msg)

	case personaCreatedMsg:
		return m.handlePersonaCreatedMsg(msg)

	case promptAgentsLoadedMsg:
		return m.handlePromptAgentsLoadedMsg(msg)

	case promptAgentSpawnedMsg:
		return m.handlePromptAgentSpawned(msg)

	case promptAgentExitMsg:
		return m.handlePromptAgentExit(msg)

	case promptTermTickMsg:
		return m.handlePromptTermTick()

	case deleteConfirmExpiredMsg:
		// 2 s elapsed after the first ctrl+D without a second press → cancel.
		m.deleteConfirmBRN = ""
		m.deleteConfirmPersonaID = ""
		m.statusMsg = ""
		m.err = nil
		return m, nil

	case statusExpiredMsg:
		return m.handleStatusExpiredMsg(msg)

	case tickMsg:
		return m.handleTickMsg()

	case hunkCommentsMsg:
		m.hunkPollInFlight = false
		if msg.err != nil {
			// A poll failing is not worth a notice on every tick — the Diff
			// tab itself is still on screen and working.
			return m, nil
		}
		return m, m.ingestHunkComments(msg)

	case paneHistoryMsg:
		return m.handlePaneHistoryMsg(msg)

	case agentSpawnedMsg:
		return m.handleAgentSpawnedMsg(msg)

	case agentExitMsg:
		return m.handleAgentExitMsg(msg)

	case agentDetachedMsg:
		return m.handleAgentDetachedMsg(msg)

	case tea.KeyPressMsg:
		return m.updateKey(msg)

	case tea.MouseMsg:
		return m.updateMouse(msg)
	}
	return m, nil
}

// handleBeadsLoadedMsg handles beadsLoadedMsg: the periodic/on-demand full
// board reload, fanning out to the dashboard-state helpers in
// update_beads_loaded.go.
func (m Model) handleBeadsLoadedMsg(msg beadsLoadedMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if msg.err == nil {
		cmds = append(cmds, m.statusChangeNotices(msg.beads)...)
	}
	m.err = msg.err
	m.beads = msg.beads
	m.humanQueue = humanQueueOf(msg.beads)
	cmds = append(cmds, m.handlePendingRunAfterAssign(msg)...)
	m.handlePendingFocusBRN()
	m.landFirstNonEmptyTab()
	m.landFirstNonEmptySubTab()
	cmds = append(cmds, m.restoreDashboardSelection()...)
	cmds = append(cmds, m.syncPromptBeadsDetail()...)
	m.restoreRunSummaries(msg)
	cmds = append(cmds, m.reconcileWorkingBeads(msg)...)
	cmds = append(cmds, m.dispatchReconcile(msg)...)
	if msg.err != nil {
		cmds = append(cmds, m.notifyErr(msg.err))
	}
	return m, tea.Batch(cmds...)
}

// handleReconcileWorkingMsg handles reconcileWorkingMsg: the liveness check
// for a bead the board believes is "working" with no local session.
func (m Model) handleReconcileWorkingMsg(msg reconcileWorkingMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil || !msg.alive {
		// Genuinely gone (or undetermined): no agent is running, so
		// "working" is a lie — mark the bead retry so it reads as
		// re-runnable (a human relaunches it, or `baron run`) instead of
		// stranding it in a status no process will ever resolve.
		return m, runTypedAction(func() (string, error) { return m.deps.ChangeStatus(msg.brn, "retry") })
	}
	// Still running in its tmux window from an earlier process —
	// reattach a live view instead of leaving 't' stuck on "no
	// session". EnsureRunning no-ops on an already-running window, so
	// this only attaches; it never re-types the prompt or restarts it.
	cols, rows := m.termDims()
	return m, spawnAgentTerminal(m.ctx, m.deps, m.beads, msg.brn, cols, rows, "")
}

// handleReconcileMsg handles reconcileMsg: the result of a background
// reconcile pass, surfaced as a single summary toast.
func (m Model) handleReconcileMsg(msg reconcileMsg) (tea.Model, tea.Cmd) {
	// Cleared unconditionally, before either early return below: this is
	// what lets the next pass be dispatched at all (see
	// reconcileInFlight's doc comment) — an error or an empty-actions
	// result is still a completed pass, not one still running.
	m.reconcileInFlight = false
	// A failed pass surfaces as the same red toast every other error
	// path in this file uses — never a raw dump to the screen. This is
	// background maintenance the user didn't ask for, but silence on
	// failure (the old behavior) meant a persistently broken
	// git/tmux/bd call had zero visibility: not even a hint to run
	// :doctor. reconcileCooldown (90s) already rate-limits how often a
	// pass — and so this toast — can fire, so a real failure repeats at
	// most once a cooldown, not once a frame.
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("reconcile: %w", msg.err))
	}
	if len(msg.summary.Actions) == 0 {
		return m, nil
	}
	var moved, needsAttention int
	for _, a := range msg.summary.Actions {
		if a.From == a.To {
			needsAttention++
		} else {
			moved++
		}
	}
	var line string
	switch {
	case moved > 0 && needsAttention > 0:
		line = fmt.Sprintf("reconciled %d bead(s), %d need attention", moved, needsAttention)
	case moved > 0:
		line = fmt.Sprintf("reconciled %d bead(s)", moved)
	default:
		line = fmt.Sprintf("%d bead(s) need attention (see Needs You)", needsAttention)
	}
	cmds := []tea.Cmd{m.notify(line)}
	if moved > 0 {
		// Something actually changed status — refresh the board so it's
		// not showing stale data until the next unrelated reload.
		cmds = append(cmds, loadBeads(m.ctx, m.deps))
	}
	return m, tea.Batch(cmds...)
}

// handleHeaderStatsMsg handles headerStatsMsg, re-arming the 10s refresh
// chain only when HeaderStats is actually wired.
func (m Model) handleHeaderStatsMsg(msg headerStatsMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		m.headerStats = msg.stats
	}
	// Re-arm the 10s refresh chain only when HeaderStats is wired; the
	// nil-deps test harness never arms it, so no phantom chain spins.
	if m.deps.HeaderStats != nil {
		return m, m.headerStatsTick()
	}
	return m, nil
}

// handleModelsLoadedMsg handles modelsLoadedMsg, building the model picker's
// huh form from the fetched (and cache-ranked) model list.
func (m Model) handleModelsLoadedMsg(msg modelsLoadedMsg) (tea.Model, tea.Cmd) {
	// The picker may have been closed (esc/q) while the fetch was in
	// flight — drop the result then.
	if !m.modelLoading && m.modelForm == nil {
		return m, nil
	}
	m.modelLoading = false
	if msg.err != nil || len(msg.models) == 0 {
		m.modelForm = nil
		m.modelResult = nil
		m.modelOptions = nil
		return m, m.notify("no models available — run the assign command manually")
	}
	cache := loadModelCache()
	var rest []string
	m.modelRecent, m.modelPopular, rest = rank(cache, msg.models)
	m.modelChoices = append(append(append([]string{}, m.modelRecent...), m.modelPopular...), rest...)
	// Build huh options with ranking preserved
	opts := make([]huh.Option[string], 0, len(m.modelRecent)+len(m.modelPopular)+len(rest))
	for _, s := range m.modelRecent {
		opts = append(opts, huh.NewOption(s, s))
	}
	for _, s := range m.modelPopular {
		opts = append(opts, huh.NewOption(s, s))
	}
	for _, s := range rest {
		opts = append(opts, huh.NewOption(s, s))
	}
	m.modelOptions = opts

	// Create the huh form now that we have the options
	var result string
	selectField := huh.NewSelect[string]().
		Title("Assign model").
		Options(opts...).
		Value(&result).
		Filtering(true).
		Height(12)
	form := newOverlayForm(huh.NewGroup(selectField))
	m.modelForm = initHuhForm(form)
	m.modelResult = &result
	return m, nil
}

// handleCommandRanMsg handles commandRanMsg: a baron CLI action's result
// (assign/close/merge/pr-review/create — never a manually typed ":" command,
// which is shellRanMsg and keeps its own inline output panel since real bash
// output can be long and worth reading in full), surfaced as a one-line
// notify() toast.
func (m Model) handleCommandRanMsg(msg commandRanMsg) (tea.Model, tea.Cmd) {
	var dismissCmd tea.Cmd
	if msg.err != nil {
		dismissCmd = m.notifyErr(msg.err)
	} else if line := oneLineStatus(msg.output); line != "" {
		dismissCmd = m.notify(line)
	} else {
		dismissCmd = m.notify("done")
	}
	// A fresh work create must land the dashboard on the tab holding
	// the new bead; grab its BRN from the confirmation line so the
	// next beadsLoadedMsg can jump there (the bead is "open" on the
	// Backlog tab unless its parent epic lives elsewhere).
	if m.pendingNewBead {
		m.pendingNewBead = false
		if msg.err == nil {
			if brn := createdBRN(msg.output); brn != "" {
				m.pendingFocusBRN = brn
			}
		}
	}
	cmds := []tea.Cmd{dismissCmd, loadBeads(m.ctx, m.deps)}
	if m.runAfterAssign != "" {
		// 'r' on an unassigned bead chained: assign succeeded → start the
		// embedded agent terminal once the reload above lands (see
		// pendingRunAfterAssign's doc comment for why spawning here,
		// straight off m.beads, would still see the bead unassigned);
		// assign failed → drop the chain.
		brn := m.runAfterAssign
		m.runAfterAssign = ""
		if msg.err == nil {
			m.pendingRunAfterAssign = brn
		}
	}
	return m, tea.Batch(cmds...)
}

// handleActionDoneMsg handles actionDoneMsg, a single-bead action's result.
func (m Model) handleActionDoneMsg(msg actionDoneMsg) (tea.Model, tea.Cmd) {
	var dismissCmd tea.Cmd
	if msg.err != nil {
		dismissCmd = m.notifyErr(msg.err)
	} else {
		dismissCmd = m.notify(msg.msg)
	}
	cmds := []tea.Cmd{dismissCmd, loadBeads(m.ctx, m.deps)}
	if m.boardDetailVisible() && m.detail.BRN != "" {
		cmds = append(cmds, loadComments(m.ctx, m.deps, string(m.detail.BRN)), loadAuditEvents(m.deps, string(m.detail.BRN)))
	}
	return m, tea.Batch(cmds...)
}

// handleShellRanMsg handles shellRanMsg, populating the ":" command bar's
// inline output panel.
func (m Model) handleShellRanMsg(msg shellRanMsg) (tea.Model, tea.Cmd) {
	m.shellOutputScroll = 0
	if len(msg.lines) == 0 && msg.err == nil {
		m.shellOutput = []string{"(no output)"}
	} else {
		m.shellOutput = msg.lines
	}
	if msg.err != nil {
		m.shellOutput = append(m.shellOutput, "─── exit: "+msg.err.Error()+" ───")
	}
	return m, nil
}

// handleFireNowMsg handles fireNowMsg: a persona's "fire now" trigger result.
func (m Model) handleFireNowMsg(msg fireNowMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("fire %s: %w", msg.id, msg.err))
	}
	cmds := []tea.Cmd{m.notify(fmt.Sprintf("fired %q — l to watch it run", msg.id))}
	if m.deps.PersonaActivity != nil {
		cmds = append(cmds, personaActivityCmd(m.deps, msg.id))
	}
	// The roster's running/idle glyph otherwise only refreshes when
	// Prompt Mode is (re)opened — stale the whole time a just-fired
	// persona is actually working. One explicit fetch on the
	// fire-and-forget action itself, not a recurring poll of every
	// persona's tmux window (that cost is what keeps the roster from
	// auto-polling in the first place) — the full roster, not just
	// msg.id: personaStatusesMsg replaces m.personaStatuses wholesale
	// (see personaIDs' doc comment).
	if m.deps.PersonaStatuses != nil {
		cmds = append(cmds, personaStatusesCmd(m.deps, personaIDs(m.personas)))
	}
	return m, tea.Batch(cmds...)
}

// handleCommentsLoadedMsg handles commentsLoadedMsg, the Overview tab's
// comment list fetch.
func (m Model) handleCommentsLoadedMsg(msg commentsLoadedMsg) (tea.Model, tea.Cmd) {
	m.comments = msg.comments
	m.commentsLoading = false
	if msg.err != nil {
		return m, m.notifyErr(msg.err)
	}
	return m, nil
}

// handleAuditEventsLoadedMsg handles auditEventsLoadedMsg, the Audit tab's
// event list fetch.
func (m Model) handleAuditEventsLoadedMsg(msg auditEventsLoadedMsg) (tea.Model, tea.Cmd) {
	m.auditEvents = msg.events
	if msg.err != nil {
		return m, m.notifyErr(msg.err)
	}
	return m, nil
}

// handleQuickPromptSentMsg handles quickPromptSentMsg, the quick-prompt
// box's send result.
func (m Model) handleQuickPromptSentMsg(msg quickPromptSentMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("prompt %s: %w", msg.agentID, msg.err))
	}
	return m, m.notify(fmt.Sprintf("sent to %s — P to watch it work", msg.agentID))
}

// handlePersonaUpdatedMsg handles personaUpdatedMsg, a persona edit's result.
func (m Model) handlePersonaUpdatedMsg(msg personaUpdatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("persona %s: %w", msg.id, msg.err))
	}
	sort.Slice(msg.personas, func(i, j int) bool { return msg.personas[i].ID < msg.personas[j].ID })
	m.personas = msg.personas
	return m, m.notify(fmt.Sprintf("updated persona %q", msg.id))
}

// handlePersonaActivityMsg handles personaActivityMsg, one persona's audit
// activity fetch.
func (m Model) handlePersonaActivityMsg(msg personaActivityMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("persona %s activity: %w", msg.id, msg.err))
	}
	if m.promptPersonaActivity == nil {
		m.promptPersonaActivity = map[string][]store.AuditEvent{}
	}
	m.promptPersonaActivity[msg.id] = msg.events
	return m, nil
}

// handlePersonaOutputMsg handles personaOutputMsg, one persona's last-run
// output fetch.
func (m Model) handlePersonaOutputMsg(msg personaOutputMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("persona %s output: %w", msg.id, msg.err))
	}
	if m.promptPersonaOutput == nil {
		m.promptPersonaOutput = map[string]string{}
	}
	m.promptPersonaOutput[msg.id] = msg.output
	return m, nil
}

// handlePersonaStatusesMsg handles personaStatusesMsg, the whole roster's
// running/idle status fetch.
func (m Model) handlePersonaStatusesMsg(msg personaStatusesMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("persona statuses: %w", msg.err))
	}
	m.personaStatuses = msg.statuses
	return m, nil
}

// handlePersonaCreatedMsg handles personaCreatedMsg, a new persona's result.
func (m Model) handlePersonaCreatedMsg(msg personaCreatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("create persona: %w", msg.err))
	}
	sort.Slice(msg.personas, func(i, j int) bool { return msg.personas[i].ID < msg.personas[j].ID })
	m.personas = msg.personas
	return m, m.notify(fmt.Sprintf("created persona %q", msg.id))
}

// handlePromptAgentsLoadedMsg handles promptAgentsLoadedMsg, the list of
// available agent CLIs for Prompt Mode's left pane.
func (m Model) handlePromptAgentsLoadedMsg(msg promptAgentsLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.notifyErr(fmt.Errorf("agent CLIs: %w", msg.err))
	}
	m.promptAgentIDs = msg.ids
	if m.promptAgentTab >= len(m.promptAgentIDs) {
		m.promptAgentTab = 0
	}
	return m, nil
}

// handleStatusExpiredMsg handles statusExpiredMsg: only the notify/notifyErr
// call that scheduled this exact timer may clear the notice — a stale timer
// from an already-replaced notice must never cut the newer one short.
func (m Model) handleStatusExpiredMsg(msg statusExpiredMsg) (tea.Model, tea.Cmd) {
	if msg.gen == m.statusGen {
		m.statusMsg = ""
		m.err = nil
	}
	return m, nil
}

// handleTickMsg handles tickMsg, the frame tick: while an embedded session
// is live it re-arms, so the pump's emulator frame is re-rendered each tick.
// Once every session has exited the tick stops and the Terminal tab
// freezes. checkTmuxDone rides the same tick to notice a tmux-backed
// session's agent finishing (DoneSentinel) — the window itself stays up, so
// nothing else would ever detect this.
//
// This is the one caller that must re-arm unconditionally: clearing
// tickScheduled first hands the chain's ownership back to termTick, which
// either continues it (a session is still live) or lets it stop. Every
// other caller is a no-op while a chain runs.
func (m Model) handleTickMsg() (tea.Model, tea.Cmd) {
	m.tickScheduled = false
	cmds := append(m.checkTmuxDone(), m.termTick())
	// The live Diff session's review comments ride the same clock, rate
	// limited to hunkPollInterval — see maybePollHunkCmd for why this is
	// not its own timer.
	if cmd := m.maybePollHunkCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// Same clock, same reasoning: a bead-run session gone quiet at its
	// own input prompt is exactly as "nobody would otherwise notice" as
	// a live Diff session's review comments — see checkIdleAgents.
	cmds = append(cmds, m.checkIdleAgents()...)
	return m, tea.Batch(cmds...)
}

// handlePaneHistoryMsg handles paneHistoryMsg, a captured-scrollback result
// for either a board session or a Prompt Mode session (matched by brn+kind
// since prompt sessions are keyed by agent ID, not brn).
func (m Model) handlePaneHistoryMsg(msg paneHistoryMsg) (tea.Model, tea.Cmd) {
	t := m.sessionsFor(msg.kind)[msg.brn]
	// Also check prompt sessions (keyed by agent ID, iterate to match brn).
	if t == nil {
		for _, pt := range m.promptSessions {
			if pt.brn == msg.brn && pt.kind == msg.kind {
				t = pt
				break
			}
		}
	}
	if t == nil {
		return m, nil
	}
	t.capturing = false
	t.capturedAt = time.Now()
	if msg.err != nil || len(msg.lines) <= m.detailPaneHeight() {
		t.historyUnavailable = true
		if p := m.paneFor(msg.brn, msg.kind); p != nil {
			p.offset = 0
		}
		return m, nil
	}
	t.historyUnavailable = false
	t.history = msg.lines
	if p := m.paneFor(msg.brn, msg.kind); p != nil {
		p.offset = clampInt(p.offset, historyMaxOffset(len(t.history), m.detailPaneHeight()))
	}
	// Clamp prompt mode scroll offset for the same terminal.
	bodyRows := max(1, m.splitBoxInnerRows()-3)
	m.promptTermScrollOffset = clampInt(m.promptTermScrollOffset, historyMaxOffset(len(t.history), bodyRows))
	return m, nil
}

// handleAgentSpawnedMsg handles agentSpawnedMsg, a just-started session (or
// its failure to start).
func (m Model) handleAgentSpawnedMsg(msg agentSpawnedMsg) (tea.Model, tea.Cmd) {
	// The spawn has resolved either way, so the slot is free again — a
	// failed start must not latch the guard and block every later retry.
	delete(m.spawning, spawnKey(msg.brn, msg.kind))
	if msg.err != nil {
		label := "agent"
		if msg.kind == kindDiff {
			label = "diff view"
		}
		return m, tea.Batch(m.notify(label+" failed to start: "+msg.err.Error()), loadBeads(m.ctx, m.deps))
	}
	t := msg.t
	m.sessionsFor(msg.kind)[msg.brn] = t
	// The session is up: register the exit waiter and size the emulator
	// to the current pane. Only sync the shared focus/zoom flags when
	// this spawn is for the tab actually on screen — the user may have
	// switched tabs while an earlier spawn (of the other kind) was
	// still in flight.
	if msg.kind == m.currentKind() {
		m.termFocus, m.termZoom = t.focus, t.zoom
	}
	m.resizeSessions()
	cmds := []tea.Cmd{waitTermExit(t), m.termTick()}
	if msg.kind == kindAgent {
		cmds = append(cmds, m.notify("agent for "+msg.brn+" is running"))
	}
	return m, tea.Batch(cmds...)
}

// handleAgentExitMsg handles agentExitMsg, dispatching to the diff-viewer or
// bead-run-pipeline exit handling depending on the session kind.
func (m Model) handleAgentExitMsg(msg agentExitMsg) (tea.Model, tea.Cmd) {
	t := m.sessionsFor(msg.kind)[msg.brn]
	if t != nil {
		t.done = true
		t.focus = false // Release focus so scroll keys work on the dead pane
	}
	showing := msg.brn == string(m.detail.BRN) && msg.kind == m.currentKind()
	if msg.kind == kindDiff {
		return m.handleDiffExitMsg(msg, t, showing)
	}
	return m.handleAgentWorkExitMsg(msg, t, showing)
}

// handleDiffExitMsg is handleAgentExitMsg's kindDiff branch. A diff viewer
// exiting has no bead-run-pipeline consequence — unlike the agent kind,
// there's no gate/resume to trigger, and nothing worth persisting (see
// spawnDiffTerminal's doc comment) or keeping a done entry around for: drop
// it from the map outright instead of leaving a done=true husk behind
// forever — the render tick (checkTmuxDone/resizeSessions/termTick) still
// walks every map entry each frame regardless of done, and letting that
// grow unbounded across a long session is exactly the accumulation bug this
// kind was rewritten to avoid.
func (m Model) handleDiffExitMsg(msg agentExitMsg, t *agentTerminal, showing bool) (tea.Model, tea.Cmd) {
	delete(m.diffSessions, msg.brn)
	if !showing {
		return m, nil
	}
	if t != nil {
		t.focus, t.zoom = false, false
	}
	m.termFocus, m.termZoom = false, false
	if t != nil && t.userKilled {
		// The user just closed it and has already been told so.
		return m, nil
	}
	if msg.err != nil {
		return m, m.notify("diff view for " + msg.brn + " exited: " + msg.err.Error())
	}
	return m, m.notify("diff view for " + msg.brn + " exited")
}

// handleAgentWorkExitMsg is handleAgentExitMsg's default (bead-run) branch.
// The embedded terminal never ran a gate — the bead is still "working" the
// moment the process exits regardless of what happened inside the session.
// --resume runs the same ask-check/validate/gate/ready-for-merge pipeline
// the headless `baron run` loop uses, just without relaunching an agent: a
// gate failure goes straight to the human queue (no retry budget) since a
// human was already driving this session directly.
func (m Model) handleAgentWorkExitMsg(msg agentExitMsg, t *agentTerminal, showing bool) (tea.Model, tea.Cmd) {
	brn := msg.brn
	resume := runTypedAction(func() (string, error) { return m.deps.Resume(brn) })
	persist := persistSessionCmd(m.deps, msg.brn, t)
	if !showing {
		return m, tea.Batch(persist, resume)
	}
	if t != nil {
		t.focus, t.zoom = false, false
	}
	m.termFocus, m.termZoom = false, false
	var dismissCmd tea.Cmd
	if msg.err != nil {
		dismissCmd = m.notify("agent for " + msg.brn + " exited: " + msg.err.Error())
	} else {
		dismissCmd = m.notify("agent for " + msg.brn + " exited")
	}
	return m, tea.Batch(dismissCmd, loadBeads(m.ctx, m.deps), persist, resume)
}

// handleAgentDetachedMsg handles agentDetachedMsg: only BARON's own viewer
// went away (killSession, or the attach client dying) — the tmux window and
// its process (agent or hunk) are untouched and may still be running.
// Detach the (now orphaned) viewer session and drop the local session entry
// so 't' reports "no session" honestly until the bead is reconciled (agent
// kind only — see beadsLoadedMsg: still-alive reattaches, truly gone
// resumes; a diff session has no such reconciliation, it just respawns next
// time the Diff tab opens).
func (m Model) handleAgentDetachedMsg(msg agentDetachedMsg) (tea.Model, tea.Cmd) {
	delete(m.sessionsFor(msg.kind), msg.brn)
	if msg.brn == string(m.detail.BRN) && msg.kind == m.currentKind() {
		m.termFocus, m.termZoom = false, false
	}
	if m.deps.AgentHost == nil {
		return m, nil
	}
	window := domain.TmuxWindowName(msg.brn)
	if msg.kind == kindDiff {
		window = domain.TmuxDiffWindowName(msg.brn)
	}
	host, ctx := m.deps.AgentHost, m.ctx
	return m, func() tea.Msg {
		_ = host.Detach(ctx, window)
		return nil
	}
}

// updateMouse handles wheel + click events on the dashboard. Wheel over the
// left pane moves the cursor, over the right pane scrolls the detail content;
// a click selects the row under it. Row Y=5 is the first list row: header 1 +
// blank 1 + border 1 + tab bar 1 + sub-tab bar 1.
//
// Screen-coordinate layout (0-indexed):
//
//	Y=0  header
//	Y=1  blank
//	Y=2  ListBox top border
// updateMouse handles wheel + click events on the dashboard. Wheel over the
// left pane moves the cursor, over the right pane scrolls the detail content;
// a click selects the row under it. Row Y=5 is the first list row: header 1 +
// blank 1 + border 1 + tab bar 1 + sub-tab bar 1.
//
// Screen-coordinate layout (0-indexed):
//
//	Y=0  header
//	Y=1  blank
//	Y=2  ListBox top border
//	Y=3  left tab bar  / right pane: title
//	Y=4  left sub-tab  / right pane: status line
//	Y=5  left bead rows start / right pane: detail-tab bar (Overview/Terminal/Audit)
//	…
