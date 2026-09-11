package domain

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/tmux"
	"github.com/baron-cli/baron/internal/tool"
)

// captureLines is how many pane lines each poll captures; the last 200 lines
// are enough to judge liveness and carry the model's tail output into
// Result.Stdout.
const captureLines = 200

// DoneSentinel is echoed by the wrapper shell right after the agent process
// exits, so the poll loop can tell "finished" from "still running" without
// relying on pane death. tmux's default teardown would otherwise close the
// window the moment the process exits; the wrapper's trailing shell keeps the
// pane alive and the sentinel marks where the agent's output ends.
const DoneSentinel = "baron-run-exit"

// LaunchScript builds the pane command for an agent run: the agent command
// (with args) runs first, then the exit code is echoed as DoneSentinel and the
// pane is handed over to an interactive shell. The pane therefore never dies —
// the window and the agent's final output stay inspectable and attachable
// after completion. Each word is ShellQuoted so arbitrary agent args (spaces,
// single quotes, newlines) survive as one shell word.
//
// env, if given (only its first element is used — a variadic optional param,
// same convention as NewGateRunner's failFast), is exported before cmd runs;
// see TaskAgentEnv for the intended use (blocking a task-agent's bd access).
// Keys are sorted for a deterministic, testable script.
func LaunchScript(cmd string, args []string, env ...map[string]string) string {
	var b strings.Builder
	if len(env) > 0 {
		keys := make([]string, 0, len(env[0]))
		for k := range env[0] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("export ")
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(tmux.ShellQuote(env[0][k]))
			b.WriteString("; ")
		}
	}
	words := make([]string, 0, len(args)+1)
	words = append(words, tmux.ShellQuote(cmd))
	for _, a := range args {
		words = append(words, tmux.ShellQuote(a))
	}
	b.WriteString(strings.Join(words, " "))
	b.WriteString("; ec=$?; echo ")
	b.WriteString(DoneSentinel)
	b.WriteString("=$ec; exec ${SHELL:-/bin/sh}")
	return b.String()
}

// pollInterval picks LaunchInTmux's poll cadence from threshold: a tenth of
// it, capped at 5s so a long silent-death threshold still polls often enough
// to catch the sentinel promptly, and floored at 1s so a very short
// threshold doesn't poll needlessly fast.
func pollInterval(threshold time.Duration) time.Duration {
	interval := threshold / 10
	if interval <= 0 || interval > 5*time.Second {
		interval = min(threshold, 5*time.Second)
	}
	if interval <= 0 {
		interval = time.Second
	}
	return interval
}

// checkSentinel reports whether content carries the wrapper shell's
// DoneSentinel — the agent has exited — and, if so, the error a non-zero
// exit code (plus its own last error-line hint, when found) should surface
// as. done=false means the agent is still running.
func checkSentinel(content string) (done bool, err error) {
	if !strings.Contains(content, DoneSentinel) {
		return false, nil
	}
	if code := SentinelExitCode(content); code != 0 {
		reason := AgentErrorLine(content)
		if reason != "" {
			reason = " — " + reason
		}
		return true, fmt.Errorf("%w: exit code %d%s", ErrAgentFailed, code, reason)
	}
	return true, nil
}

// windowSpec bundles LaunchInTmux's launch-target parameters so startWindow
// stays within the package's argument-count limit — also LaunchInTmux's own
// launch-target parameters, for the same reason.
type windowSpec struct {
	Window    string
	Cmd       string
	Args      []string
	Dir       string
	Threshold time.Duration
	LogPath   string
}

// startWindow is LaunchInTmux's setup phase: resolve the launch env, ensure
// the session exists, spawn the wrapped script in spec.Window, and start
// piping its pane output to spec.LogPath when given (a log-pipe failure
// must never block the run).
func startWindow(ctx context.Context, tmx *tmux.Client, spec windowSpec, env ...map[string]string) error {
	launchEnv := TaskAgentEnv(spec.Dir)
	if len(env) > 0 {
		launchEnv = env[0]
	}
	if err := tmx.Ensure(ctx); err != nil {
		return err
	}
	if err := tmx.Spawn(ctx, spec.Window, "sh", spec.Dir, "-c", LaunchScript(spec.Cmd, spec.Args, launchEnv)); err != nil {
		return err
	}
	if spec.LogPath != "" {
		_ = tmx.PipePane(ctx, spec.Window, spec.LogPath)
	}
	return nil
}

