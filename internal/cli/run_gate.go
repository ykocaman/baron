package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

type postAgentOutcome int

const (
	// postAgentDone means the bead needs no further action from the
	// caller: either it was marked ready to merge (and auto-merged, if
	// policy allowed it), or there was nothing to validate (no unstaged
	// changes).
	postAgentDone postAgentOutcome = iota
	// postAgentBlocked means the bead was parked in the human queue for a
	// reason the retry budget has no bearing on: the agent asked a
	// question, a secret was found, or a commit was unsigned.
	postAgentBlocked
	// postAgentRetryExhausted means the gate failed and the retry budget
	// is spent: the bead was parked in the human queue.
	postAgentRetryExhausted
	// postAgentRetry means the gate failed but the retry budget allows
	// another attempt: the bead is in BeadStateRetry, and the caller
	// should relaunch the agent with gateSummary appended to its prompt.
	postAgentRetry
)

// postAgentGateParams bundles postAgentGate's inputs — bundled rather than
// passed positionally since the natural parameter list (brn, bead, wt,
// state, attempt, budget, trigger, parkIfNoChange) pushes the count past 7.
type postAgentGateParams struct {
	brn   domain.BRN
	bead  store.Bead
	wt    domain.Worktree
	state domain.BeadState
	// attempt is the caller's retry counter, shared across calls in a retry
	// loop; budget is gate.RetryBudget, or 0 to skip retrying entirely (e.g.
	// for a bead the human is already driving interactively via the
	// embedded terminal — see `baron run --resume`) and go straight to the
	// human queue on the first gate failure instead of relaunching in the
	// background.
	attempt        *int
	budget         int
	trigger        string
	parkIfNoChange bool
}

// postAgentGate runs the pipeline after one agent run completes: an
// [ask]-prefixed comment parks the bead in the human queue (nothing to
// validate until a human answers); no unstaged changes means nothing to
// validate at all; otherwise the bead moves to validating and the gate
// runs — success marks the bead ready to merge (and auto-merges it, if policy allows),
// failure retries (relaunching with the gate's failure summary appended)
// until budget runs out, at which point the bead parks in the human queue.
func (a *app) postAgentGate(ctx context.Context, p postAgentGateParams) (outcome postAgentOutcome, newState domain.BeadState, gateSummary string, err error) {
	brn, bead, wt, state := p.brn, p.bead, p.wt, p.state
	attempt, budget, trigger, parkIfNoChange := p.attempt, p.budget, p.trigger, p.parkIfNoChange
	id := a.idOf(brn)

	if outcome, newState, handled, err := a.checkAgentAsked(ctx, id, brn, state, trigger); handled {
		return outcome, newState, "", err
	}

	changed, outcome, newState, handled, err := a.checkNoOpRun(ctx, brn, wt, state, trigger, parkIfNoChange)
	if handled {
		return outcome, newState, "", err
	}

	if err := a.transitionStatus(ctx, id, state, domain.BeadStateValidating, a.systemActor()); err != nil {
		return postAgentDone, state, "", err
	}
	// Audited (unlike most transitionStatus calls in this file, which rely
	// on the eventual "gate"/"retry"/"mergable" entry to imply when the
	// prior state ended) because the reconciler needs a reliable "when did
	// validating start" timestamp to judge staleness — bead.UpdatedAt gets
	// bumped by unrelated things (a comment landing) and can't be trusted
	// alone.
	a.auditLog("status", string(brn), fmt.Sprintf("%s -> validating (%s)", state, trigger))
	state = domain.BeadStateValidating
	report, err := a.runGateReport(ctx, brn, wt)
	if err != nil {
		return postAgentDone, state, "", err
	}
	if report.Success {
		return a.handleGateSuccess(ctx, gateSuccessParams{
			brn: brn, bead: bead, wt: wt, state: state, changed: changed, attempt: attempt, budget: budget,
		})
	}

	exhausted, retryState, err := a.recordFailure(ctx, brn, state, attempt, budget, "gate failed", a.systemActor())
	if err != nil {
		return postAgentDone, state, "", err
	}
	if exhausted {
		return postAgentRetryExhausted, retryState, "", nil
	}
	return postAgentRetry, retryState, gateFailureSummary(report), nil
}

// checkAgentAsked handles postAgentGate's "did the agent leave an [ask]
// comment" check: parking the bead in the human queue is a fully resolved
// outcome (handled=true), needing nothing else from the caller.
func (a *app) checkAgentAsked(ctx context.Context, id string, brn domain.BRN, state domain.BeadState, trigger string) (outcome postAgentOutcome, newState domain.BeadState, handled bool, err error) {
	asked, err := a.agentAsked(ctx, id)
	if err != nil {
		return postAgentDone, state, true, err
	}
	if !asked {
		return postAgentDone, state, false, nil
	}
	if err := a.transitionStatus(ctx, id, state, domain.BeadStateHumanQueue, a.systemActor()); err != nil {
		return postAgentDone, state, true, err
	}
	detail := fmt.Sprintf("agent asked a question — parked in the human queue (%s)", trigger)
	a.auditLog("ask", string(brn), detail)
	a.outf("%s\n", detail)
	return postAgentBlocked, domain.BeadStateHumanQueue, true, nil
}

