package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// MergePreflight is the result of a dry-run merge check that never touches
// the worktree.
type MergePreflight struct {
	Clean  bool
	Output string // conflict details, when not clean
}

// Preflight checks whether branch merges cleanly into base via
// `git merge-tree --write-tree`, without touching dir's worktree. git
// documents exit code 1 as "merge produced conflicts" and other non-zero
// codes as a genuine failure (git-scm.com/docs/git-merge-tree); only the
// former is a normal MergePreflight{Clean: false} result.
func Preflight(ctx context.Context, runner Runner, dir, base, branch string) (MergePreflight, error) {
	res, err := runner.Run(ctx, "git", []string{"merge-tree", "--write-tree", base, branch}, Options{Dir: dir})
	if err == nil {
		return MergePreflight{Clean: true}, nil
	}
	if res.ExitCode == 1 {
		return MergePreflight{Output: res.Stdout}, nil
	}
	return MergePreflight{}, fmt.Errorf("git merge-tree --write-tree %s %s: %w", base, branch, err)
}

// UnresolvedConflicts reports whether dir has unresolved merge conflicts
// via `git ls-files -u`.
func UnresolvedConflicts(ctx context.Context, runner Runner, dir string) (bool, error) {
	res, err := runner.Run(ctx, "git", []string{"ls-files", "-u"}, Options{Dir: dir})
	if err != nil {
		return false, fmt.Errorf("git ls-files -u: %w", err)
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}

// UnstagedChanges lists every path git status --porcelain reports, i.e. any
// uncommitted difference from HEAD: worktree edits, staged-but-uncommitted
// edits, and untracked files. A clean tree yields an empty slice — the
// signal that a prompt's run changed nothing and gates are unnecessary.
// Untracked files count: an agent-created new file is a change too, and
// plain `git diff` would silently miss it.
func UnstagedChanges(ctx context.Context, runner Runner, dir string) ([]string, error) {
	res, err := runner.Run(ctx, "git", []string{"status", "--porcelain"}, Options{Dir: dir})
	if err != nil {
		return nil, fmt.Errorf("git status --porcelain: %w", err)
	}
	var files []string
	for ln := range strings.SplitSeq(res.Stdout, "\n") {
		if len(ln) < 4 {
			continue
		}
		// Porcelain v1: two status columns (X=index, Y=worktree), space,
		// then the path. Renames are "R  old -> new" — keep the whole tail.
		files = append(files, ln[3:])
	}
	return files, nil
}

// StageChanges runs `git add` on the given files (see UnstagedChanges for
// what they are). Files already committed or missing are no-ops for git add.
func StageChanges(ctx context.Context, runner Runner, dir string, files []string) error {
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"add"}, files...)
	res, err := runner.Run(ctx, "git", args, Options{Dir: dir})
	if err != nil {
		return fmt.Errorf("git add: %w (%s)", err, res.Stderr)
	}
	return nil
}

// ErrNothingStaged reports that Commit found an index identical to HEAD.
// It is not a failure: an agent that already committed its own work leaves
// exactly this state, and the caller carries on with that commit.
var ErrNothingStaged = errors.New("nothing staged to commit")

// Commit records the staged content in dir as a single commit. The repo's
// own git config decides authorship and signing (commit.gpgsign) — BARON
// neither generates nor stores keys (docs/PRD/security.md §4) — and hooks
// are left enabled, so a project's pre-commit hook still applies.
// ErrNothingStaged means the index matched HEAD and no commit was made.
func Commit(ctx context.Context, runner Runner, dir, message string) (string, error) {
	if staged, err := hasStagedChanges(ctx, runner, dir); err != nil {
		return "", err
	} else if !staged {
		return "", ErrNothingStaged
	}
	res, err := runner.Run(ctx, "git", []string{"commit", "-m", message}, Options{Dir: dir})
	if err != nil {
		return "", fmt.Errorf("git commit: %w (%s)", err, firstNonEmpty(res.Stderr, res.Stdout))
	}
	return HeadSHA(ctx, runner, dir)
}

// hasStagedChanges reports whether dir's index differs from HEAD.
// `git diff --cached --quiet` exits 1 when it does, 0 when it doesn't.
func hasStagedChanges(ctx context.Context, runner Runner, dir string) (bool, error) {
	res, err := runner.Run(ctx, "git", []string{"diff", "--cached", "--quiet"}, Options{Dir: dir})
	if err == nil {
		return false, nil
	}
	if res.ExitCode == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --cached --quiet: %w (%s)", err, res.Stderr)
}

