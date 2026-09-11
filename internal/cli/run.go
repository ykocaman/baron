package cli

import (
	"context"
	"fmt"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// gateReportJSON is the JSON shape of a `baron run --gate` report.
type gateReportJSON struct {
	BRN     domain.BRN           `json:"brn"`
	Profile string               `json:"profile"`
	Success bool                 `json:"success"`
	Steps   []gateStepResultJSON `json:"steps"`
}

// gateStepResultJSON is the JSON shape of one gate step's result.
type gateStepResultJSON struct {
	Name     string `json:"name"`
	Command  string `json:"command,omitempty"`
	Success  bool   `json:"success"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
	Duration string `json:"duration"`
}

// mergeBaseFor returns the branch bead's own branch merges into: its
// parent's branch for a hierarchical child (bd's dotted-id or explicit
// Parent link — see store.ParentOf), so a subtree integrates into its
// parent before that parent ever reaches the configured base branch; ""
// for anything without a parent, meaning the caller falls back to the
// configured base branch itself.
func (a *app) mergeBaseFor(ctx context.Context, bead store.Bead) (string, error) {
	all, err := a.beads.List(ctx)
	if err != nil {
		return "", err
	}
	parent, ok := store.ParentOf(bead, all)
	if !ok {
		return "", nil
	}
	return domain.BranchName(parent.IssueType, parent.ID), nil
}

// StartRun resolves brn to its bead, agent and worktree, and runs it —
// cobra-free equivalent of plain `baron run <bead>` (no --gate/--resume).
// Used by newRunCmd's RunE and by the reconciler's opt-in retry relaunch
// (see reconcile.go); a bead with no assignee or an agent missing from the
// registry fails here with the same error resolveAgent already gives a
// human running `baron run` by hand.
func (a *app) StartRun(ctx context.Context, brn domain.BRN) error {
	bead, err := a.findBead(ctx, brn, string(brn))
	if err != nil {
		return err
	}
	parentBranch, err := a.mergeBaseFor(ctx, bead)
	if err != nil {
		return err
	}
	// Resolve the agent before touching the worktree: a bead that isn't
	// assigned, or assigned to an agent missing from the registry,
	// shouldn't leave a worktree/branch behind.
	ag, err := a.resolveAgent(ctx, bead)
	if err != nil {
		return err
	}
	wt, err := a.worktrees.Create(ctx, a.idOf(brn), bead.IssueType, parentBranch)
	if err != nil {
		return err
	}
	return a.runBead(ctx, brn, bead, ag, wt)
}

// ResumeRun runs the post-agent pipeline (ask-check, validate, gate,
// ready-for-merge) for a bead whose agent already ran outside baron's own
// launch loop — cobra-free equivalent of `baron run <bead> --resume`. See
// runResume's own doc comment for why this path forces the retry budget to
// 0 (a gate failure goes straight to human_queue).
func (a *app) ResumeRun(ctx context.Context, brn domain.BRN) error {
	bead, err := a.findBead(ctx, brn, string(brn))
	if err != nil {
		return err
	}
	parentBranch, err := a.mergeBaseFor(ctx, bead)
	if err != nil {
		return err
	}
	wt, err := a.worktrees.Create(ctx, a.idOf(brn), bead.IssueType, parentBranch)
	if err != nil {
		return err
	}
	return a.runResume(ctx, brn, bead, wt, "agent-exited", false)
}

// IdleResumeRun is ResumeRun's idle-triggered sibling — cobra-free
// equivalent of nothing (there is no `baron` subcommand for this; the only
// caller is the TUI's checkIdleAgents, via Deps.IdleResume). Same pipeline,
// same forced-0 retry budget, but parkIfNoChange=true: see postAgentGate's
// doc comment for why "idle with nothing changed" must not land the bead
// back in a silent no-op the way a real process exit's equivalent case
// does.
func (a *app) IdleResumeRun(ctx context.Context, brn domain.BRN) error {
	bead, err := a.findBead(ctx, brn, string(brn))
	if err != nil {
		return err
	}
	parentBranch, err := a.mergeBaseFor(ctx, bead)
	if err != nil {
		return err
	}
	wt, err := a.worktrees.Create(ctx, a.idOf(brn), bead.IssueType, parentBranch)
	if err != nil {
		return err
	}
	return a.runResume(ctx, brn, bead, wt, "idle-detected", true)
}

// runResume is the post-agent pipeline
// for a bead whose agent ran outside baron's own launch loop — namely the
// TUI's embedded terminal ('r'), where a human was driving the session
// directly and may still be. The interactive process exiting (see
// terminal.go's agentExitMsg) is the trigger; this is what actually
// resolves the bead out of "working" once it does, since the embedded
// terminal itself never ran a gate. budget is forced to 0 (no relaunch):
// unlike the headless retry loop, there is no prompt to relaunch with here
// that wouldn't just silently restart a session the human was present for
// — a gate failure goes straight to the human queue so a person decides
// whether to press 'r' again, not a background loop.
//
// trigger/parkIfNoChange thread straight through to postAgentGate — see
// its own doc comment. ResumeRun (a real process exit) and IdleResumeRun
// (checkIdleAgents noticing the session went quiet) are this function's
// two callers, differing only in these two values.
func (a *app) runResume(ctx context.Context, brn domain.BRN, bead store.Bead, wt domain.Worktree, trigger string, parkIfNoChange bool) error {
	state := resolveDomainState(bead)
	attempt := 0
	outcome, _, _, err := a.postAgentGate(ctx, postAgentGateParams{
		brn: brn, bead: bead, wt: wt, state: state, attempt: &attempt, budget: 0, trigger: trigger, parkIfNoChange: parkIfNoChange,
	})
	if err != nil {
		return err
	}
	switch outcome {
	case postAgentBlocked, postAgentRetryExhausted:
		return silentError{code: ExitHuman}
	default:
		return nil
	}
}

// resolveAgent looks up bead's assigned agent in the registry, falling
// back to an on-demand PATH probe so an agent assigned before `baron
// doctor` ran still launches when its CLI is installed.
//
// bead.Assignee is always the bare agent name (e.g. "claude", "opencode").
// The model and the reasoning-effort level are separate fields
// (bead.Model(), bead.Effort(), set by `work assign`), injected here as
// that CLI's own flags — see agent.Agent.Invocation, which knows each
// CLI's flag names and where in its argument list they have to go.
func (a *app) resolveAgent(ctx context.Context, bead store.Bead) (agent.Agent, error) {
	if bead.Assignee == "" {
		return agent.Agent{}, fmt.Errorf("bead %s is not assigned; run `baron work assign %s --model <id>`", bead.BRN, bead.BRN)
	}
	a.loadAgents()
	ag, ok := a.agents.Get(bead.Assignee)
	if !ok {
		var err error
		ag, err = agent.NewProbe(a.runner).ProbeSingle(ctx, bead.Assignee)
		if err != nil {
			return agent.Agent{}, fmt.Errorf("agent %q is not in the registry; run `baron doctor` to discover agents", bead.Assignee)
		}
		a.agents.Add(ag)
	}
	ag.Args = ag.Invocation(bead.Model(), bead.Effort())
	return ag, nil
}

// opencodeFailureReason extracts the last provider failure from the newest
// opencode session log (~/.local/share/opencode/log). A rate limit shows up
// there ("message=\"stream error\" ... Rate limit exceeded") even though the
// agent window only renders the generic "Error: Aborted" — appending the log
// line to the retry reason makes the real cause visible in the audit trail.
