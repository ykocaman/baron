package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
)

func loadBeads(ctx context.Context, deps Deps) tea.Cmd {
	return func() tea.Msg {
		beads, err := deps.Beads.List(ctx)
		return beadsLoadedMsg{beads: beads, err: err}
	}
}

// reconcileCmd runs one Deps.Reconcile pass off the update loop. skip is
// the set of BRNs Reconcile must leave alone (live sessions, and every
// "working" bead — see Deps.Reconcile's doc comment for why that state is
// excluded here).
// reconcilePassTimeout bounds one Reconcile pass so a hung subprocess call
// (a git lock file, a stuck tmux/bd invocation) can't leave
// Model.reconcileInFlight stuck true forever — which would silently
// disable reconciliation for the rest of the session, since nothing else
// would ever clear it. Generous relative to a legitimate pass: every
// individual bead's own git/tmux/bd calls should be seconds, not minutes,
// even on a large project, so this is a backstop against a genuinely
// wedged call, not a budget a normal pass is expected to approach.
const reconcilePassTimeout = 3 * time.Minute

func reconcileCmd(ctx context.Context, deps Deps, skip map[string]bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, reconcilePassTimeout)
		defer cancel()
		summary, err := deps.Reconcile(ctx, skip)
		return reconcileMsg{summary: summary, err: err}
	}
}

func loadRuns(deps Deps) tea.Cmd {
	return func() tea.Msg {
		if deps.Runs == nil {
			return runsLoadedMsg{}
		}
		runs, err := deps.Runs.All()
		return runsLoadedMsg{runs: runs, err: err}
	}
}

func loadHeaderStats(deps Deps, brn string) tea.Cmd {
	return func() tea.Msg {
		if deps.HeaderStats == nil {
			return headerStatsMsg{}
		}
		stats, err := deps.HeaderStats(brn)
		return headerStatsMsg{stats: stats, err: err}
	}
}

// headerStatsTick schedules the next header-stats refresh. headerStatsMsg
// calls this on every fetch it handles (see update.go), not just the tick's
// own — afterSelect and Init each produce one too — so
// headerStatsTickScheduled guards it down to exactly one live tea.Tick the
// same way termTick's tickScheduled does: a no-op whenever a chain is
// already running. headerStatsTickMsg is the one caller that must always
// re-arm, and it does so by clearing the flag first.
func (m *Model) headerStatsTick() tea.Cmd {
	if m.headerStatsTickScheduled {
		return nil
	}
	m.headerStatsTickScheduled = true
	return tea.Tick(headerStatsInterval, func(time.Time) tea.Msg { return headerStatsTickMsg{} })
}

// loadComments fetches a bead's comments; the store strips its own BRN
// prefix, so a full BRN is accepted.
func loadComments(ctx context.Context, deps Deps, brn string) tea.Cmd {
	return func() tea.Msg {
		if deps.Beads == nil {
			return commentsLoadedMsg{}
		}
		comments, err := deps.Beads.Comments(ctx, brn)
		return commentsLoadedMsg{comments: comments, err: err}
	}
}

// loadAuditEvents fetches the bead's check/gate/run history from the audit
// store; a nil Audit (tests, no path configured) yields an empty timeline.
func loadAuditEvents(deps Deps, brn string) tea.Cmd {
	return func() tea.Msg {
		if deps.Audit == nil {
			return auditEventsLoadedMsg{}
		}
		events, err := deps.Audit.Query(brn)
		return auditEventsLoadedMsg{events: events, err: err}
	}
}

// loadModels fetches the assign-model picker's list in the background.
func loadModels(deps Deps) tea.Cmd {
	return func() tea.Msg {
		models, err := deps.Models()
		return modelsLoadedMsg{models: models, err: err}
	}
}

// runTypedCommand and runTypedAction are the typed-Deps-method counterparts
// of runCommand/the now-removed runAction: same downstream message shape
// (commandRanMsg/actionDoneMsg) as the []string-through-RunCommand path,
// just calling a typed Deps method (ChangeStatus, Assign, CloseBead,
// Comment, Merge, Resume) instead of building argv for cobra to parse.
func runTypedCommand(fn func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		out, err := fn()
		return commandRanMsg{output: out, err: err}
	}
}

func runTypedAction(fn func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		out, err := fn()
		return actionDoneMsg{msg: out, err: err}
	}
}