// checkNoOpRun handles postAgentGate's "did this run leave anything to
// validate" check. A prompt that left the branch exactly as it found it has
// nothing to validate: skip the gate entirely instead of burning a full
// format/lint/tidy/test/build+secret-scan pass on a no-op. "As it found it"
// means both halves — a clean worktree *and* no commits ahead of the base
// branch. An agent that commits its own work (docs/PRD/agent-contract.md §7
// lets it) leaves a clean worktree with real work in it; treating that as a
// no-op would skip the gate, the secret scan and marking it ready to merge,
// and strand the bead in "working".
//
// parkIfNoChange flips what "nothing to validate" means: a real process
// exit (trigger="agent-exited") leaving no diff is a legitimate no-op — a
// read-only investigation, or a `--resume` on a session that already
// committed everything itself — so the bead just stays "working" for a
// human or a later run to pick up. An idle-detected trigger finding the
// exact same "nothing changed" is a different signal entirely: nobody
// confirmed the agent is actually done, it simply stopped producing any
// output at all. Silently doing nothing there is indistinguishable from the
// bead being permanently stuck — the case a human most needs to see — so
// this path parks it in human_queue instead of no-op'ing.
//
// handled=false means there was something to validate: changed carries the
// unstaged file list postAgentGate's caller needs later, for staging once
// the gate passes.
func (a *app) checkNoOpRun(ctx context.Context, brn domain.BRN, wt domain.Worktree, state domain.BeadState, trigger string, parkIfNoChange bool) (changed []string, outcome postAgentOutcome, newState domain.BeadState, handled bool, err error) {
	changed, err = tool.UnstagedChanges(ctx, a.runner, wt.Path)
	if err != nil {
		return nil, postAgentDone, state, true, err
	}
	ahead, err := a.commitsAhead(ctx, wt)
	if err != nil {
		return nil, postAgentDone, state, true, err
	}
	if len(changed) != 0 || ahead != 0 {
		return changed, postAgentDone, state, false, nil
	}
	if parkIfNoChange {
		outcome, newState, _, err := a.parkOnError(ctx, brn, state, "idle", fmt.Errorf("no changes or new commits (%s)", trigger))
		return nil, outcome, newState, true, err
	}
	a.outf("no changes and no new commits after the run — gates skipped\n")
	return nil, postAgentDone, state, true, nil
}

// gateSuccessParams bundles handleGateSuccess's inputs — bundled rather than
// passed positionally since changed (the pre-gate unstaged file list, needed
// again here for staging) pushes the natural parameter count past 7.
type gateSuccessParams struct {
	brn     domain.BRN
	bead    store.Bead
	wt      domain.Worktree
	state   domain.BeadState
	changed []string
	attempt *int
	budget  int
}

// handleGateSuccess is postAgentGate's "gate passed" tail: stage the
// changes that triggered it, scan for secrets, commit, verify the commit is
// signed, run the review gate, and — once every check clears — mark the
// bead ready to merge and try auto-merging it.
func (a *app) handleGateSuccess(ctx context.Context, p gateSuccessParams) (postAgentOutcome, domain.BeadState, string, error) {
	brn, bead, wt, state := p.brn, p.bead, p.wt, p.state
	// The changes that triggered the gate passed: stage them so the
	// branch's next prompt run starts from a clean diff baseline.
	if err := tool.StageChanges(ctx, a.runner, wt.Path, p.changed); err != nil {
		return postAgentDone, state, "", err
	}
	if len(p.changed) > 0 {
		a.outf("staged %d file(s)\n", len(p.changed))
	}
	// Scan before committing, not after: the staged tree is exactly what
	// the commit will contain, and a secret caught here never enters the
	// branch's history in the first place.
	blocked, err := a.scanSecrets(ctx, brn, state, wt)
	if err != nil {
		return postAgentDone, state, "", err
	}
	if blocked {
		return postAgentBlocked, domain.BeadStateHumanQueue, "", nil
	}
	// From here on the bead is in "validating", a state with no edge back
	// to "working": a raw error return would leave it there permanently,
	// and every later `baron run` on it would die with "invalid bead
	// transition validating -> working". A commit failure is an
	// infrastructure problem a person has to fix (e.g. a pre-commit hook
	// rejecting the tree), so it parks the bead in the human queue — the
	// same landing spot as a secret finding, and one that can be re-run
	// once the cause is fixed.
	if err := a.commitWork(ctx, brn, bead, wt); err != nil {
		return a.parkOnError(ctx, brn, state, "commit", err)
	}
	blocked, err = a.checkSignedCommits(ctx, brn, state, wt)
	if err != nil {
		return postAgentDone, state, "", err
	}
	if blocked {
		return postAgentBlocked, domain.BeadStateHumanQueue, "", nil
	}
	// One more gate step, same synchronous pipeline as the profile gate
	// just above — see reviewGate's own doc comment for why this can't
	// be an async persona/comment signal instead: nothing this late in
	// the pipeline could deterministically stop the auto-merge two
	// lines below from racing past it.
	if reviewPass, reviewReason, err := a.reviewGate(ctx, brn, bead, wt); err != nil {
		return postAgentDone, state, "", err
	} else if !reviewPass {
		exhausted, retryState, err := a.recordFailure(ctx, brn, state, p.attempt, p.budget, "review: "+reviewReason, a.systemActor())
		if err != nil {
			return postAgentDone, state, "", err
		}
		if exhausted {
			return postAgentRetryExhausted, retryState, "", nil
		}
		return postAgentRetry, retryState, reviewRetrySummary(reviewReason), nil
	}
	if err := a.readyForMerge(ctx, brn, state, wt); err != nil {
		return a.parkOnError(ctx, brn, state, "mergable", err)
	}
	state = domain.BeadStateMergable
	if err := a.tryAutoMerge(ctx, brn, state, wt); err != nil {
		return postAgentDone, state, "", err
	}
	a.printDiff(ctx, wt.Path)
	return postAgentDone, state, "", nil
}

