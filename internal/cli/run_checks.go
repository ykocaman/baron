package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

func (a *app) agentAsked(ctx context.Context, id string) (bool, error) {
	comments, err := a.beads.Comments(ctx, id)
	if err != nil {
		return false, err
	}
	for _, comment := range slices.Backward(comments) {
		text := strings.TrimSpace(comment.Text)
		if text == "" {
			continue
		}
		return strings.HasPrefix(text, askMarker), nil
	}
	return false, nil
}

// recordFailure applies one failed attempt (gate failure or silent death)
// against the retry budget: either move the bead toward retry and bump
// *attempt, or exhaust the budget and park it in the human queue. Returns
// the bead's new domain state so the caller's tracked state stays accurate.
// bd's status field has no retry/human_queue states of its own
// (baronStatusNames collapses them into in_progress/open), so a comment
// carries the real reason even though the status alone can't distinguish it.
func (a *app) recordFailure(ctx context.Context, brn domain.BRN, from domain.BeadState, attempt *int, budget int, reason string, actor domain.Actor) (exhausted bool, newState domain.BeadState, err error) {
	id := a.idOf(brn)
	if domain.NextRetry(*attempt, budget) == domain.RetryExhausted {
		if err := a.transitionStatus(ctx, id, from, domain.BeadStateHumanQueue, actor); err != nil {
			return false, from, err
		}
		detail := fmt.Sprintf("retry budget (%d) exhausted: parked in human queue (%s)", budget, reason)
		a.auditLogAs(storeActor(actor), "retry", string(brn), detail)
		// The reason travels with the message. Without it the whole run reads
		// as "retry budget exhausted" and nothing else — the actual cause (a
		// model over its quota, an agent CLI that won't start) is knowable
		// only by opening the audit log, which is not where someone watching
		// a run is looking.
		a.outf("retry budget exhausted (%d): %s parked in the human queue — %s\n", budget, brn, reason)
		return true, domain.BeadStateHumanQueue, nil
	}
	*attempt++
	// validating -> retry is the only retry edge  defines;
	// a silent death (from "working") has no such edge — the model is just
	// relaunched, and the bead stays "working" the whole time.
	newState = from
	if from == domain.BeadStateValidating {
		if err := a.transitionStatus(ctx, id, from, domain.BeadStateRetry, actor); err != nil {
			return false, from, err
		}
		newState = domain.BeadStateRetry
	}
	detail := fmt.Sprintf("retry %d/%d: %s", *attempt, budget, reason)
	a.auditLogAs(storeActor(actor), "retry", string(brn), detail)
	a.outf("%s\n", detail)
	return false, newState, nil
}

// runGateReport runs the project's gate profile inside wt, prints the
// report, and audits the result.
func (a *app) runGateReport(ctx context.Context, brn domain.BRN, wt domain.Worktree) (domain.GateReport, error) {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return domain.GateReport{}, err
	}
	profile := profileByName(cfg, cfg.General.Profile, wt.Path)

	report, err := a.gate.Run(ctx, wt.Path, profile)
	if err != nil {
		return domain.GateReport{}, err
	}
	a.auditLog("gate", string(brn), gateAuditDetail(report))

	if a.json {
		if err := printJSON(a.out, toGateReportJSON(brn, report)); err != nil {
			return domain.GateReport{}, err
		}
	} else {
		a.printGateReport(report)
	}
	return report, nil
}