// LaunchInTmux runs cmd with args in dir inside a tmux window of tmx's
// session: the model's live output stays visible and
// attachable while baron watches with the same silent-death semantics as the
// direct subprocess path. No new pane output for threshold kills the window
// and returns ErrSilentDeath.
//
// The agent runs inside a wrapper shell (LaunchScript) so the pane stays
// alive after the agent exits: the window and its final output remain
// inspectable and attachable after completion, instead of tmux tearing the
// window down with the process. The window is reaped on silent death, by the
// next Spawn of the same window name, or explicitly by the CLI/TUI once the
// bead is closed. Aborting baron (ctx cancelled) also leaves the window up —
// the agent keeps running and its output stays available until the bead is
// closed. Output is captured into Result for the gate/merge pipeline; when
// logPath is non-empty, the pane's output is also teed there (tmux pipe-pane)
// for reads after the window is gone.
//
// env optionally overrides the BEADS_DB isolation every task-agent gets by
// default (TaskAgentEnv(dir), applied when env is omitted entirely) — a
// variadic optional param, same convention as NewGateRunner's failFast.
// Crew Mode persona launches (docs/PRD/crew-mode.md §4.4) pass their own
// env explicitly: an empty map for a bd_write:true persona (real bd
// access, no isolation) or TaskAgentEnv(dir) spelled out for bd_write:false
// (identical to the default, just explicit at the call site).
func LaunchInTmux(ctx context.Context, tmx *tmux.Client, spec windowSpec, env ...map[string]string) (tool.Result, error) {
	start := time.Now()
	if err := startWindow(ctx, tmx, spec, env...); err != nil {
		return tool.Result{}, err
	}

	return waitForTmux(ctx, tmx, spec, start, pollInterval(spec.Threshold))
}

func waitForTmux(ctx context.Context, tmx *tmux.Client, spec windowSpec, start time.Time, interval time.Duration) (tool.Result, error) {
	lastActivity := time.Now()
	lastContent := ""
	// finish kills the window only on silent death — the process is still
	// alive there and must be reaped. Abort and normal completion leave the
	// window on screen (wrapper shell keeps the pane alive) for inspection;
	// Spawn pre-kills it on the next run, and closing the bead reaps it
	// explicitly.
	finish := func(content string, err error, kill bool) (tool.Result, error) {
		if kill {
			_ = tmx.KillWindow(ctx, spec.Window)
		}
		// ponytail: ExitCode is always 0 — tmux's pane_dead_status format is
		// newer than some supported releases; the pipeline consumes Duration
		// only.
		return tool.Result{Stdout: content, Duration: time.Since(start)}, err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Abort keeps the window: the agent keeps running and its output
			// stays inspectable until the bead is closed or re-run.
			return finish(lastContent, ctx.Err(), false)
		case <-ticker.C:
		}

		content, changed, gone, done, sentinelErr := pollTmux(ctx, tmx, spec, lastContent)
		if sentinelErr != nil {
			return finish(content, sentinelErr, false)
		}
		if done {
			return finish(content, nil, false)
		}
		if gone {
			return finish(lastContent, nil, false)
		}
		if changed {
			lastContent, lastActivity = content, time.Now()
		}

		if silentDeath(lastActivity, spec.Threshold) {
			return finish(lastContent, ErrSilentDeath, true)
		}
	}
}

func silentDeath(lastActivity time.Time, threshold time.Duration) bool {
	return time.Since(lastActivity) >= threshold
}

func pollTmux(ctx context.Context, tmx *tmux.Client, spec windowSpec, previous string) (content string, changed, gone, done bool, err error) {
	content, err = tmx.CapturePane(ctx, spec.Window, captureLines)
	if err != nil {
		if windowGone(err) || paneDead(ctx, tmx, spec.Window) {
			return "", false, true, false, nil
		}
		return previous, false, false, false, nil
	}
	if content != previous {
		changed = true
	}
	done, err = checkSentinel(content)
	return content, changed, false, done, err
}

// windowGone reports whether a tmux error means the window no longer
// exists. tmux prints "can't find window: <name>" on stderr when a window
// was torn down with its pane (the default when the pane's command exits).
func windowGone(err error) bool {
	return err != nil && strings.Contains(err.Error(), "can't find window")
}

// paneDead reports whether window's pane is dead via tmux's #{pane_dead}
// format ("1" = dead). A missing window — tmux's default teardown when the
// pane's command exits — reads as dead too.
func paneDead(ctx context.Context, tmx *tmux.Client, window string) bool {
	res, err := tmx.Runner.Run(ctx, "tmux", []string{
		"display-message", "-p", "-t", tmx.Session + ":" + window, "#{pane_dead}",
	}, tool.Options{CleanEnv: true})
	if err != nil {
		return strings.Contains(res.Stderr, "can't find")
	}
	return strings.Contains(res.Stdout, "1")
}

// SentinelExitCode parses the agent's exit code from the pane content once
// the wrapper's DoneSentinel line appears ("baron-run-exit=N"). 0 when the
// sentinel is absent or the code is unreadable.
func SentinelExitCode(content string) int {
	_, after, ok := strings.Cut(content, DoneSentinel)
	if !ok {
		return 0
	}
	rest := strings.TrimPrefix(after, "=")
	code := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		code = code*10 + int(r-'0')
	}
	return code
}

// AgentErrorLine returns the pane's last error-looking line before the
// sentinel ("Error: Aborted" and the like) so the failure reason mirrors what
// the user saw in the window. Empty when nothing matches.
func AgentErrorLine(content string) string {
	before, _, ok := strings.Cut(content, DoneSentinel)
	if !ok {
		return ""
	}
	lines := strings.Split(before, "\n")
	for _, line := range slices.Backward(lines) {
		ln := strings.TrimSpace(line)
		low := strings.ToLower(ln)
		if strings.Contains(low, "error") || strings.Contains(low, "aborted") || strings.Contains(low, "rate limit") {
			if len(ln) > 80 {
				ln = ln[:80] + "…"
			}
			return ln
		}
	}
	return ""
}
