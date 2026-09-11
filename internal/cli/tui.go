package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tui"
)

// runTUI opens the TUI (internal/tui), wiring it to the same store/provider
// instances the rest of the app uses. The embedded-terminal rework removed
// the cockpit: `baron` in a TTY now draws the full split-pane TUI directly
// (no tmux sidecar hosting the TUI itself, no re-entrant second copy) —
// tmux comes back in only as each bead's agent-process host, see
// agentHostFor.
func (a *app) runTUI(ctx context.Context, versionString string) error {
	cfg, err := a.loadConfig(a.dir)
	theme := "dark"
	baseBranch := "main"
	// idleThreshold's fallback matches store.Config's own default
	// (general.silent_death_threshold, config.go) so a failed config load
	// degrades to the same value a fresh init would have, not to disabled.
	idleThreshold := 30 * time.Minute
	var agentHost tui.AgentHost
	if err == nil {
		theme = cfg.TUI.Theme
		baseBranch = cfg.General.BaseBranch
		agentHost = a.agentHostFor(ctx, cfg)
		idleThreshold = time.Duration(cfg.General.SilentDeathThreshold) * time.Minute
	}
	deps := tui.Deps{
		HunkComments: a.hunkDeps(ctx),
		IdleResume: func(brn string) (string, error) {
			return a.isolatedCall(func(sub *app) error {
				return sub.IdleResumeRun(ctx, domain.BRN(brn))
			})
		},
		IdleThreshold: idleThreshold,
		Beads:         a.beads,
		Runs:          a.runs,
		Audit:         a.audit,
		LoadConfig:    a.loadConfig,
		Dir:           a.dir,
		// Models backs the assign-model picker ('a', and the status menu's
		// open->assigned target): see assignableModels.
		Models: func() ([]string, error) {
			return a.assignableModels(ctx)
		},
		// EffortChoices backs the picker's follow-up effort step: see
		// effortChoicesFor.
		EffortChoices: func(picked string) ([]string, error) {
			return a.effortChoicesFor(ctx, picked)
		},
		NoColor:    a.noColor,
		Theme:      theme,
		BaseBranch: baseBranch,
		// ReadRunSummary/WriteRunSummary persist a bead's Agent-tab output
		// (the embedded terminal's final frame) across restarts, since
		// m.sessions lives only in memory: see agent_log.go.
		ReadRunSummary: func(brn string) ([]string, error) {
			return a.readRunSummary(domain.BRN(brn))
		},
		WriteRunSummary: func(brn string, lines []string) error {
			return a.writeRunSummary(domain.BRN(brn), lines)
		},
		HeaderStats: func(brn string) (tui.HeaderStats, error) {
			return a.headerStats(ctx, brn)
		},
		// Worktrees backs the embedded Agent-tab terminal: pressing 'r'
		// spawns the bead's agent in its own worktree.
		Worktrees: a.worktrees,
		AgentHost: agentHost,
		// Isolated the same way Merge/Resume are (see isolatedCall's doc
		// comment) — and for the same reason: Reconcile calls a.warn on
		// every per-bead failure it swallows and continues past (dead tmux
		// window, a transient bd/git error), and a.warn writes straight to
		// a.errOut, which for the process actually running the TUI is real
		// os.Stderr (see newApp's call site). Calling a.Reconcile directly
		// here — as this did before — meant every one of those warnings
		// wrote raw text under the TUI's alt-screen buffer, corrupting the
		// display outside bubbletea's rendering entirely. A fresh sub-app
		// with its own buffers absorbs them; the pass's real result (the
		// report, and any error that aborts it outright — bad config, `bd
		// list` failing — as opposed to one bead's reconcile step failing)
		// still comes back and reaches reconcileMsg's own toast/log path
		// same as before.
		Reconcile: func(ctx context.Context, skip map[string]bool) (tui.ReconcileSummary, error) {
			return a.tuiReconcile(ctx, skip)
		},
		// CreateBead/ChangeStatus/Assign/CloseBead/Comment call cobra-free
		// typed methods directly. Comment (AddComment) and EditBead are
		// genuinely print-free — checked, not assumed: neither calls
		// a.warn anywhere in its body. CreateBead/ChangeStatus/Assign/
		// CloseBead are NOT: CreateBead/Assign can hit loadAgents' agent-
		// cache warning, Assign also warnEffort's two, ChangeStatus and
		// CloseBead both call killBeadWindow (tmux kill-window warning) or
		// autoCloseEpic (epic auto-close warning) on ordinary paths, not
		// edge cases — closing a bead, or setting status to closed, routes
		// through one of these whenever a tmux window needs killing or an
		// epic auto-closes. a.warn writes straight to a.errOut, which for
		// the process actually running the TUI is real os.Stderr (see
		// newApp's call site) — so each of those, called directly on the
		// live app, printed raw text under the TUI's alt-screen buffer,
		// corrupting the display the same way Reconcile's warnings did
		// (see that Deps field's comment). isolatedApp() is the fix: same
		// mechanism Merge/Resume already used for their own print calls.
		// alwaysApprove skips their confirm parameter unconditionally — the
		// TUI's own screen (the picker, the delete-twice confirm) is
		// already the real approval, never a second prompt underneath it.
		CreateBead: func(title, description, accept, priority, issueType, parent, tier string) (string, error) {
			return a.tuiCreateBead(tuiCreateBeadInput{ctx: ctx, title: title, description: description, accept: accept, priority: priority, issueType: issueType, parent: parent, tier: tier})
		},
		ChangeStatus: func(brn, target string) (string, error) {
			return a.tuiChangeStatus(ctx, brn, target)
		},
		Assign: func(brn, agentName, modelID, effort string) (string, error) {
			return a.tuiAssign(ctx, brn, agentName, modelID, effort)
		},
		CloseBead: func(brn string) (string, error) {
			return a.tuiClose(ctx, brn)
		},
		Comment: func(brn, text string) (string, error) {
			res, err := a.AddComment(ctx, domain.BRN(brn), text)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("commented on %s", res.BRN), nil
		},
		EditBead: func(brn, title, description string) (string, error) {
			res, err := a.EditBead(ctx, domain.BRN(brn), title, description)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("updated %s", res.BRN), nil
		},
		// Merge/Resume run pipeline code (runMerge/ResumeRun) that still
		// prints through a.out/a.warn and, for Merge, would otherwise
		// block on stdin — isolatedCall runs them against a fresh sub-app
		// with its own buffers and inTTY forced false, capturing the
		// output instead of leaking it and skipping any confirmation
		// prompt (pressing 'm' on a mergable bead already is the human's
		// approval; sub.yes=true + confirmMerge=true reproduces exactly
		// what --yes --confirm-merge did on the old string-arg path).
		Merge: func(brn string) (string, error) {
			return a.isolatedCall(func(sub *app) error {
				sub.yes = true
				return sub.runMerge(ctx, brn, true)
			})
		},
		Resume: func(brn string) (string, error) {
			return a.isolatedCall(func(sub *app) error {
				return sub.ResumeRun(ctx, domain.BRN(brn))
			})
		},
		// Personas/Dispatch/... back the Crew Roster (P key,
		// docs/PRD/crew-mode.md). All isolated the same way
		// CreateBead/ChangeStatus/etc are: loading personas can hit
		// persona.LoadAll's first-run seeding, and launching one calls
		// a.warn on a resolution/tmux failure — either would otherwise
		// write straight to a.errOut, corrupting the TUI's alt-screen (see
		// Reconcile's field comment above for the full story).
		Personas: func() ([]persona.Persona, error) {
			return a.isolatedApp().loadPersonas()
		},
		UpdatePersona: func(f persona.FormFields) error {
			return a.isolatedApp().UpdatePersonaByID(f)
		},
		DeletePersona: func(id string) error {
			return a.isolatedApp().DeletePersonaByID(id)
		},
		// CreatePersona backs Prompt Mode's Personas-tab 'n' (v7 redesign,
		// docs/PRD/crew-mode.md) — the only persona-create path in the app.
		CreatePersona: func(f persona.FormFields) (string, error) {
			return a.isolatedApp().CreatePersonaByFields(f)
		},
		// PromptAgentIDs/PromptAgentEnsure back Prompt Mode's left pane: a
		// tabbed, live tmux-attached agent-CLI chat, one tab per active
		// agent, bd-scoped, running in the main checkout at its current
		// branch. Isolated the same way as every other Deps closure here.
		PromptAgentIDs: func() ([]string, error) {
			return a.isolatedApp().PromptAgentIDs()
		},
		PromptAgentEnsure: func(agentID string) (string, bool, error) {
			return a.isolatedApp().PromptAgentEnsure(ctx, agentID)
		},
		FireNow: func(id string) error {
			return a.isolatedApp().FireNow(ctx, id)
		},
		PersonaActivity: func(id string) ([]store.AuditEvent, error) {
			return a.isolatedApp().PersonaActivity(id)
		},
		PersonaOutput: func(id string) (string, error) {
			return a.isolatedApp().PersonaOutput(ctx, id)
		},
		PersonaStatuses: func(ids []string) (map[string]tui.PersonaStatus, error) {
			statuses, err := a.isolatedApp().PersonaStatuses(ctx, ids)
			if err != nil {
				return nil, err
			}
			out := make(map[string]tui.PersonaStatus, len(statuses))
			for id, s := range statuses {
				out[id] = tui.PersonaStatus{Running: s.Running, Since: s.Since}
			}
			return out, nil
		},
		// Root().Version is the full "<ver> (commit: x, built: y)" string
		// baked in for `baron --version`; the header bar is a persistent
		// one-line status strip, not a version report, so it gets just the
		// bare version token — the full string either eats most of the
		// header's width or gets dropped entirely on a narrower terminal.
		Version: shortVersion(versionString),
	}
	return a.tuiRun(ctx, deps, a.in, a.out)
}

