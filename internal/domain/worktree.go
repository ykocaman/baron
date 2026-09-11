package domain

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/baron-cli/baron/internal/tool"
)

// Worktree describes a git worktree/branch pair prepared for a bead.
type Worktree struct {
	BRN    string
	Branch string
	Path   string
}

// WorktreeManager prepares, tracks and tears down one git worktree/branch per
// active bead so concurrent beads never write to the same checkout.
type WorktreeManager struct {
	runner  tool.Runner
	repoDir string // main repository root; git commands run here
	rootDir string // directory worktrees are created under
	// BaseBranch names the local branch new bead branches fork from (e.g.
	// "main"). Empty forks from the main checkout's HEAD instead — see
	// Create.
	BaseBranch string
}

// NewWorktreeManager creates a manager rooted at repoDir, placing worktrees
// under rootDir (typically "<repoDir>/.baron/worktrees").
//
// Both are resolved once, here, at construction — never lazily on later
// calls, which would make Path's return value depend on whether
// ".baron/worktrees" happens to already exist on disk at the moment of
// each individual call (it may not, on a project's very first worktree
// ever) and so return a *different* string for the very same bead
// depending on timing, exactly the kind of instability this fix exists to
// remove. repoDir always exists by the time any WorktreeManager is
// constructed (it's the repo itself), so it resolves straightforwardly;
// rootDir's resolved form is derived from repoDir's purely lexically
// (filepath.Rel + Join, no filesystem access, so it doesn't care whether
// rootDir exists yet) whenever rootDir is actually a subpath of repoDir —
// true for the one real call site (repoDir + "/.baron/worktrees") — falling
// back to resolving rootDir directly for the unusual case it isn't.
func NewWorktreeManager(runner tool.Runner, repoDir, rootDir string) *WorktreeManager {
	resolvedRepo := resolveDir(repoDir)
	resolvedRoot := resolveDir(rootDir)
	if rel, err := filepath.Rel(repoDir, rootDir); err == nil && !strings.HasPrefix(rel, "..") {
		resolvedRoot = filepath.Join(resolvedRepo, rel)
	}
	return &WorktreeManager{runner: runner, repoDir: resolvedRepo, rootDir: resolvedRoot}
}

// resolveDir resolves dir's symlinks (macOS's /tmp -> /private/tmp is the
// common real-world case, but any project root under a symlinked path —
// an iCloud Drive folder, some Homebrew-managed locations, an NFS mount —
// hits the same thing) so every path this package constructs from it stays
// byte-comparable with what `git worktree list` reports, which git always
// resolves internally no matter what path was passed to `worktree add`.
// Caught for real: WorktreeManager.Create's own idempotency check
// (List(), string-compared against m.Path(brn)) silently missed an
// already-existing worktree under a symlinked root, so `git worktree add`
// ran a second time at the (canonically identical, string-different) path
// and failed outright ("already exists") — which meant the TUI's
// reattach-after-restart path (spawnAgentTerminal calling this same
// Create) failed too, stranding a bead's still-alive agent as invisible
// ("press r to start it") with no way back except deleting the worktree by
// hand. dir not existing yet (or any other resolution failure) falls back
// to dir unresolved rather than erroring.
func resolveDir(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return resolved
}

// Path returns the deterministic worktree path for a bead.
func (m *WorktreeManager) Path(brn string) string {
	return WorktreePath(m.rootDir, brn)
}

// Create prepares a worktree and branch for brn, reusing an existing one if
// already present so a reassigned bead continues in the same worktree/branch
// family. Two beads never share a path since each path is keyed by BRN.
//
// parentBranch, when non-empty, forks brn's branch from it instead of the
// configured base branch — a hierarchical child (bd's dotted-id or explicit
// Parent link) integrates into its parent's branch, not straight into main,
// so the parent's branch is the review/merge unit for the whole subtree.
// The parent branch is created (at the base branch's tip) if it doesn't
// exist yet — the parent bead itself may never have run its own agent.
func (m *WorktreeManager) Create(ctx context.Context, brn, issueType, parentBranch string) (Worktree, error) {
	branch := BranchName(issueType, brn)
	path := m.Path(brn)

	existing, err := m.List(ctx)
	if err != nil {
		return Worktree{}, err
	}
	for _, w := range existing {
		// Case-insensitive filesystems (macOS, Windows) may resolve the
		// worktree path with different case than rootDir.
		if strings.EqualFold(w.Path, path) {
			return w, nil
		}
	}

	args, err := m.createArgs(ctx, path, branch, parentBranch)
	if err != nil {
		return Worktree{}, err
	}
	if _, err := m.runner.Run(ctx, "git", args, tool.Options{Dir: m.repoDir}); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add %s: %w", path, err)
	}
	return Worktree{BRN: brn, Branch: branch, Path: path}, nil
}