// HeadSHA returns dir's current HEAD commit.
func HeadSHA(ctx context.Context, runner Runner, dir string) (string, error) {
	res, err := runner.Run(ctx, "git", []string{"rev-parse", "HEAD"}, Options{Dir: dir})
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// CommitsAhead counts the commits dir's HEAD has that base does not. A base
// ref that doesn't resolve (no remote configured, never fetched) yields
// (0, false, nil): "can't tell", which callers must not read as "no work".
func CommitsAhead(ctx context.Context, runner Runner, dir, base string) (count int, known bool, err error) {
	if _, verifyErr := runner.Run(ctx, "git", []string{"rev-parse", "--verify", "--quiet", base}, Options{Dir: dir}); verifyErr == nil {
		res, err := runner.Run(ctx, "git", []string{"rev-list", "--count", base + "..HEAD"}, Options{Dir: dir})
		if err != nil {
			return 0, false, fmt.Errorf("git rev-list --count %s..HEAD: %w", base, err)
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(res.Stdout))
		if convErr != nil {
			return 0, false, fmt.Errorf("git rev-list --count %s..HEAD: unexpected output %q", base, res.Stdout)
		}
		return n, true, nil
	}
	return 0, false, nil
}

// firstNonEmpty returns the first non-blank string, for picking whichever
// of stderr/stdout a failing git command actually wrote to.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// MergeBranch merges branch into dir's current HEAD with a real merge
// commit (--no-ff, --no-edit) — a genuine merge, never a squash or rebase.
// Callers are expected to have already preflighted (Preflight) so this
// should apply cleanly; a conflict here still surfaces as an error rather
// than leaving dir mid-merge silently.
func MergeBranch(ctx context.Context, runner Runner, dir, branch string) error {
	res, err := runner.Run(ctx, "git", []string{"merge", "--no-ff", "--no-edit", branch}, Options{Dir: dir})
	if err != nil {
		return fmt.Errorf("git merge --no-ff %s: %w (%s)", branch, err, firstNonEmpty(res.Stderr, res.Stdout))
	}
	return nil
}

// BranchDiff returns branch's diff against base (`git diff base...branch`).
// Empty string means no changes.
func BranchDiff(ctx context.Context, runner Runner, dir, base, branch string) (string, error) {
	res, err := runner.Run(ctx, "git", []string{"diff", base + "..." + branch}, Options{Dir: dir})
	if err != nil {
		return "", fmt.Errorf("git diff %s...%s: %w", base, branch, err)
	}
	return res.Stdout, nil
}

// RangeDiff returns the plain two-dot diff between from and to (`git diff
// from to`) — what a --no-ff merge commit actually introduced, given from
// its own pre-merge base tip and to its post-merge HEAD. Unlike BranchDiff's
// three-dot form (branch's own commits since it diverged from base), this
// is a straight tree comparison: the right shape once from/to are two
// points on the SAME branch's own history (see domain.MergeBaseDetail's
// doc comment for why a --no-ff merge commit's own diff, `git show`, is
// empty by git's default combined-diff semantics and can't be used here).
func RangeDiff(ctx context.Context, runner Runner, dir, from, to string) (string, error) {
	res, err := runner.Run(ctx, "git", []string{"diff", from, to}, Options{Dir: dir})
	if err != nil {
		return "", fmt.Errorf("git diff %s %s: %w", from, to, err)
	}
	return res.Stdout, nil
}

// ChangedFiles returns the paths changed and total added+deleted line count
// for branch against base (`git diff --numstat`), used by the auto-merge
// policy check.
func ChangedFiles(ctx context.Context, runner Runner, dir, base, branch string) (paths []string, lines int, err error) {
	res, err := runner.Run(ctx, "git", []string{"diff", "--numstat", base + "..." + branch}, Options{Dir: dir})
	if err != nil {
		return nil, 0, fmt.Errorf("git diff --numstat %s...%s: %w", base, branch, err)
	}
	for ln := range strings.SplitSeq(strings.TrimRight(res.Stdout, "\n"), "\n") {
		if ln == "" {
			continue
		}
		fields := strings.SplitN(ln, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		paths = append(paths, fields[2])
		added, _ := strconv.Atoi(fields[0])
		deleted, _ := strconv.Atoi(fields[1])
		lines += added + deleted
	}
	return paths, lines, nil
}

// DirtyDiff reports whitespace/conflict-marker errors via `git diff --check`.
// Any non-zero exit means problems were found — this command doesn't fail
// for infrastructure reasons in a working repo.
func DirtyDiff(ctx context.Context, runner Runner, dir string) (bool, string, error) {
	res, err := runner.Run(ctx, "git", []string{"diff", "--check"}, Options{Dir: dir})
	if err == nil {
		return false, "", nil
	}
	return true, res.Stdout, nil
}

// LaunchConflictResolver implements suspend → exec → resume: it hands the
// terminal over to `git mergetool`, waits for the tool to exit, then
// returns so the caller can re-validate. The caller remains responsible for
// post-exit ls-files -u / diff --check checks. inTTY must be true — calling
// this in a non-TTY context is a programming error (this only applies
// interactively).
func LaunchConflictResolver(ctx context.Context, dir string) error {
	name, args := ResolverCommand()
	return runInteractive(ctx, dir, name, args...)
}

// ResolverCommand picks the conflict-resolution surface: `git mergetool`
// with trustExitCode=false (backups stay on disk — a GUI exit code of 0
// doesn't prove resolution). Shared by the CLI (LaunchConflictResolver)
// and the TUI, which needs the name/args separately to build its own
// tea.ExecProcess suspend/resume.
func ResolverCommand() (name string, args []string) {
	return "git", []string{"-c", "mergetool.trustExitCode=false", "mergetool"}
}

// runInteractive runs cmd with full terminal access (os.Stdin/Stdout/Stderr)
// in dir. This is the PTY hand-off: the parent yields the terminal to the
// child for the duration of its run, then resumes on child exit.
func runInteractive(ctx context.Context, dir string, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
