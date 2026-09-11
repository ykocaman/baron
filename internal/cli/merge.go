package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// runMerge is BARON's single merge gate. It runs a real local `git merge`
// of the bead's worktree branch into the configured base branch — no
// remote involved. There is no actor:agent path to guard against here —
// v1's architecture never lets a model process invoke baron actions
// directly (models only add bd comments); only a human (via the TUI's 'm'
// on a mergable bead) ever reaches this code. The conditional auto-merge
// policy is a separate, opt-in path (see tryAutoMerge). confirmMerge is a
// leftover non-TTY safety parameter from the old CLI invocation
// (--yes AND --confirm-merge both required); the TUI always calls this
// through isolatedCall with confirmMerge forced true — pressing 'm' on a
// mergable bead already is the human's approval, immediate and
// confirmation-free, the same way Assign/CloseBead are (see tui.Deps.Merge).
func (a *app) runMerge(ctx context.Context, ref string, confirmMerge bool) error {
	if err := checkForbiddenMergeConfig(ctx, a.runner, a.dir); err != nil {
		return err
	}
	bead, err := a.resolveMergeTarget(ctx, ref)
	if err != nil {
		return err
	}
	brn := bead.BRN
	id := a.idOf(brn)
	branch := domain.BranchName(bead.IssueType, id)
	dir, base, err := a.mergeDir(ctx, bead)
	if err != nil {
		return err
	}

	preflight, err := tool.Preflight(ctx, a.runner, dir, base, branch)
	if err != nil {
		return err
	}
	if !preflight.Clean {
		if err := a.resolveMergeConflict(ctx, ref, branch, base, dir, preflight.Output); err != nil {
			return err
		}
	}

	if diff, err := tool.BranchDiff(ctx, a.runner, dir, base, branch); err == nil && diff != "" {
		a.outf("%s\n", diff)
	}

	approved, err := a.confirmMerge(ref, confirmMerge)
	if err != nil {
		return err
	}
	if !approved {
		a.aborted()
		return silentError{code: ExitMerge}
	}

	// Captured before the merge runs — see domain.MergeBaseDetail's own doc
	// comment for why the pre-merge base tip, not the resulting merge
	// commit, is what the Diff tab needs to reconstruct what this merge
	// introduced.
	beforeSHA, shaErr := tool.HeadSHA(ctx, a.runner, dir)
	if shaErr != nil {
		a.warn("merge base sha: %v", shaErr)
	}
	if err := tool.MergeBranch(ctx, a.runner, dir, branch); err != nil {
		return err
	}
	detail := domain.MergeBaseDetail(fmt.Sprintf("merged %s into %s", branch, base), beforeSHA)
	// Closure: bd status, audit, worktree cleanup. Cost recording is a
	// separate ticket (baron-3ca) not built yet.
	//
	// merged is a resting terminal state (registered as a bd custom status,
	// see store.customStatuses), not a momentary hop to closed.
	// resolveMergeTarget only reaches here via a bead already in mergable
	// (readyForMerge got it there), so that's the state assumed here.
	//
	// KillWindow: true — "merged" is a resting terminal state, a human may
	// never run `baron work close` on top of it — so the agent's tmux
	// window is reaped here too, or it outlives the work it was running.
	// tryAutoMerge's own call to finishMerge leaves this false: it runs from
	// inside postAgentGate right after the agent process already exited, so
	// there is no window left to kill.
	a.finishMerge(ctx, mergeFinishParams{
		id: id, brn: brn, auditRef: ref, dir: dir, beforeSHA: beforeSHA, bead: bead,
		from: domain.BeadStateMergable, transitionActor: a.systemActor(),
		auditActor:  store.Actor{Type: store.ActorUser, Name: currentUser()},
		auditAction: "merge", detail: detail, killWindow: true,
	})
	return nil
}

// resolveMergeConflict handles runMerge's preflight-conflict branch: a
// non-TTY caller can't be handed a terminal, so it just aborts with
// instructions; a TTY caller gets BARON suspended and the terminal handed to
// git mergetool, then a post-resolver re-validation (no unresolved conflict
// markers, clean `git diff --check`) before returning control.
func (a *app) resolveMergeConflict(ctx context.Context, ref, branch, base, dir, preflightOutput string) error {
	if !a.inTTY() {
		// Non-TTY: can't hand off terminal; abort with instructions.
		a.outf("merge conflict between %s and %s:\n%s\n", branch, base, preflightOutput)
		a.outf("resolve manually, then re-run `baron merge %s`\n", ref)
		return silentError{code: ExitMerge}
	}
	// TTY: suspend BARON, hand terminal to git mergetool, resume and
	// re-validate.
	a.outf("merge conflict — launching conflict resolver (q to cancel)…\n")
	if err := tool.LaunchConflictResolver(ctx, dir); err != nil {
		a.outf("conflict resolver exited: %v\n", err)
	}
	// Post-resolver validation.
	ok, unresolved, diffOut, err := mergeConflictFree(ctx, a.runner, dir)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if unresolved {
		a.outf("unresolved conflicts remain (git ls-files -u is non-empty); resolve and re-run `baron merge %s`\n", ref)
	} else {
		a.outf("git diff --check found problems:\n%s\nfix and re-run `baron merge %s`\n", diffOut, ref)
	}
	return silentError{code: ExitMerge}
}

