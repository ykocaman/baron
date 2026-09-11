package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/tool"
	"github.com/baron-cli/baron/internal/tui"
)

func (a *app) headerStats(ctx context.Context, brn string) (tui.HeaderStats, error) {
	var stats tui.HeaderStats
	stats.ShortPath = shortPath(a.dir)

	gitDir := a.statsGitDir(brn)
	branch, err := a.gitBranch(ctx, gitDir)
	if err != nil {
		return stats, err
	}
	stats.Branch = branch

	if stats.Branch != "" {
		stats.ChangedFiles, stats.UntrackedFiles, stats.DeletedFiles = a.gitChangeCounts(ctx, gitDir)
		stats.DiffInserted, stats.DiffDeleted = a.gitDiffStat(ctx, gitDir)
	}

	stats.Agents = a.tmuxAgentCount(ctx)

	return stats, nil
}

// shortPath collapses dir to a "~"-prefixed path when it's under the user's
// home directory, falling back to the absolute path (or, failing even that,
// just dir's base name).
func shortPath(dir string) string {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Base(dir)
	}
	homeDir, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(absDir, homeDir) {
		return "~" + strings.TrimPrefix(absDir, homeDir)
	}
	return absDir
}

// statsGitDir resolves which directory headerStats' git calls should run
// in: brn's own worktree when one exists on disk, else the main checkout.
func (a *app) statsGitDir(brn string) string {
	if brn == "" || a.worktrees == nil {
		return a.dir
	}
	wt := a.worktrees.Path(a.idOf(domain.BRN(brn)))
	if wt == "" {
		return a.dir
	}
	if info, err := os.Stat(wt); err == nil && info.IsDir() {
		return wt
	}
	return a.dir
}

// gitBranch resolves gitDir's current branch, empty for a detached HEAD.
func (a *app) gitBranch(ctx context.Context, gitDir string) (string, error) {
	res, err := a.runner.Run(ctx, "git", []string{"rev-parse", "--abbrev-ref", "HEAD"}, tool.Options{Dir: gitDir})
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(res.Stdout)
	if branch == "" || branch == "HEAD" {
		return "", nil
	}
	return branch, nil
}

// gitChangeCounts parses `git status --porcelain` into changed/untracked/
// deleted file counts; a run failure reports zero for all three.
func (a *app) gitChangeCounts(ctx context.Context, gitDir string) (changed, untracked, deleted int) {
	res, err := a.runner.Run(ctx, "git", []string{"status", "--porcelain"}, tool.Options{Dir: gitDir})
	if err != nil {
		return 0, 0, 0
	}
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		changed++
		switch {
		case strings.HasPrefix(line, "??"):
			untracked++
		case len(line) >= 2 && (line[0] == 'D' || line[1] == 'D'):
			deleted++
		}
	}
	return changed, untracked, deleted
}

// gitDiffStat parses `git diff --shortstat` (e.g. " 1 file changed, 2
// insertions(+), 1 deletion(-)") into inserted/deleted line counts; a run
// failure or unparseable output reports zero for both.
func (a *app) gitDiffStat(ctx context.Context, gitDir string) (inserted, deleted int) {
	res, err := a.runner.Run(ctx, "git", []string{"diff", "--shortstat"}, tool.Options{Dir: gitDir})
	if err != nil {
		return 0, 0
	}
	diffOut := strings.TrimSpace(res.Stdout)
	if diffOut == "" {
		return 0, 0
	}
	for part := range strings.SplitSeq(diffOut, ",") {
		switch {
		case strings.Contains(part, "insertion"):
			if fields := strings.Fields(strings.TrimSpace(part)); len(fields) > 0 {
				inserted, _ = strconv.Atoi(fields[0])
			}
		case strings.Contains(part, "deletion"):
			if fields := strings.Fields(strings.TrimSpace(part)); len(fields) > 0 {
				deleted, _ = strconv.Atoi(fields[0])
			}
		}
	}
	return inserted, deleted
}

// tmuxAgentCount counts distinct live agent panes across every tmux
// session — panes whose start command still carries the LaunchScript
// wrapper's DoneSentinel (bare shells, the TUI's own pane, and user
// terminals never have it). Grouped sessions (the per-window baron-view-*
// viewers) list the same panes once per member session, so dedupe by
// pane_id before counting. A run failure reports zero.
func (a *app) tmuxAgentCount(ctx context.Context) int {
	res, err := a.runner.Run(ctx, "tmux", []string{"list-panes", "-a", "-F", "#{pane_id}\t#{pane_start_command}\t#{pane_current_command}"}, tool.Options{Dir: a.dir})
	if err != nil {
		return 0
	}
	seen := make(map[string]bool)
	count := 0
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		paneID, rest, ok := strings.Cut(line, "\t")
		if !ok || paneID == "" || seen[paneID] {
			continue
		}
		seen[paneID] = true
		start, _, _ := strings.Cut(rest, "\t")
		if strings.Contains(start, domain.DoneSentinel) {
			count++
		}
	}
	return count
}

// assignableModels is the assign picker's choice list: every model in the
// machine-wide catalog, read straight off disk.
//
// This is a cache read, not a probe. It used to spawn `opencode models` on
// every open (and a second `opencode models --verbose` per selection just
// to learn that model's effort levels), which is what made pressing 'a'
// lag. The catalog is written by `baron init` and `baron doctor`; when it
// is missing or stale, one refresh runs here and the result is cached for
// every later open — see agent.Catalog.Stale.
func (a *app) assignableModels(ctx context.Context) ([]string, error) {
	return a.catalog(ctx).IDs(), nil
}

// effortChoicesFor returns the reasoning-effort levels the picked model
// accepts — nil means it has none and the picker's effort step is skipped
// entirely.
//
// The answer comes from the catalog entry itself, so no string surgery on
// the ID is involved. That matters: opencode's own IDs are "provider/model"
// pairs ("opencode-go/deepseek-v4-flash") whose prefix is a provider
// namespace, not the agent that runs them, so a prefix-based lookup was
// never able to answer this question in general.
func (a *app) effortChoicesFor(ctx context.Context, picked string) ([]string, error) {
	m, ok := a.catalog(ctx).Find(picked)
	if !ok {
		return nil, nil
	}
	return m.Efforts, nil
}

// catalog returns the machine-wide model catalog, refreshing it first when
// it is missing or older than its TTL. A refresh failure falls back to
// whatever is cached: a stale list is far better than an empty picker.
// Either way the project's [[agents]] are layered on top, so an agent
// registered only in config.toml is assignable even while the shared
// cache is still fresh (Refresh only ever merges them into a rebuilt
// catalog; the cache itself never carries config-only agents).
func (a *app) catalog(ctx context.Context) *agent.Catalog {
	cat := agent.LoadCatalog()
	if cat.Stale() {
		d, err := agent.Refresh(ctx, a.runner, a.configAgents())
		if err != nil && len(d.Catalog.Models) == 0 {
			return cat
		}
		cat = d.Catalog
	}
	return cat.WithConfigAgents(ctx, a.runner, a.configAgents())
}

// newIsolatedApp builds a copy of a's dependencies with its own output
// buffers and inTTY forced false — the isolation isolatedCall needs:
// nothing a sub-app prints leaks into the TUI's own rendering (which owns
// the real stdout), and nothing it does can block on a stdin prompt
// nothing would ever answer.