// scanSecrets runs gitleaks on wt once the gate passes (
// step 1: "Kapı geçer → secret scan çalışır"). A finding or a run error
// blocks the merge and parks the bead in the human queue directly — unlike a
// gate failure, a secret scan result never spends the retry budget
// (: "Gitleaks asla otomatik bir onay/merge mekanizmasının
// parçası olmaz"); there's nothing for the model to retry against a secret.
func (a *app) scanSecrets(ctx context.Context, brn domain.BRN, from domain.BeadState, wt domain.Worktree) (blocked bool, err error) {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return false, err
	}
	if !cfg.Gate.Gitleaks.Enabled {
		return false, nil
	}
	reportDir := filepath.Join(a.dir, filepath.Dir(cfg.Gate.Gitleaks.ReportPath))
	if err := os.MkdirAll(reportDir, 0o750); err != nil {
		return false, err
	}
	base := cfg.General.BaseBranch
	res := tool.ScanAll(ctx, a.runner, wt.Path, base, tool.GitleaksOptions{
		ReportDir:    reportDir,
		Redact:       cfg.Gate.Gitleaks.Redact,
		BaselinePath: cfg.Gate.Gitleaks.BaselinePath,
	})
	if res.Clean {
		a.auditLog("secret_scan", string(brn), "secret scan passed: no findings")
		return false, nil
	}

	id := a.idOf(brn)
	if err := a.transitionStatus(ctx, id, from, domain.BeadStateHumanQueue, a.systemActor()); err != nil {
		return false, err
	}
	detail := secretScanDetail(res)
	a.auditLog("secret_scan", string(brn), detail)
	a.outf("%s\n", detail)
	return true, nil
}

// secretScanDetail summarizes a gitleaks scan result for the audit trail,
// distinguishing a finding from a run error the way  does
// (both block, but they're different kinds of event).
func secretScanDetail(res tool.ScanResult) string {
	if res.RunError != "" {
		return fmt.Sprintf("secret scan error (%s): %s — merge blocked, parked in human queue", res.Target, res.RunError)
	}
	names := make([]string, len(res.Findings))
	for i, f := range res.Findings {
		names[i] = f.RuleID
	}
	return fmt.Sprintf("secret scan found %d finding(s) in %s (%s) — merge blocked, parked in human queue",
		len(res.Findings), res.Target, strings.Join(names, ", "))
}

// checkSignedCommits verifies every commit the bead's branch adds over the
// base branch is signed.
// Like a secret finding, an unsigned commit parks the bead in the human
// queue directly, without spending the retry budget: there's no gate step
// for the model to fix, only a commit to re-sign or a config to change.
func (a *app) checkSignedCommits(ctx context.Context, brn domain.BRN, from domain.BeadState, wt domain.Worktree) (blocked bool, err error) {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return false, err
	}
	base := cfg.General.BaseBranch
	unsigned, err := tool.UnsignedCommits(ctx, a.runner, wt.Path, base)
	if err != nil {
		return false, err
	}
	if len(unsigned) == 0 {
		a.auditLog("signed_commit", string(brn), "signed commit check passed: all commits signed")
		return false, nil
	}

	id := a.idOf(brn)
	if err := a.transitionStatus(ctx, id, from, domain.BeadStateHumanQueue, a.systemActor()); err != nil {
		return false, err
	}
	// Full SHAs, not abbreviated: the audit trail is what a human retraces a
	// blocked bead from, and an abbreviation can go ambiguous as the repo grows.
	detail := fmt.Sprintf("unsigned commit(s) %s — merge blocked, parked in human queue; configure signing (`git config gpg.format ssh && git config user.signingkey <key> && git config commit.gpgsign true`), then re-sign with `git commit --amend --no-edit -S`",
		strings.Join(unsigned, ", "))
	a.auditLog("signed_commit", string(brn), detail)
	a.outf("%s\n", detail)
	return true, nil
}

// readyForMerge marks the bead's worktree branch ready for a local merge
// once the gate, secret scan, and signed-commit check have all passed. There
// is no push and no remote round-trip: the branch already lives in the
// bead's worktree, and `baron merge` (or the auto-merge path right after
// this) runs `git merge` against it directly in the main checkout. The
// audit log is the record of what happened and when.
func (a *app) readyForMerge(ctx context.Context, brn domain.BRN, from domain.BeadState, wt domain.Worktree) error {
	id := a.idOf(brn)
	if err := a.transitionStatus(ctx, id, from, domain.BeadStateMergable, a.systemActor()); err != nil {
		return err
	}
	detail := fmt.Sprintf("ready to merge: %s", wt.Branch)
	a.auditLog("mergable", string(brn), detail)
	a.outf("%s\n", detail)
	return nil
}

