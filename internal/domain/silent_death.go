package domain

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/tmux"
	"github.com/baron-cli/baron/internal/tool"
)

// ErrSilentDeath is returned when a launched model produces no output for
// threshold and is killed.
var ErrSilentDeath = errors.New("silent death: no output from model process")

// ErrAgentFailed is returned when the agent process exits non-zero (the
// wrapper shell echoes baron-run-exit=<code> after it): the run produced
// output but the model failed — e.g. a provider error the agent rendered as
// "Error: Aborted". Distinct from ErrSilentDeath, which means no output
// arrived at all before the watchdog fired.
var ErrAgentFailed = errors.New("agent failed (non-zero exit)")

// SilentDeathMonitor wraps a Backend launch with a liveness watchdog: if the
// model process produces no stdout/stderr for threshold, the process is
// killed and Launch returns ErrSilentDeath. Recovery (retry budget, human
// queue) is the caller's job (cli.runBead), same as a gate failure.
type SilentDeathMonitor struct {
	backend       *Backend
	threshold     time.Duration
	checkInterval time.Duration // how often liveness is checked; small in tests

	// Tmux, set together with TmuxWindow, launches the model inside a tmux
	// window instead of a direct subprocess, so its live
	// output stays visible/attachable. nil keeps the direct-exec path
	// byte-identical.
	Tmux       *tmux.Client
	TmuxWindow string
	// LogPath, when set alongside Tmux/TmuxWindow, is where the tmux pane's
	// output is also teed (pipe-pane) so it survives the window being
	// killed on completion. Empty disables the tee.
	LogPath string
}

// NewSilentDeathMonitor creates a monitor. checkInterval of 0 defaults to
// threshold/10, capped at 5s, so tests can use short thresholds without
// tuning a separate interval.
func NewSilentDeathMonitor(backend *Backend, threshold time.Duration) *SilentDeathMonitor {
	interval := threshold / 10
	if interval <= 0 || interval > 5*time.Second {
		interval = min(threshold, 5*time.Second)
	}
	return &SilentDeathMonitor{backend: backend, threshold: threshold, checkInterval: interval}
}

// Launch runs m in dir with prompt, killing it and returning ErrSilentDeath
// if no output arrives within threshold. A zero threshold disables
// monitoring (the process runs unwatched, like Backend.Launch).
func (sd *SilentDeathMonitor) Launch(ctx context.Context, m agent.Agent, dir, prompt string) (tool.Result, error) {
	if sd.threshold <= 0 {
		return sd.backend.Launch(ctx, m, dir, prompt, nil)
	}
	if sd.Tmux != nil && sd.TmuxWindow != "" {
		name, args, err := BuildCmd(m, prompt)
		if err != nil {
			return tool.Result{}, err
		}
		return LaunchInTmux(ctx, sd.Tmux, windowSpec{Window: sd.TmuxWindow, Cmd: name, Args: args, Dir: dir, Threshold: sd.threshold, LogPath: sd.LogPath})
	}

	var mu sync.Mutex
	lastActivity := time.Now()
	onOutput := func() {
		mu.Lock()
		lastActivity = time.Now()
		mu.Unlock()
	}

	watchCtx, cancel := context.WithCancel(ctx)
	var timedOut atomic.Bool
	died := make(chan struct{})
	go func() {
		defer close(died)
		ticker := time.NewTicker(sd.checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				silent := time.Since(lastActivity)
				mu.Unlock()
				if silent >= sd.threshold {
					timedOut.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	res, err := sd.backend.Launch(watchCtx, m, dir, prompt, onOutput)
	cancel() // stop the watcher goroutine (a no-op if it already fired) so <-died unblocks
	<-died
	if timedOut.Load() {
		return res, ErrSilentDeath
	}
	return res, err
}