func (m *WorktreeManager) createArgs(ctx context.Context, path, branch, parentBranch string) ([]string, error) {
	args := []string{"worktree", "add", path}
	if m.branchExists(ctx, branch) {
		return append(args, branch), nil
	}
	if parentBranch != "" {
		if err := m.ensureBranch(ctx, parentBranch); err != nil {
			return nil, err
		}
		return append(args, "-b", branch, parentBranch), nil
	}
	args = append(args, "-b", branch)
	if start := m.startPoint(ctx); start != "" {
		args = append(args, start)
	}
	return args, nil
}

func (m *WorktreeManager) ensureBranch(ctx context.Context, branch string) error {
	if m.branchExists(ctx, branch) {
		return nil
	}
	args := []string{"branch", branch}
	if start := m.startPoint(ctx); start != "" {
		args = append(args, start)
	}
	if _, err := m.runner.Run(ctx, "git", args, tool.Options{Dir: m.repoDir}); err != nil {
		return fmt.Errorf("git branch %s: %w", branch, err)
	}
	return nil
}

// startPoint returns the ref a new bead branch forks from: the local base
// branch. "" means fall back to the main checkout's HEAD.
//
// Forking from HEAD is wrong for an orchestrator: BARON merges locally
// (`baron merge` runs `git merge` straight into the base branch, no forge,
// no push), so the base branch itself is always current the moment a merge
// lands — the same ref the gate's secret scan, the signed-commit check and
// the merge preflight all compare against.
//
// A failure here (no such branch yet — a project mid-setup) falls back to
// HEAD rather than failing the run.
func (m *WorktreeManager) startPoint(ctx context.Context) string {
	if m.BaseBranch == "" {
		return ""
	}
	if _, err := m.runner.Run(ctx, "git", []string{"rev-parse", "--verify", "--quiet", m.BaseBranch}, tool.Options{Dir: m.repoDir}); err != nil {
		return ""
	}
	return m.BaseBranch
}

// Remove deletes a bead's worktree directory and prunes git's worktree
// metadata. Force is used because a bead's worktree may hold uncommitted
// gate/model output when the bead closes; the branch and its commit
// history are not deleted. Removing a worktree that was never created is
// not an error.
func (m *WorktreeManager) Remove(ctx context.Context, brn string) error {
	path := m.Path(brn)
	_, err := m.runner.Run(ctx, "git", []string{"worktree", "remove", "--force", path}, tool.Options{Dir: m.repoDir})
	if err != nil && !strings.Contains(err.Error(), "not a working tree") {
		return fmt.Errorf("git worktree remove %s: %w", path, err)
	}
	return nil
}

// List returns the bead worktrees currently tracked by git under rootDir.
func (m *WorktreeManager) List(ctx context.Context) ([]Worktree, error) {
	res, err := m.runner.Run(ctx, "git", []string{"worktree", "list", "--porcelain"}, tool.Options{Dir: m.repoDir})
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktreeList(res.Stdout, m.rootDir), nil
}

// branchExists reports whether branch is a known local branch.
func (m *WorktreeManager) branchExists(ctx context.Context, branch string) bool {
	_, err := m.runner.Run(ctx, "git", []string{"rev-parse", "--verify", "--quiet", "refs/heads/" + branch}, tool.Options{Dir: m.repoDir})
	return err == nil
}

// cutFoldPrefix removes prefix from s when they match case-insensitively,
// mirroring strings.CutPrefix for case-insensitive filesystems.
func cutFoldPrefix(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

// parseWorktreeList parses `git worktree list --porcelain` output, keeping
// only entries under rootDir and deriving BRN from the trailing path segment.
func parseWorktreeList(output, rootDir string) []Worktree {
	var worktrees []Worktree
	var cur Worktree
	flush := func() {
		if cur.Path == "" {
			return
		}
		if prefix := rootDir + "/"; strings.HasPrefix(cur.Path, prefix) {
			cur.BRN = strings.TrimPrefix(cur.Path, prefix)
			worktrees = append(worktrees, cur)
		} else if rest, ok := cutFoldPrefix(cur.Path, prefix); ok {
			cur.BRN = rest
			worktrees = append(worktrees, cur)
		}
		cur = Worktree{}
	}
	for line := range strings.SplitSeq(output, "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	flush()
	return worktrees
}