func (a *app) tuiReconcile(ctx context.Context, skip map[string]bool) (tui.ReconcileSummary, error) {
	var out, errOut bytes.Buffer
	sub := a.newIsolatedApp(&out, &errOut)
	report, err := sub.Reconcile(ctx, skip)
	return toTUIReconcileSummary(report), err
}

type tuiCreateBeadInput struct {
	ctx                                                           context.Context
	title, description, accept, priority, issueType, parent, tier string
}

func (a *app) tuiCreateBead(p tuiCreateBeadInput) (string, error) {
	res, err := a.isolatedApp().CreateBead(p.ctx, CreateBeadInput{Title: p.title, Description: p.description, Accept: p.accept, Priority: p.priority, IssueType: p.issueType, Parent: p.parent, Tier: p.tier})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("created %s: %s", res.BRN, res.Title), nil
}

func (a *app) tuiChangeStatus(ctx context.Context, brn, target string) (string, error) {
	res, err := a.isolatedApp().ChangeStatus(ctx, domain.BRN(brn), domain.BeadState(target), a.systemActor())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("updated %s to %s", res.BRN, res.To), nil
}

func (a *app) tuiAssign(ctx context.Context, brn, agentName, modelID, effort string) (string, error) {
	res, ok, err := a.isolatedApp().AssignBead(ctx, domain.BRN(brn), modelID, agentName, effort, alwaysApprove)
	if err != nil || !ok {
		return "", err
	}
	return fmt.Sprintf("assigned %s to %s", res.BRN, assignLabel(res.Agent, res.Model)), nil
}

func (a *app) tuiClose(ctx context.Context, brn string) (string, error) {
	res, ok, err := a.isolatedApp().CloseBead(ctx, domain.BRN(brn), alwaysApprove)
	if err != nil || !ok {
		return "", err
	}
	return fmt.Sprintf("closed %s", res.BRN), nil
}

// shortVersion returns the bare version token off the front of a cobra
// "<ver> (commit: x, built: y)" version string.
func shortVersion(v string) string {
	head, _, _ := strings.Cut(v, " ")
	return head
}

// alwaysApprove is the confirm func for TUI-triggered actions whose real
// approval already happened at a TUI screen (a picker selection, a
// press-twice-to-delete) — passed to AssignBead/CloseBead so they don't
// block on a second, redundant confirmation nothing would ever answer.
func alwaysApprove(string) (bool, error) { return true, nil }

// agentHostFor builds the TUI's tui.AgentHost when tmux is enabled
// (config tui.tmux != "never", the same gate baron run's headless path
// uses) and installed — nil otherwise, which falls the embedded terminal
// back to a bare pty with no persistence across a BARON restart. A failed
// config load (cfg == nil) degrades the same way, matching runTUI's own