// parkOnError parks a bead in the human queue after a post-gate step failed
// for a reason the retry budget can't help with (nothing for the model to
// fix — the remote, the push or gh is what broke), and reports the cause on
// stdout and in the audit trail. The retry budget is deliberately untouched,
// matching how a secret finding and an unsigned commit are handled.
func (a *app) parkOnError(ctx context.Context, brn domain.BRN, from domain.BeadState, action string, cause error) (postAgentOutcome, domain.BeadState, string, error) {
	id := a.idOf(brn)
	if err := a.transitionStatus(ctx, id, from, domain.BeadStateHumanQueue, a.systemActor()); err != nil {
		// The park itself failed: report the original cause, which is the
		// one the user needs, rather than the bookkeeping error.
		a.warn("park in human queue: %v", err)
		return postAgentDone, from, "", cause
	}
	detail := fmt.Sprintf("%s failed: %v — parked in human queue", action, cause)
	a.auditLog(action, string(brn), detail)
	a.outf("%s\n", detail)
	return postAgentBlocked, domain.BeadStateHumanQueue, "", nil
}

// commitsAhead counts the commits wt's branch has that the base branch does
// not. A base ref that doesn't resolve (project mid-setup, base branch not
// created yet) counts as 0 with a warning rather than an error: the
// uncommitted-changes signal still carries the run on its own.
func (a *app) commitsAhead(ctx context.Context, wt domain.Worktree) (int, error) {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return 0, err
	}
	base := cfg.General.BaseBranch
	n, known, err := tool.CommitsAhead(ctx, a.runner, wt.Path, base)
	if err != nil {
		return 0, err
	}
	if !known {
		a.warn("cannot resolve %s — counting the branch as having no new commits", base)
		return 0, nil
	}
	return n, nil
}

// commitWork turns the gate-approved, secret-scanned staged tree into a
// commit. An agent that committed its own work (docs/PRD/agent-contract.md
// §7) leaves nothing staged, and that is a success, not a failure — its
// commits are what the signed-commit check and the merge then run against.
// Signing is the repository's own git config; BARON never generates or
// stores keys (docs/PRD/security.md §4).
func (a *app) commitWork(ctx context.Context, brn domain.BRN, bead store.Bead, wt domain.Worktree) error {
	sha, err := tool.Commit(ctx, a.runner, wt.Path, commitMessage(brn, bead))
	if errors.Is(err, tool.ErrNothingStaged) {
		return nil
	}
	if err != nil {
		return err
	}
	detail := fmt.Sprintf("committed %s", shortSHA(sha))
	a.auditLog("commit", string(brn), detail)
	a.outf("%s\n", detail)
	return nil
}

// commitMessage builds the commit BARON writes for a bead: a conventional
// subject derived from the bead's issue type, and a trailer naming the bead
// so a commit found later in history points back at the work item.
func commitMessage(brn domain.BRN, bead store.Bead) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", commitType(bead.IssueType), bead.Title)
	if desc := strings.TrimSpace(bead.Description); desc != "" {
		fmt.Fprintf(&b, "\n%s\n", desc)
	}
	fmt.Fprintf(&b, "\nBead: %s\n", brn)
	return b.String()
}

// commitType maps a bd issue type to its conventional-commit prefix.
// Unknown types fall back to "chore" rather than being passed through, so a
// project's custom bd types can't produce a malformed subject line.
func commitType(issueType string) string {
	switch strings.ToLower(strings.TrimSpace(issueType)) {
	case "feature":
		return "feat"
	case "bug":
		return "fix"
	case "task", "chore", "decision", "epic", "":
		return "chore"
	default:
		return "chore"
	}
}

// shortSHA abbreviates a commit SHA for human-facing output.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// askMarker prefixes a comment an agent adds when it needs human input.
const askMarker = "[ask]"

// agentAsked reports whether the bead's latest comment is a question from
// the agent ([ask] prefix). Agents can't change bead status (agent-contract
// §7), so the ask signal rides in the comment thread.
