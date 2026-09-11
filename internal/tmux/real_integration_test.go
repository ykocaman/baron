package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/baron-cli/baron/internal/tool"
)

type realRunnerFunc func(context.Context, string, []string, tool.Options) (tool.Result, error)

func (f realRunnerFunc) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	return f(ctx, name, args, opts)
}

// attachCmd builds the exec.Cmd for argv (AttachArgv's output) with a
// guaranteed-valid TERM. A real BARON process always has one — it's
// launched from a human's interactive shell, which bash/zsh guarantee sets
// TERM — but the process running `go test` is not necessarily one (CI
// runners, some sandboxes, some non-interactive harnesses leave it unset).
// tmux refuses to attach a pty whose TERM doesn't resolve to a terminfo
// entry ("open terminal failed: not a terminal" / "...does not support
// clear"), which otherwise fails this test for a reason having nothing to
// do with the tmux lifecycle behavior it actually exercises. Explicitly
// setting TERM here reproduces the guarantee a real terminal always gives,
// the same way production implicitly relies on it.
func attachCmd(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	env := os.Environ()
	for i, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			env[i] = "TERM=xterm-256color"
			cmd.Env = env
			return cmd
		}
	}
	env = append(env, "TERM=xterm-256color")
	cmd.Env = env
	return cmd
}

// readUntilContains reads from f, accumulating output, until it contains
// want or timeout elapses (whichever first) — breaking early on a
// non-timeout read error too, since that can never resolve into a match.
// Returns whatever was collected either way; the caller decides whether
// "want never appeared" is fatal, and with what message.
func readUntilContains(t *testing.T, f *os.File, want string, timeout time.Duration) string {
	t.Helper()
	buf := make([]byte, 8192)
	var collected strings.Builder
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = f.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, rerr := f.Read(buf)
		if n > 0 {
			collected.Write(buf[:n])
		}
		if strings.Contains(collected.String(), want) {
			break
		}
		if rerr != nil && !os.IsTimeout(rerr) {
			break
		}
	}
	return collected.String()
}

// TestRealTmuxEnsureRunningAndViewerLifecycle exercises the real tmux
// binary end to end (skipped when tmux isn't installed): a window's agent
// process must keep running independent of any attached viewer, and a
// fresh viewer must be able to reattach and see its still-live output —
// the whole reason EnsureRunning/EnsureViewer exist. This caught a real
// bug once already: WindowExists used to call `display-message -p -t
// session:window`, which real tmux does NOT error on for a missing window
// (it silently falls back to the session's *current* window) — the stub
// tests alone couldn't have caught that, since the stub only models
// whatever behavior it's told to.
func TestRealTmuxEnsureRunningAndViewerLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	runner := realRunnerFunc(tool.Run)
	c := &Client{Runner: runner, Session: "baron-tmux-integration-test"}
	defer func() {
		_, _ = runner.Run(context.Background(), "tmux", []string{"kill-session", "-t", "baron-tmux-integration-test"}, tool.Options{CleanEnv: true})
	}()

	ctx := context.Background()
	window := "w1"
	script := "i=0; while [ $i -lt 100 ]; do i=$((i+1)); echo TICK-$i; sleep 0.1; done; echo baron-run-exit=0; exec ${SHELL:-/bin/sh}"

	started, err := c.EnsureRunning(ctx, window, script, "")
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Fatal("started = false, want true (fresh window)")
	}

	// Reconnect must not kill/restart the already-running window.
	started2, err := c.EnsureRunning(ctx, window, script, "")
	if err != nil {
		t.Fatalf("EnsureRunning (reconnect): %v", err)
	}
	if started2 {
		t.Fatal("started = true on reconnect, want false (must not respawn)")
	}

	viewer := "baron-view-w1-test"
	if err := c.EnsureViewer(ctx, viewer, window); err != nil {
		t.Fatalf("EnsureViewer: %v", err)
	}
	defer func() { _ = c.KillViewer(ctx, viewer) }()

	argv := AttachArgv(viewer)
	cmd := attachCmd(ctx, argv)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 20, Cols: 60})
	if err != nil {
		t.Fatalf("pty.StartWithSize(attach): %v", err)
	}

	collected := readUntilContains(t, f, "TICK-3", 5*time.Second)
	if !strings.Contains(collected, "TICK-") {
		t.Fatalf("viewer pty never saw the agent's live output; got: %q", collected)
	}
	fmt.Println("OK: viewer saw live TICK output")

	// Detach the local viewer (close its pty) — the window must survive.
	_ = f.Close()
	_ = cmd.Process.Kill()
	time.Sleep(300 * time.Millisecond)

	alive, err := c.WindowExists(ctx, window)
	if err != nil {
		t.Fatalf("WindowExists after detach: %v", err)
	}
	if !alive {
		t.Fatal("window died when the local viewer detached — it must survive independently")
	}
	fmt.Println("OK: window survived viewer detach")

	// Reattach with a fresh viewer and confirm we see fresh content.
	viewer2 := "baron-view-w1-test-2"
	if err := c.EnsureViewer(ctx, viewer2, window); err != nil {
		t.Fatalf("EnsureViewer (reattach): %v", err)
	}
	defer func() { _ = c.KillViewer(ctx, viewer2) }()
	argv2 := AttachArgv(viewer2)
	cmd2 := attachCmd(ctx, argv2)
	f2, err := pty.StartWithSize(cmd2, &pty.Winsize{Rows: 20, Cols: 60})
	if err != nil {
		t.Fatalf("pty.StartWithSize(attach2): %v", err)
	}
	defer func() { _ = f2.Close(); _ = cmd2.Process.Kill() }()
	collected2 := readUntilContains(t, f2, "TICK-", 5*time.Second)
	if !strings.Contains(collected2, "TICK-") {
		t.Fatalf("reattached viewer never saw live output; got: %q", collected2)
	}
	fmt.Println("OK: reattached viewer saw live output from the still-running window")

	// Now actually kill the window and confirm it's really gone.
	if err := c.KillWindow(ctx, window); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	alive, err = c.WindowExists(ctx, window)
	if err != nil {
		t.Fatalf("WindowExists after KillWindow: %v", err)
	}
	if alive {
		t.Fatal("window still alive after KillWindow")
	}
	fmt.Println("OK: KillWindow actually stops the agent")
}
