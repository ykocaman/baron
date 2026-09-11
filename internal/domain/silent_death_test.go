package domain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/tool"
)

// blockingRunner ignores ctx cancellation signals from the test's point of
// view except that it respects ctx.Done() to return promptly, simulating a
// model process that hangs (produces no output) until killed.
type blockingRunner struct {
	onOutput func()
}

func (r *blockingRunner) Run(ctx context.Context, _ string, _ []string, opts tool.Options) (tool.Result, error) {
	if opts.OnOutput != nil && r.onOutput == nil {
		r.onOutput = opts.OnOutput
	}
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

func TestSilentDeathMonitorKillsHungProcess(t *testing.T) {
	backend := NewBackend(&blockingRunner{})
	monitor := NewSilentDeathMonitor(backend, 30*time.Millisecond)
	m := agent.Agent{Name: "claude", Command: "claude", Args: []string{"-p", "{{prompt}}"}}

	start := time.Now()
	_, err := monitor.Launch(context.Background(), m, "/tmp", "do work")
	elapsed := time.Since(start)

	if !errors.Is(err, ErrSilentDeath) {
		t.Fatalf("err = %v, want ErrSilentDeath", err)
	}
	if elapsed > time.Second {
		t.Errorf("Launch took %s, want it to give up close to the 30ms threshold", elapsed)
	}
}

// activeRunner emits output on a fixed cadence so the monitor should never
// consider it silent, then exits successfully.
type activeRunner struct {
	interval time.Duration
	ticks    int
}

func (r *activeRunner) Run(ctx context.Context, _ string, _ []string, opts tool.Options) (tool.Result, error) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for i := 0; i < r.ticks; i++ {
		select {
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		case <-ticker.C:
			if opts.OnOutput != nil {
				opts.OnOutput()
			}
		}
	}
	return tool.Result{}, nil
}

func TestSilentDeathMonitorLeavesActiveProcessAlone(t *testing.T) {
	backend := NewBackend(&activeRunner{interval: 10 * time.Millisecond, ticks: 5})
	monitor := NewSilentDeathMonitor(backend, 40*time.Millisecond)
	m := agent.Agent{Name: "claude", Command: "claude", Args: []string{"-p", "{{prompt}}"}}

	_, err := monitor.Launch(context.Background(), m, "/tmp", "do work")
	if err != nil {
		t.Fatalf("Launch() error = %v, want nil (process kept producing output)", err)
	}
}

func TestSilentDeathMonitorZeroThresholdDisablesWatch(t *testing.T) {
	backend := NewBackend(&blockingRunner{})
	monitor := NewSilentDeathMonitor(backend, 0)
	m := agent.Agent{Name: "claude", Command: "claude", Args: []string{"-p", "{{prompt}}"}}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := monitor.Launch(ctx, m, "/tmp", "do work")
	if errors.Is(err, ErrSilentDeath) {
		t.Errorf("err = %v, want a plain context deadline, not ErrSilentDeath, when monitoring is disabled", err)
	}
}
