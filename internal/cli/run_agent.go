package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tmux"
)

func opencodeFailureReason() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".local", "share", "opencode", "log")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var name string
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Before(newest) {
			continue
		}
		newest, name = info.ModTime(), e.Name()
	}
	if name == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	// Only the tail matters: the run that just failed is the newest entry.
	if len(data) > 64<<10 {
		data = data[len(data)-64<<10:]
	}
	return matchOpencodeFailure(reverseLines(string(data)))
}

// matchOpencodeFailure scans opencode session log lines (newest first) for
// a known failure shape and returns a short, human-readable summary — "" if
// none of them match.
func matchOpencodeFailure(lines []string) string {
	for _, ln := range lines {
		if reason := matchRateLimit(ln); reason != "" {
			return reason
		}
		if reason := matchStreamError(ln); reason != "" {
			return reason
		}
		if reason := matchServerError(ln); reason != "" {
			return reason
		}
	}
	return ""
}

func matchRateLimit(line string) string {
	if i := strings.Index(line, "Rate limit"); i >= 0 {
		return clipReason(line[i:])
	}
	return ""
}

func matchStreamError(line string) string {
	i := strings.Index(line, "stream error")
	if i < 0 {
		return ""
	}
	_, tail, ok := strings.Cut(line[i:], "error.error=")
	if !ok {
		return ""
	}
	return clipReason(tail)
}

func matchServerError(line string) string {
	if !strings.Contains(line, "UnknownError") {
		return ""
	}
	_, tail, ok := strings.Cut(line, "ref=")
	if !ok {
		return ""
	}
	ref, _, _ := strings.Cut(tail, " ")
	if ref == "" {
		return ""
	}
	return "server error, " + ref + " — see ~/.local/share/opencode/log for detail"
}

func reverseLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

func clipReason(s string) string {
	s = strings.TrimSpace(strings.Trim(s, `"`))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// classifyAgentFailure builds runBead's human-readable failure reason for a
// launch that ended in ErrSilentDeath or ErrAgentFailed: the silent-death
// case is a fixed message naming the threshold; the agent-failure case
// starts from the error text and enriches it — opencode's own session log
// tail when available, and (for a bare "exit code" failure not already
// flagged transient) the agent's own captured log tail — so a human parking
// in the queue sees more than "exit status 1".
func (a *app) classifyAgentFailure(err error, brn domain.BRN, threshold time.Duration) string {
	if !errors.Is(err, domain.ErrAgentFailed) {
		return fmt.Sprintf("silent death: no output for %s", threshold)
	}
	reason := err.Error()
	if extra := opencodeFailureReason(); extra != "" {
		reason += " [opencode: " + extra + "]"
	}
	if !isTransientFailureReason(reason) && strings.Contains(strings.ToLower(reason), "exit code") {
		reason = a.enrichWithLogTail(brn, reason)
	}
	return reason
}

// runBead launches bead's assigned model in its worktree, watched by a
// SilentDeathMonitor, then runs the gate once the model exits. Both a silent
// death and a gate failure spend the project's retry budget
// (gate.retry_budget, default 3; component error table
// treats them the same way); exhausting it parks the bead in the human queue
// . A passing gate leaves the bead
// in "validating": becoming ready to merge requires the secret scan and
// signed-commit checks (baron-d0p, baron-fp4), not built yet.
func (a *app) runBead(ctx context.Context, brn domain.BRN, bead store.Bead, ag agent.Agent, wt domain.Worktree) error {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return err
	}
	budget := cfg.Gate.RetryBudget
	threshold := time.Duration(cfg.General.SilentDeathThreshold) * time.Minute
	monitor := a.setupBeadMonitor(ctx, cfg, brn, threshold)

	basePrompt := domain.ProjectContext(a.dir) + domain.Prompt(bead.Title, bead.Description, bead.AcceptanceCriteria)
	prompt := basePrompt
	state := resolveDomainState(bead)

	attempt := 0
	for {
		nextState, nextPrompt, done, err := a.runBeadAttempt(ctx, runBeadAttemptParams{
			brn: brn, bead: bead, ag: ag, wt: wt, monitor: monitor, state: state, prompt: prompt,
			basePrompt: basePrompt, threshold: threshold, attempt: &attempt, budget: budget,
		})
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		state, prompt = nextState, nextPrompt
	}
}

type runBeadAttemptParams struct {
	brn                domain.BRN
	bead               store.Bead
	ag                 agent.Agent
	wt                 domain.Worktree
	monitor            *domain.SilentDeathMonitor
	state              domain.BeadState
	prompt, basePrompt string
	threshold          time.Duration
	attempt            *int
	budget             int
}

func (a *app) runBeadAttempt(ctx context.Context, p runBeadAttemptParams) (domain.BeadState, string, bool, error) {
	if err := a.transitionStatus(ctx, a.idOf(p.brn), p.state, domain.BeadStateWorking, a.systemActor()); err != nil {
		return p.state, p.prompt, false, err
	}
	startedAt := time.Now()
	res, err := p.monitor.Launch(ctx, p.ag, p.wt.Path, p.prompt)
	a.recordRun(string(p.brn), p.bead.Assignee, startedAt, time.Since(startedAt))
	if err != nil {
		if !errors.Is(err, domain.ErrSilentDeath) && !errors.Is(err, domain.ErrAgentFailed) {
			return p.state, p.prompt, false, err
		}
		state, prompt, retryErr := a.handleLaunchFailure(ctx, launchFailureParams{
			brn: p.brn, bead: &p.bead, ag: &p.ag, launchErr: err, state: domain.BeadStateWorking,
			threshold: p.threshold, attempt: p.attempt, budget: p.budget, basePrompt: p.basePrompt,
		})
		return state, prompt, false, retryErr
	}
	a.auditLog("run", string(p.brn), fmt.Sprintf("launched %s (%s)", p.bead.Assignee, res.Duration))
	a.outf("%s ran in %s (%s)\n", p.bead.Assignee, p.wt.Path, res.Duration)
	state, prompt, done, outcomeErr := a.handlePostAgentOutcome(ctx, postAgentOutcomeParams{
		brn: p.brn, bead: p.bead, wt: p.wt, state: domain.BeadStateWorking, attempt: p.attempt, budget: p.budget, basePrompt: p.basePrompt,
	})
	return state, prompt, done, outcomeErr
}

