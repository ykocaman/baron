package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// tmuxRunRunner scripts a healthy tmux server for `baron run` and records
// every tmux invocation. tmuxOut is the `tmux -V` answer; an empty string
// (tmux unavailable) must route runs back to the direct exec path.
type tmuxRunRunner struct {
	inner   *runGateRunner
	tmuxOut string
	calls   [][]string
}

func (r *tmuxRunRunner) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	if name != "tmux" {
		return r.inner.Run(ctx, name, args, opts)
	}
	r.calls = append(r.calls, append([]string(nil), args...))
	switch {
	case slices.Equal(args, []string{"-V"}):
		return tool.Result{Stdout: r.tmuxOut}, nil
	case args[0] == "has-session" || args[0] == "kill-window" || args[0] == "new-window":
		return tool.Result{}, nil
	case args[0] == "capture-pane":
		// The wrapper shell echoes the done sentinel when the agent exits;
		// without it the run would never be detected as complete.
		return tool.Result{Stdout: "model output\n" + domain.DoneSentinel + "=0\n"}, nil
	case args[0] == "display-message":
		return tool.Result{Stdout: "1"}, nil // pane dead on the first poll
	}
	return tool.Result{}, nil
}

func tmuxConfig(t *testing.T, tmux string) func(string) (*store.Config, error) {
	t.Helper()
	return func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.TUI.Tmux = tmux
		return cfg, nil
	}
}

func TestRunTmuxNeverKeepsDirectExec(t *testing.T) {
	runner := &tmuxRunRunner{inner: &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"}}
	a := newTestApp(t, runner)
	a.loadConfig = tmuxConfig(t, "never")
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("tmux calls = %v, want none with tui.tmux=never", runner.calls)
	}
	if len(runner.inner.modelCall) != 1 {
		t.Errorf("model calls = %d, want 1 (direct exec)", len(runner.inner.modelCall))
	}
	if !strings.Contains(stdout(t, a), "gate passed") {
		t.Errorf("stdout = %q, want the gate to pass after the model launch", stdout(t, a))
	}
}

func TestRunTmuxAlwaysLaunchesModelInTmux(t *testing.T) {
	runner := &tmuxRunRunner{
		tmuxOut: "tmux 3.4",
		inner:   &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", gateFailAttempts: -1},
	}
	a := newTestApp(t, runner)
	a.loadConfig = tmuxConfig(t, "always")
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.inner.modelCall) != 0 {
		t.Errorf("model calls = %d, want 0 (the model runs in the tmux window, not a direct subprocess)", len(runner.inner.modelCall))
	}
	workDir := a.worktrees.Path(a.idOf(domain.BRN("baron-a1b2c3")))
	wantNW := []string{
		"new-window", "-t", "baron:", "-n", "baron-a1b2c3",
		"-c", workDir,
		"--", "sh", "-c", domain.LaunchScript("claude", []string{"-p", "Implement auth"}, domain.TaskAgentEnv(workDir)),
	}
	if !findCall(runner.calls, wantNW) {
		t.Errorf("tmux calls %v, want a new-window call %v", runner.calls, wantNW)
	}
	if !strings.Contains(stdout(t, a), "gate passed") {
		t.Errorf("stdout = %q, want the gate to pass after the tmux-launched model completes", stdout(t, a))
	}
}

func TestRunTmuxUnavailableFallsBackToDirectExec(t *testing.T) {
	runner := &tmuxRunRunner{inner: &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"}}
	a := newTestApp(t, runner)
	a.loadConfig = tmuxConfig(t, "always")
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.calls) != 1 || !slices.Equal(runner.calls[0], []string{"-V"}) {
		t.Errorf("tmux calls = %v, want only the availability probe [-V] when tmux is missing", runner.calls)
	}
	if len(runner.inner.modelCall) != 1 {
		t.Errorf("model calls = %d, want 1 (direct exec fallback)", len(runner.inner.modelCall))
	}
}

// findCall reports whether calls contains an argv equal to want.
func findCall(calls [][]string, want []string) bool {
	for _, c := range calls {
		if slices.Equal(c, want) {
			return true
		}
	}
	return false
}
