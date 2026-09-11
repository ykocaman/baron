package domain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/tmux"
	"github.com/baron-cli/baron/internal/tool"
)

// scriptStep scripts one tmux argv's responses; fn receives the zero-based
// occurrence index of that argv so tests can change behavior across polls
// (e.g. the pane dying on the Nth liveness check).
type scriptStep struct {
	argv []string
	fn   func(int) (tool.Result, error)
}

// tmuxScriptedRunner records every argv it receives and answers scripted calls
// by exact argv; unscripted calls succeed with empty output.
type tmuxScriptedRunner struct {
	mu    sync.Mutex
	calls [][]string
	steps []scriptStep
}

func (s *tmuxScriptedRunner) Run(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
	s.mu.Lock()
	n := 0
	for _, c := range s.calls {
		if slices.Equal(c, args) {
			n++
		}
	}
	s.calls = append(s.calls, append([]string(nil), args...))
	s.mu.Unlock()
	for _, st := range s.steps {
		if slices.Equal(st.argv, args) {
			return st.fn(n)
		}
	}
	return tool.Result{}, nil
}

func script(argv []string, fn func(int) (tool.Result, error)) scriptStep {
	return scriptStep{argv: argv, fn: fn}
}

func ok(stdout string) func(int) (tool.Result, error) {
	return func(int) (tool.Result, error) { return tool.Result{Stdout: stdout}, nil }
}

var _ tool.Runner = (*tmuxScriptedRunner)(nil)

const (
	testWindow = "BRN-1"
	testDir    = "/work"
)

var (
	hasSession = script([]string{"has-session", "-t", "baron"}, ok(""))
	killWindow = script([]string{"kill-window", "-t", "baron:" + testWindow}, ok(""))
	// newWindow scripts the wrapper-shell Spawn: the agent runs inside
	// `sh -c <LaunchScript>`, so the pane survives completion.
	newWindow = func(cmd string, args []string) scriptStep {
		return script([]string{"new-window", "-t", "baron:", "-n", testWindow, "-c", testDir, "--", "sh", "-c", LaunchScript(cmd, args)}, ok(""))
	}
	capturePane = func(fn func(int) (tool.Result, error)) scriptStep {
		return script([]string{"capture-pane", "-t", "baron:" + testWindow, "-p", "-S", "-200"}, fn)
	}
)