// runShellCmd runs a bare bash command string (not a baron CLI command) and
// returns its combined stdout+stderr as a shellRanMsg so the inline output
// panel can render it. The command's working directory is deps.Dir.
func runShellCmd(ctx context.Context, deps Deps, line string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.CommandContext(ctx, "bash", "-c", line)
		cmd.Dir = deps.Dir
		out, err := cmd.CombinedOutput()
		raw := strings.TrimRight(string(out), "\n")
		var lines []string
		if raw != "" {
			lines = strings.Split(raw, "\n")
		}
		return shellRanMsg{lines: lines, err: err}
	}
}

// quickPromptCmd sends text into the active left-pane agent session as a
// live steer (bracketed paste + submit — agentTerminal.steer, the same
// mechanism deliverCommentCmd uses to hand a comment to a running agent) —
// Board Mode's 'p', now that the left pane provides one persistent chat per
// agent CLI instead of a one-shot persona dispatch. brn prefixes the text
// so the agent knows which bead this is about — steer has no bd-comment
// step of its own, so unlike the old Dispatch mechanism the bead reference
// has to travel in the message text itself, not a separately recorded
// comment.
func quickPromptCmd(m Model, brn, text string) tea.Cmd {
	id := m.activePromptAgentID()
	if id == "" {
		return func() tea.Msg {
			return quickPromptSentMsg{brn: brn, err: fmt.Errorf("no active agent CLI — open Prompt Mode and pick a tab first")}
		}
	}
	t := m.promptSessions[id]
	if t == nil || t.done {
		return func() tea.Msg {
			return quickPromptSentMsg{agentID: id, brn: brn, err: fmt.Errorf("%s isn't running — P then t to start it first", id)}
		}
	}
	msg := text
	if brn != "" {
		msg = brn + ": " + text
	}
	return func() tea.Msg {
		t.steer(msg)
		return quickPromptSentMsg{agentID: id, brn: brn}
	}
}

// personaUpdateCmd applies the Personas tab's edit form via
// deps.UpdatePersona, then re-fetches the persona list on success
// (deps.Personas) so the edited fields land in personaUpdatedMsg ready to
// replace m.personas — see that message type's doc comment for why this
// refetches instead of just toasting.
func personaUpdateCmd(deps Deps, f persona.FormFields) tea.Cmd {
	return func() tea.Msg {
		if err := deps.UpdatePersona(f); err != nil {
			return personaUpdatedMsg{id: f.ID, err: err}
		}
		personas, err := deps.Personas()
		return personaUpdatedMsg{id: f.ID, personas: personas, err: err}
	}
}

// personaDeleteCmd applies the Personas tab's delete key via
// deps.DeletePersona, then re-fetches the persona list on success — same
// shape as personaUpdateCmd/personaCreateCmd.
func personaDeleteCmd(deps Deps, id string) tea.Cmd {
	return func() tea.Msg {
		if err := deps.DeletePersona(id); err != nil {
			return personaUpdatedMsg{id: id, err: err}
		}
		personas, err := deps.Personas()
		return personaUpdatedMsg{id: id, personas: personas, err: err}
	}
}

// fireNowCmd fires deps.FireNow for id in the background — the Crew
// Roster's 'r' key.
func fireNowCmd(deps Deps, id string) tea.Cmd {
	return func() tea.Msg {
		err := deps.FireNow(id)
		return fireNowMsg{id: id, err: err}
	}
}

// personaStatusesCmd fetches deps.PersonaStatuses for ids in the
// background — Prompt Mode's status-first roster rows.
func personaStatusesCmd(deps Deps, ids []string) tea.Cmd {
	return func() tea.Msg {
		if deps.PersonaStatuses == nil {
			return personaStatusesMsg{}
		}
		statuses, err := deps.PersonaStatuses(ids)
		return personaStatusesMsg{statuses: statuses, err: err}
	}
}

// personaActivityCmd fetches deps.PersonaActivity for id in the
// background — Prompt Mode's worklist.
func personaActivityCmd(deps Deps, id string) tea.Cmd {
	return func() tea.Msg {
		if deps.PersonaActivity == nil {
			return personaActivityMsg{id: id}
		}
		events, err := deps.PersonaActivity(id)
		return personaActivityMsg{id: id, events: events, err: err}
	}
}

// personaOutputCmd fetches deps.PersonaOutput for id in the background —
// this shells out to tmux capture-pane, so it must never run inline from
// View(). A nil Deps.PersonaOutput yields an empty, error-free result
// (same "hint, not error" contract as the real implementation's
// never-run case).
func personaOutputCmd(deps Deps, id string) tea.Cmd {
	return func() tea.Msg {
		if deps.PersonaOutput == nil {
			return personaOutputMsg{id: id}
		}
		out, err := deps.PersonaOutput(id)
		return personaOutputMsg{id: id, output: out, err: err}
	}
}