// mergeDir resolves where bead's own branch merge (preflight, diff, the
// actual `git merge`) should run, and what it merges into: the main
// checkout and the configured base branch for anything without a parent,
// or a worktree for the parent bead and the parent's own branch for a
// hierarchical child (bd's dotted-id or explicit Parent link, see
// store.ParentOf) — a subtree merges into its parent before that parent
// ever reaches the configured base branch, and the parent's branch isn't
// checked out anywhere else, so merging into it needs its own workspace.
// The parent worktree is created (or reused) on demand; it isn't removed
// here — it stays the integration point for any remaining siblings, and
// for the parent's own eventual merge.
func (a *app) mergeDir(ctx context.Context, bead store.Bead) (dir, base string, err error) {
	all, err := a.beads.List(ctx)
	if err != nil {
		return "", "", err
	}
	parent, ok := store.ParentOf(bead, all)
	if !ok {
		cfg, err := a.loadConfig(a.dir)
		if err != nil {
			return "", "", err
		}
		return a.dir, cfg.General.BaseBranch, nil
	}
	grandBase, err := a.mergeBaseFor(ctx, parent)
	if err != nil {
		return "", "", err
	}
	wt, err := a.worktrees.Create(ctx, parent.ID, parent.IssueType, grandBase)
	if err != nil {
		return "", "", err
	}
	return wt.Path, domain.BranchName(parent.IssueType, parent.ID), nil
}

// mergeConflictFree runs the two checks both merge paths (runMerge,
// tryAutoMerge) need after preflight: no unresolved conflict markers (git
// ls-files -u), and a clean `git diff --check`. ok is false when either
// finds a problem — the caller renders its own path-specific message
// around unresolved/diffOut (the two paths report this very differently:
// runMerge sends the human to fix it and re-run, tryAutoMerge just leaves
// the bead ready for manual review).
func mergeConflictFree(ctx context.Context, runner tool.Runner, dir string) (ok, unresolved bool, diffOut string, err error) {
	if unresolved, err := tool.UnresolvedConflicts(ctx, runner, dir); err != nil {
		return false, false, "", err
	} else if unresolved {
		return false, true, "", nil
	}
	if dirty, out, err := tool.DirtyDiff(ctx, runner, dir); err != nil {
		return false, false, "", err
	} else if dirty {
		return false, false, out, nil
	}
	return true, false, "", nil
}

// checkForbiddenMergeConfig enforces the forbidden-merge rule: merges are
// refused while the repository defines any forbidden merge configuration —
// merge=ours/union gitattributes attributes, an "ours" merge strategy, or
// rerere.autoUpdate. BARON never activates these itself; if the project has
// them set, the merge result would be silently biased, so both merge paths
// (human, auto) abort before anything runs.
func checkForbiddenMergeConfig(ctx context.Context, runner tool.Runner, dir string) error {
	if gitConfigTruthy(gitConfigGet(ctx, runner, dir, "rerere.autoUpdate")) {
		return fmt.Errorf("merge forbidden: rerere.autoUpdate is enabled — automatic conflict-resolution replay; unset it (`git config --unset rerere.autoUpdate`) and re-run")
	}
	if gitConfigTruthy(gitConfigGet(ctx, runner, dir, "merge.renormalize")) {
		return fmt.Errorf("merge forbidden: merge.renormalize is enabled — silent merge-result rewriting; unset it and re-run")
	}
	if gitConfigGet(ctx, runner, dir, "merge.strategy") == "ours" {
		return fmt.Errorf("merge forbidden: merge.strategy is \"ours\" — discards the incoming branch; unset it and re-run")
	}
	if attrs, err := os.ReadFile(filepath.Join(dir, ".gitattributes")); err == nil {
		for _, banned := range []string{"merge=ours", "merge=union"} {
			if strings.Contains(string(attrs), banned) {
				return fmt.Errorf("merge forbidden: .gitattributes defines %s — merge strategy attributes; remove the attribute and re-run", banned)
			}
		}
	}
	return nil
}

// gitConfigGet returns a repository git config value, "" when unset.
// A missing key makes `git config --get` exit nonzero, which is absence,
// not an error.
func gitConfigGet(ctx context.Context, runner tool.Runner, dir, key string) string {
	res, err := runner.Run(ctx, "git", []string{"config", "--get", key}, tool.Options{Dir: dir})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// gitConfigTruthy reports whether a git config value reads as enabled.
func gitConfigTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// resolveMergeTarget resolves ref (a BRN or bare bd id) to the bead ready to
// merge. Only a bead in the mergable (gate passed, awaiting human approval)
// state has a branch to merge.
func (a *app) resolveMergeTarget(ctx context.Context, ref string) (store.Bead, error) {
	brn, err := a.parseBRNArg(ref)
	if err != nil {
		return store.Bead{}, err
	}
	bead, err := a.findBead(ctx, brn, ref)
	if err != nil {
		return store.Bead{}, err
	}
	if bead.Status != store.BeadStatusMergable {
		return store.Bead{}, fmt.Errorf("%s is not ready to merge (status: %s)", ref, bead.Status)
	}
	return bead, nil
}

// confirmMerge implements the merge approval rule, which is stricter than
// app.confirm: --yes never approves a merge by itself. In a TTY it always
// prompts interactively, ignoring --yes. Non-TTY requires both --yes and
// --confirm-merge; missing either is reported so the caller can exit 5.
func (a *app) confirmMerge(ref string, confirmMergeFlag bool) (bool, error) {
	if !a.inTTY() {
		if !a.yes || !confirmMergeFlag {
			a.warn("non-interactive merge requires both --yes and --confirm-merge: baron merge %s --yes --confirm-merge", ref)
			return false, nil
		}
		return true, nil
	}
	var value bool
	confirm := huh.NewConfirm().
		Title(fmt.Sprintf("merge %s?", ref)).
		Affirmative("yes").
		Negative("no").
		Value(&value)
	err := huh.NewForm(huh.NewGroup(confirm)).
		WithInput(a.in).
		WithOutput(a.out).
		Run()
	return value, err
}