// setupBeadMonitor builds runBead's SilentDeathMonitor and, on the first
// runtime read of the tui.tmux/tui.tmux_session keys, arms it to launch the
// model inside a tmux window so its live output stays visible and
// attachable. "never", or a missing tmux binary, keeps the direct subprocess
// path. The window name matches the worktree naming (string(brn)).
func (a *app) setupBeadMonitor(ctx context.Context, cfg *store.Config, brn domain.BRN, threshold time.Duration) *domain.SilentDeathMonitor {
	monitor := domain.NewSilentDeathMonitor(a.backend, threshold)
	if !tmux.Usable(ctx, a.runner, cfg.TUI.Tmux) {
		return monitor
	}
	session := cfg.TUI.TmuxSession
	if session == "" {
		session = domain.DefaultTmuxSession
	}
	tmx := tmux.New(a.runner)
	tmx.Session = session
	monitor.Tmux = tmx
	// TmuxWindowName maps dotted child BRNs ("baron-4al.1") to a tmux-safe
	// name ("baron-4al-1"); the TUI derives targets the same way.
	monitor.TmuxWindow = domain.TmuxWindowName(string(brn))
	if path, err := a.prepareAgentLogPath(brn); err != nil {
		a.warn("agent log: %v", err)
	} else {
		monitor.LogPath = path
	}
	return monitor
}

// launchFailureParams bundles handleLaunchFailure's inputs — bundled rather
// than passed positionally since the natural parameter list pushes the
// count past 7. bead and ag are pointers because a successful reassignment
// mutates the caller's own loop variables (bead's Assignee/Model, and ag
// itself) exactly like the original inline code did.
type launchFailureParams struct {
	brn        domain.BRN
	bead       *store.Bead
	ag         *agent.Agent
	launchErr  error
	state      domain.BeadState
	threshold  time.Duration
	attempt    *int
	budget     int
	basePrompt string
}

// handleLaunchFailure is runBead's silent-death/agent-failure branch: record
// the failure against the retry budget, reassign to a different model on a
// billing/transient reason, and hand back the state/prompt the next loop
// iteration should continue with.
func (a *app) handleLaunchFailure(ctx context.Context, p launchFailureParams) (domain.BeadState, string, error) {
	reason := a.classifyAgentFailure(p.launchErr, p.brn, p.threshold)
	exhausted, newState, err := a.recordFailure(ctx, p.brn, p.state, p.attempt, p.budget, reason, a.systemActor())
	if err != nil {
		return newState, "", err
	}
	if exhausted {
		return newState, "", silentError{code: ExitGateFail}
	}
	if isBillingFailure(reason) || isTransientFailureReason(reason) {
		if _, ok := a.reassignOnTransientFailure(ctx, agent.LoadCatalog(), p.bead, reason); ok {
			a.outf("reassigned %s to %s (%s) after transient failure\n", p.brn, p.bead.Assignee, p.bead.Model())
			if newAg, ok := a.agents.Get(p.bead.Assignee); ok {
				*p.ag = newAg
			}
		}
	}
	return newState, p.basePrompt, nil
}

// postAgentOutcomeParams bundles handlePostAgentOutcome's inputs — bundled
// rather than passed positionally since the natural parameter list pushes
// the count past 7.
type postAgentOutcomeParams struct {
	brn        domain.BRN
	bead       store.Bead
	wt         domain.Worktree
	state      domain.BeadState
	attempt    *int
	budget     int
	basePrompt string
}

// handlePostAgentOutcome is runBead's success branch: run postAgentGate and
// translate its outcome into the next loop state, or done=true once the
// bead needs nothing further from this loop (including the error outcomes,
// returned as err rather than a special done state since the caller returns
// on any non-nil err the same way).
func (a *app) handlePostAgentOutcome(ctx context.Context, p postAgentOutcomeParams) (newState domain.BeadState, prompt string, done bool, err error) {
	outcome, newState, gateSummary, err := a.postAgentGate(ctx, postAgentGateParams{
		brn: p.brn, bead: p.bead, wt: p.wt, state: p.state, attempt: p.attempt, budget: p.budget, trigger: "agent-exited", parkIfNoChange: false,
	})
	if err != nil {
		return newState, "", false, err
	}
	switch outcome {
	case postAgentBlocked:
		return newState, "", false, silentError{code: ExitHuman}
	case postAgentRetryExhausted:
		return newState, "", false, silentError{code: ExitGateFail}
	case postAgentRetry:
		return newState, p.basePrompt + "\n\n" + gateSummary, false, nil
	default: // postAgentDone
		return newState, "", true, nil
	}
}

// postAgentOutcome is what postAgentGate decided after one agent run.