// personaCreateCmd applies the Personas tab's new-persona form via
// deps.CreatePersona, then re-fetches the persona list on success so the
// new row appears immediately — same shape as personaUpdateCmd.
func personaCreateCmd(deps Deps, f persona.FormFields) tea.Cmd {
	return func() tea.Msg {
		newID, err := deps.CreatePersona(f)
		if err != nil {
			return personaCreatedMsg{id: f.Name, err: err}
		}
		personas, err := deps.Personas()
		return personaCreatedMsg{id: newID, personas: personas, err: err}
	}
}

// promptAgentIDsCmd fetches deps.PromptAgentIDs in the background — the
// left pane's tab bar, fetched once on Prompt Mode entry.
func promptAgentIDsCmd(deps Deps) tea.Cmd {
	return func() tea.Msg {
		if deps.PromptAgentIDs == nil {
			return promptAgentsLoadedMsg{}
		}
		ids, err := deps.PromptAgentIDs()
		return promptAgentsLoadedMsg{ids: ids, err: err}
	}
}

// isBaronCmd reports whether first is a known baron top-level command —
// used to decide whether a :cmd line should run through the baron CLI or
// as a raw bash command.

// notifyDuration is how long a notice stays on screen before it
// auto-dismisses: fixed and unaffected by keypresses or by whether another
// notice arrives in the meantime (see notify's gen guard).
const notifyDuration = 5 * time.Second

// notify is the ONE path every transient notice (status or error) must go
// through: every screen/overlay that needs to tell the user something showed
// up, succeeded, or failed calls this (or notifyErr) instead of touching
// statusMsg/err directly, so the toast's timing and rendering stay in
// exactly one place — see view.go's viewToast/viewWithToast, the only
// renderer for what this sets.
func (m *Model) notify(msg string) tea.Cmd {
	m.statusMsg, m.err = msg, nil
	m.statusGen++
	return statusDismissCmd(m.statusGen)
}

// notifyErr is notify's error counterpart.
func (m *Model) notifyErr(err error) tea.Cmd {
	m.err, m.statusMsg = err, ""
	m.statusGen++
	return statusDismissCmd(m.statusGen)
}

// statusDismissCmd clears the status message after notifyDuration, so a
// transient notice never lingers on screen. gen must be the statusGen
// notify/notifyErr set when scheduling this timer — see statusExpiredMsg.
func statusDismissCmd(gen int) tea.Cmd {
	return tea.Tick(notifyDuration, func(time.Time) tea.Msg { return statusExpiredMsg{gen: gen} })
}

// runPipelineStatuses are the statuses an agent's run pipeline drives by
// itself. Bead changes into/out of the user-driven statuses (open, assigned,
// closed, cancelled) already confirm themselves through their action result,
// so only these transitions are worth a watch notice.
var runPipelineStatuses = map[store.BeadStatus]bool{
	store.BeadStatusWorking:    true,
	store.BeadStatusValidating: true,
	store.BeadStatusRetry:      true,
	store.BeadStatusBlocked:    true,
	store.BeadStatusMergable:   true,
	store.BeadStatusHumanQueue: true,
	store.BeadStatusMerged:     true,
}

// statusChangeNotices compares freshly loaded beads against the previous
// snapshot (m.beads) and returns a notify cmd per bead whose status moved
// through the run pipeline. The first load has no snapshot and notifies
// nothing.
func (m *Model) statusChangeNotices(beads []store.Bead) []tea.Cmd {
	if m.beads == nil {
		return nil
	}
	prev := make(map[string]store.BeadStatus, len(m.beads))
	for _, b := range m.beads {
		prev[string(b.BRN)] = b.Status
	}
	var cmds []tea.Cmd
	for _, b := range beads {
		old, ok := prev[string(b.BRN)]
		if !ok || old == b.Status || !runPipelineStatuses[b.Status] {
			continue
		}
		cmds = append(cmds, m.notify(fmt.Sprintf("%s → %s", b.BRN, b.Status)))
	}
	return cmds
}

// newBeadFormTypeOptions returns the huh.Options for the new-bead form's
// type select field, each option's display Key carrying the same
// glyph+color styling the board tree uses for that type (typeGlyph/
// styles.typeStyle) so the picker doesn't go visually flat next to the rest
// of the app.
