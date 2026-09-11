package domain

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/baron-cli/baron/internal/tmux"
)

// DefaultIssueType is the fallback branch type prefix when unspecified.
const DefaultIssueType = "task"

// DefaultTmuxSession is the tmux session that hosts agent processes.
const DefaultTmuxSession = "baron"

// ValidIssueTypes lists all supported issue type categories.
var ValidIssueTypes = []string{"task", "feature", "bug", "epic", "chore", "decision"}

// NormalizeIssueType returns a canonical lowercased issue type, defaulting to "task".
func NormalizeIssueType(issueType string) string {
	t := strings.ToLower(strings.TrimSpace(issueType))
	if slices.Contains(ValidIssueTypes, t) {
		return t
	}
	return DefaultIssueType
}

// ShortID returns the short suffix of a bead identifier (e.g. "baron-4al.1" -> "4al.1", "test-8ep" -> "8ep", "dxs" -> "dxs").
func ShortID(id string) string {
	s := strings.TrimSpace(id)
	if i := strings.LastIndexByte(s, '-'); i >= 0 && i < len(s)-1 {
		return s[i+1:]
	}
	return s
}

// WorktreePath returns the deterministic worktree filesystem path for a bead.
func WorktreePath(rootDir, id string) string {
	return rootDir + "/" + id
}

// TmuxWindowName returns a tmux-safe window identifier for an agent process,
// replacing dots in child bead IDs ("baron-4al.1" -> "baron-4al-1").
func TmuxWindowName(id string) string {
	return strings.ReplaceAll(id, ".", "-")
}

// TmuxDiffWindowName returns the tmux window name for a live hunk diff review session.
func TmuxDiffWindowName(id string) string {
	return TmuxWindowName(id) + "-diff"
}

// BranchName returns the deterministic branch name for a bead: a normalized
// issue type prefix plus the short bead suffix, e.g. BranchName("feature",
// "baron-4al.1") == "feature/4al.1". It is idempotent — an id that already
// carries a "<type>/" or "baron/" prefix is stripped before the short suffix
// is derived, so re-deriving a branch from an existing branch name (or a
// BRN) yields the same result.
func BranchName(issueType, id string) string {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return NormalizeIssueType(issueType) + "/" + ShortID(id)
}

// AgentLogPath returns the deterministic log file path for a bead agent.
func AgentLogPath(dir, brn string) string {
	return filepath.Join(dir, ".baron", "logs", brn+".log")
}

// RunSummaryPath returns the deterministic run summary file path for a bead.
func RunSummaryPath(dir, brn string) string {
	return filepath.Join(dir, ".baron", "logs", brn+".summary")
}

// ViewerSessionName names the read-only viewer tmux session that renders one
// agent window, keyed by pid so concurrent viewers of the same window do not
// collide.
func ViewerSessionName(window string, pid int) string {
	return tmux.ViewerPrefix + window + "-" + strconv.Itoa(pid)
}