// tryAutoMerge evaluates the opt-in conditional auto-merge policy against
// the bead's branch and merges it locally (actor:manager) when every
// condition — policy fields plus the same fixed preconditions `baron merge`
// enforces (preflight, no unresolved conflicts, clean git diff --check) —
// is satisfied. Any unmet condition leaves the bead in mergable awaiting
// human approval; there is no silent partial pass.
func (a *app) tryAutoMerge(ctx context.Context, brn domain.BRN, from domain.BeadState, wt domain.Worktree) error {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return err
	}
	// The forbidden-config list applies to the auto path too.
	if err := checkForbiddenMergeConfig(ctx, a.runner, a.dir); err != nil {
		return err
	}
	auto := cfg.Merge.Auto
	if !auto.Enabled {
		return nil
	}
	bead, err := a.findBead(ctx, brn, string(brn))
	if err != nil {
		return err
	}
	policy := domain.MergePolicy{
		Enabled:         auto.Enabled,
		RequireTags:     auto.RequireTags,
		MaxChangedFiles: auto.MaxChangedFiles,
		MaxDiffLines:    auto.MaxDiffLines,
		ForbidPaths:     auto.ForbidPaths,
	}
	dir, base, err := a.mergeDir(ctx, bead)
	if err != nil {
		return err
	}
	allowed, err := a.checkAutoMergePolicy(ctx, policy, bead, dir, base, wt)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}

	// Captured before the merge runs — see domain.MergeBaseDetail's own doc
	// comment (internal/domain/merge_audit.go) for why the pre-merge base
	// tip, not the resulting merge commit, is what the Diff tab needs.
	beforeSHA, shaErr := tool.HeadSHA(ctx, a.runner, dir)
	if shaErr != nil {
		a.warn("merge base sha: %v", shaErr)
	}
	if err := tool.MergeBranch(ctx, a.runner, dir, wt.Branch); err != nil {
		return err
	}
	id := a.idOf(brn)
	detail := domain.MergeBaseDetail(fmt.Sprintf("auto-merged %s into %s (policy satisfied)", wt.Branch, base), beforeSHA)
	// merged is a resting terminal state (registered as a bd custom status,
	// see store.customStatuses), not a momentary hop to closed: a bead that
	// went through a merge should stay distinguishable from one closed
	// without ever having gone through one. See finishMerge's killWindow
	// doc comment for why this call leaves it false.
	a.finishMerge(ctx, mergeFinishParams{
		id: id, brn: brn, auditRef: string(brn), dir: dir, beforeSHA: beforeSHA, bead: bead,
		from: from, transitionActor: a.managerActor(),
		auditActor:  storeActor(a.managerActor()),
		auditAction: "merge_auto", detail: detail, killWindow: false,
	})
	return nil
}

func (a *app) checkAutoMergePolicy(ctx context.Context, policy domain.MergePolicy, bead store.Bead, dir, base string, wt domain.Worktree) (bool, error) {
	paths, lines, err := tool.ChangedFiles(ctx, a.runner, dir, base, wt.Branch)
	if err != nil {
		return false, err
	}
	decision := domain.EvaluateMergePolicy(policy, bead.Tags, len(paths), lines, paths)
	if !decision.Allowed {
		a.outf("auto-merge policy not satisfied: %s (%s stays ready for human review)\n", decision.Reason, wt.Branch)
		return false, nil
	}
	preflight, err := tool.Preflight(ctx, a.runner, dir, base, wt.Branch)
	if err != nil {
		return false, err
	}
	if !preflight.Clean {
		a.outf("auto-merge skipped: preflight conflict between %s and %s (%s stays ready)\n", wt.Branch, base, wt.Branch)
		return false, nil
	}
	ok, unresolved, diffOut, err := mergeConflictFree(ctx, a.runner, wt.Path)
	if err != nil {
		return false, err
	}
	if !ok {
		if unresolved {
			a.outf("auto-merge skipped: unresolved conflicts in %s (%s stays ready)\n", wt.Path, wt.Branch)
		} else {
			a.outf("auto-merge skipped: git diff --check found problems (%s stays ready):\n%s\n", wt.Branch, diffOut)
		}
		return false, nil
	}
	return true, nil
}