func TestLaunchInTmuxHappyPath(t *testing.T) {
	stub := &tmuxScriptedRunner{steps: []scriptStep{
		hasSession,
		killWindow,
		newWindow("claude", []string{"-p", "hello"}),
		capturePane(func(n int) (tool.Result, error) {
			if n < 2 {
				return tool.Result{Stdout: fmt.Sprintf("output line %d\n", n)}, nil
			}
			// The wrapper echoes the sentinel right after the agent exits.
			return tool.Result{Stdout: "output line 2\n" + DoneSentinel + "=0\n"}, nil
		}),
	}}

	res, err := LaunchInTmux(context.Background(), tmux.New(stub), windowSpec{Window: testWindow, Cmd: "claude", Args: []string{"-p", "hello"}, Dir: testDir, Threshold: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("LaunchInTmux: %v", err)
	}
	if res.Stdout != "output line 2\n"+DoneSentinel+"=0\n" {
		t.Errorf("Stdout = %q, want the last captured pane content (with the sentinel)", res.Stdout)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %s, want > 0", res.Duration)
	}
	wantCalls(t, stub.calls, [][]string{
		{"has-session", "-t", "baron"},
		{"kill-window", "-t", "baron:BRN-1"},
		{"new-window", "-t", "baron:", "-n", "BRN-1", "-c", "/work", "--", "sh", "-c", LaunchScript("claude", []string{"-p", "hello"}, TaskAgentEnv(testDir))},
		{"capture-pane", "-t", "baron:BRN-1", "-p", "-S", "-200"},
		{"capture-pane", "-t", "baron:BRN-1", "-p", "-S", "-200"},
		{"capture-pane", "-t", "baron:BRN-1", "-p", "-S", "-200"},
	})
}

func TestLaunchInTmuxSilentDeath(t *testing.T) {
	stub := &tmuxScriptedRunner{steps: []scriptStep{
		hasSession,
		killWindow,
		newWindow("claude", []string{"-p", "hello"}),
		capturePane(ok("stuck output\n")),
	}}

	res, err := LaunchInTmux(context.Background(), tmux.New(stub), windowSpec{Window: testWindow, Cmd: "claude", Args: []string{"-p", "hello"}, Dir: testDir, Threshold: 30 * time.Millisecond})
	if !errors.Is(err, ErrSilentDeath) {
		t.Fatalf("err = %v, want ErrSilentDeath", err)
	}
	if res.Stdout != "stuck output\n" {
		t.Errorf("Stdout = %q, want the last captured content", res.Stdout)
	}
	kills := 0
	for _, c := range stub.calls {
		if slices.Equal(c, []string{"kill-window", "-t", "baron:" + testWindow}) {
			kills++
		}
	}
	if kills < 2 {
		t.Errorf("kill-window calls = %d, want >= 2 (Spawn's pre-kill + the silent-death kill)", kills)
	}
}

func TestLaunchInTmuxWindowDestroyedOnExit(t *testing.T) {
	gone := func(n int) (tool.Result, error) {
		if n == 0 {
			return tool.Result{Stdout: "work\n"}, nil
		}
		return tool.Result{Stderr: "can't find window: baron:BRN-1"}, errors.New("exit status 1")
	}
	stub := &tmuxScriptedRunner{steps: []scriptStep{
		hasSession,
		killWindow,
		newWindow("claude", []string{"-p", "hello"}),
		capturePane(gone),
	}}

	res, err := LaunchInTmux(context.Background(), tmux.New(stub), windowSpec{Window: testWindow, Cmd: "claude", Args: []string{"-p", "hello"}, Dir: testDir, Threshold: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("LaunchInTmux: %v, want a clean completion when the window vanishes", err)
	}
	if res.Stdout != "work\n" {
		t.Errorf("Stdout = %q, want the content captured before the window vanished", res.Stdout)
	}
	wantCalls(t, stub.calls, [][]string{
		{"has-session", "-t", "baron"},
		{"kill-window", "-t", "baron:BRN-1"},
		{"new-window", "-t", "baron:", "-n", "BRN-1", "-c", "/work", "--", "sh", "-c", LaunchScript("claude", []string{"-p", "hello"}, TaskAgentEnv(testDir))},
		{"capture-pane", "-t", "baron:BRN-1", "-p", "-S", "-200"},
		{"capture-pane", "-t", "baron:BRN-1", "-p", "-S", "-200"},
	})
}

func TestLaunchInTmuxAgentFailed(t *testing.T) {
	stub := &tmuxScriptedRunner{steps: []scriptStep{
		hasSession,
		killWindow,
		newWindow("opencode", []string{"run", "hello"}),
		capturePane(func(n int) (tool.Result, error) {
			if n < 2 {
				return tool.Result{Stdout: fmt.Sprintf("thinking line %d\n", n)}, nil
			}
			return tool.Result{Stdout: "✱ Glob *.go\nError: Aborted\n" + DoneSentinel + "=1\n"}, nil
		}),
	}}

	_, err := LaunchInTmux(context.Background(), tmux.New(stub), windowSpec{Window: testWindow, Cmd: "opencode", Args: []string{"run", "hello"}, Dir: testDir, Threshold: 50 * time.Millisecond})
	if !errors.Is(err, ErrAgentFailed) {
		t.Fatalf("err = %v, want ErrAgentFailed", err)
	}
	if !strings.Contains(err.Error(), "exit code 1") {
		t.Errorf("err = %q, want the sentinel exit code in the message", err)
	}
	if !strings.Contains(err.Error(), "Error: Aborted") {
		t.Errorf("err = %q, want the agent's error line in the message", err)
	}
	// The window stays open for inspection on failure: only Spawn's pre-kill
	// runs, never a kill on the failed run itself.
	kills := 0
	for _, c := range stub.calls {
		if slices.Equal(c, []string{"kill-window", "-t", "baron:" + testWindow}) {
			kills++
		}
	}
	if kills != 1 {
		t.Errorf("kill-window calls = %d, want exactly 1 (only Spawn's pre-kill — the failed window stays inspectable)", kills)
	}
}

// recordingRunner records every direct exec and returns success immediately.
type recordingRunner struct {
	mu    sync.Mutex
	execs [][]string
}

func (r *recordingRunner) Run(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
	r.mu.Lock()
	r.execs = append(r.execs, append([]string{name}, args...))
	r.mu.Unlock()
	return tool.Result{}, nil
}

func TestLaunchInTmuxAbortKeepsWindow(t *testing.T) {
	stub := &tmuxScriptedRunner{steps: []scriptStep{
		hasSession,
		killWindow,
		newWindow("claude", []string{"-p", "hello"}),
		capturePane(ok("partial output\n")),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // baron aborted before the first poll

	_, err := LaunchInTmux(ctx, tmux.New(stub), windowSpec{Window: testWindow, Cmd: "claude", Args: []string{"-p", "hello"}, Dir: testDir, Threshold: 30 * time.Millisecond})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	kills := 0
	for _, c := range stub.calls {
		if slices.Equal(c, []string{"kill-window", "-t", "baron:" + testWindow}) {
			kills++
		}
	}
	if kills != 1 {
		t.Errorf("kill-window calls = %d, want exactly 1 (only Spawn's pre-kill — the window stays after abort)", kills)
	}
}

func TestSilentDeathMonitorTmuxNilUsesDirectExec(t *testing.T) {
	rec := &recordingRunner{}
	monitor := NewSilentDeathMonitor(NewBackend(rec), time.Minute)
	m := agent.Agent{Name: "claude", Command: "claude", Args: []string{"-p", "{{prompt}}"}}

	if _, err := monitor.Launch(context.Background(), m, "/work", "do work"); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	want := [][]string{{"claude", "-p", "do work"}}
	if !slices.EqualFunc(rec.execs, want, slices.Equal) {
		t.Errorf("execs = %v, want the Backend.Launch-style direct call %v", rec.execs, want)
	}
}

func TestSilentDeathMonitorTmuxSetRoutesToTmux(t *testing.T) {
	stub := &tmuxScriptedRunner{steps: []scriptStep{
		hasSession,
		killWindow,
		newWindow("claude", []string{"-p", "do work"}),
		capturePane(ok("model says hi\n" + DoneSentinel + "=0\n")),
	}}
	monitor := NewSilentDeathMonitor(NewBackend(stub), 30*time.Millisecond)
	monitor.Tmux = tmux.New(stub)
	monitor.TmuxWindow = testWindow
	m := agent.Agent{Name: "claude", Command: "claude", Args: []string{"-p", "{{prompt}}"}}

	res, err := monitor.Launch(context.Background(), m, "/work", "do work")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.Stdout != "model says hi\n"+DoneSentinel+"=0\n" {
		t.Errorf("Stdout = %q, want the captured pane content", res.Stdout)
	}
	wantCalls(t, stub.calls, [][]string{
		{"has-session", "-t", "baron"},
		{"kill-window", "-t", "baron:BRN-1"},
		{"new-window", "-t", "baron:", "-n", "BRN-1", "-c", "/work", "--", "sh", "-c", LaunchScript("claude", []string{"-p", "do work"}, TaskAgentEnv("/work"))},
		{"capture-pane", "-t", "baron:BRN-1", "-p", "-S", "-200"},
	})
}

// wantCalls asserts the exact argv sequence the runner received.
func wantCalls(t *testing.T, got, want [][]string) {
	t.Helper()
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("runner argv calls:\n got: %v\nwant: %v", got, want)
	}
}
