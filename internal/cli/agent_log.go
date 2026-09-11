package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/baron-cli/baron/internal/domain"
)

// agentLogPath is where a bead's tmux pane output is teed (pipe-pane) and
// later read back from once the window is gone. Both the writer (runBead's
// LaunchInTmux call) and the reader (the TUI's live-agent fallback) use
// this same path, so there is exactly one place an agent's output lives
// regardless of whether the tmux window is still alive.
func agentLogPath(dir string, brn domain.BRN) string {
	return domain.AgentLogPath(dir, string(brn))
}

// prepareAgentLogPath returns brn's log path, creating its parent directory
// if needed.
func (a *app) prepareAgentLogPath(brn domain.BRN) (string, error) {
	path := agentLogPath(a.dir, brn)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return path, nil
}

// runSummaryPath is where a bead's one-shot run summary (the `baron run`
// stdout: agent echo + gate report) is persisted. The tmux pane and the
// pipe-pane agent log never contain the gate text, so the TUI reads this
// back after a restart to keep the result visible.
func runSummaryPath(dir string, brn domain.BRN) string {
	return domain.RunSummaryPath(dir, string(brn))
}

// readRunSummary reads brn's persisted run summary, nil with no error when
// none exists yet.
func (a *app) readRunSummary(brn domain.BRN) ([]string, error) {
	data, err := os.ReadFile(runSummaryPath(a.dir, brn))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

// writeRunSummary persists brn's run summary for the next session.
func (a *app) writeRunSummary(brn domain.BRN, lines []string) error {
	path := runSummaryPath(a.dir, brn)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}