// passed positionally since the natural parameter list pushes the count
// well past 7. transitionActor and auditActor carry runMerge (human,
// user-triggered) and tryAutoMerge (policy, manager-triggered)'s one real
// difference — see each call site's own comment.
type mergeFinishParams struct {
	id              string
	brn             domain.BRN
	auditRef        string
	dir             string
	beforeSHA       string
	bead            store.Bead
	from            domain.BeadState
	transitionActor domain.Actor
	auditActor      store.Actor
	auditAction     string
	detail          string
	killWindow      bool
}

// finishMerge is runMerge and tryAutoMerge's shared merge-completion tail,
// run once `git merge` has already succeeded: transition the bead to
// merged, audit-log it, clean up its worktree (and, for a human-triggered
// merge, its live agent tmux window — see killWindow), advance a
// hierarchical parent, and run the close gate. Every step here degrades to
// a warning on failure, never aborting the merge itself, which already
// landed.
func (a *app) finishMerge(ctx context.Context, p mergeFinishParams) {
	if err := a.transitionStatus(ctx, p.id, p.from, domain.BeadStateMerged, p.transitionActor); err != nil {
		a.warn("status: %v", err)
	}
	a.auditLogAs(p.auditActor, p.auditAction, p.auditRef, p.detail)
	a.outf("%s\n", p.detail)
	if err := a.worktrees.Remove(ctx, p.id); err != nil {
		a.warn("worktree cleanup: %v", err)
	}
	if p.killWindow {
		a.killBeadWindow(ctx, p.brn)
	}
	// A hierarchical child just handed its work up into its parent's
	// branch: if that was the parent's last open child, the parent is now
	// ready for its own human-reviewed merge (see tryAdvanceParent).
	if err := a.tryAdvanceParent(ctx, p.bead); err != nil {
		a.warn("parent advance: %v", err)
	}
	if err := a.mergeCloseGate(ctx, p.brn, p.bead, p.dir, p.beforeSHA); err != nil {
		a.warn("closer: %v", err)
	}
}

// printDiff shows what the model changed in dir as plain `git diff` text.
// Skipped in --json mode and for --gate-only runs, which touch no model
// output.
func (a *app) printDiff(ctx context.Context, dir string) {
	if a.json {
		return
	}
	res, err := a.runner.Run(ctx, "git", []string{"diff", "HEAD"}, tool.Options{Dir: dir})
	if err != nil {
		a.warn("diff: %v", err)
		return
	}
	diff := strings.TrimSpace(res.Stdout)
	if diff == "" {
		return
	}
	a.outf("\n%s\n", diff)
}

// recordRun logs one model launch attempt for `baron report cost`, degrading
// to a warning on failure: a broken cost log must never block a run.
func (a *app) recordRun(brn, model string, startedAt time.Time, duration time.Duration) {
	err := a.runs.Append(store.Run{BRN: brn, Model: model, StartedAt: startedAt, Duration: duration})
	if err != nil {
		a.warn("run log: %v", err)
	}
}

// gateFailureSummary renders a gate report's failed steps for a retry
// prompt: the model sees the gate name, command and exit code — not the
// full output, which is exactly what it caused.
