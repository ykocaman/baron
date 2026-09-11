package domain

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/profile"
	"github.com/baron-cli/baron/internal/tool"
)

// GateProfileTimeout returns how long a gate run against p may reasonably
// take: the sum of every step's effective timeout (its own Timeout if set,
// else defaultSeconds — mirrors GateRunner.Run's own per-step fallback),
// scaled by margin. A bead sitting in "validating" longer than this without
// any gate/status audit activity is a candidate for the reconciler to treat
// as stuck, not a slow-but-healthy profile — margin exists so a profile
// that's merely slow (a large test suite) isn't mistaken for one that's
// stuck.
func GateProfileTimeout(p profile.Profile, defaultSeconds int, margin float64) time.Duration {
	total := 0
	for _, step := range p.Steps {
		t := step.Timeout
		if t <= 0 {
			t = defaultSeconds
		}
		total += t
	}
	return time.Duration(float64(total) * margin * float64(time.Second))
}

// GateResult holds the result of a single gate step.
type GateResult struct {
	Name     string
	Command  string
	Success  bool
	ExitCode int
	Output   string
	Duration time.Duration
	Error    error
}

// GateReport holds the full gate run results.
type GateReport struct {
	Profile  string
	Results  []GateResult
	Success  bool // true only if ALL steps passed
	Duration time.Duration
}

// GateRunner executes gate steps.
type GateRunner struct {
	runner   tool.Runner
	timeout  int // default timeout per step (seconds)
	failFast bool
}

// NewGateRunner creates a gate runner. failFast is optional: when true, the
// run stops at the first failing step instead of producing a full report.
func NewGateRunner(runner tool.Runner, timeout int, failFast ...bool) *GateRunner {
	g := &GateRunner{runner: runner, timeout: timeout}
	if len(failFast) > 0 {
		g.failFast = failFast[0]
	}
	return g
}

// Run executes profile steps sequentially. Every step runs even if an
// earlier one fails (full-report semantics) unless the runner is in
// fail-fast mode, which stops at the first failure. The returned error is
// always nil; failures are recorded per step and in report.Success.
func (g *GateRunner) Run(ctx context.Context, dir string, profile profile.Profile) (GateReport, error) {
	report := GateReport{Profile: profile.Name, Success: true}
	start := time.Now()
	for _, step := range profile.Steps {
		result := GateResult{Name: step.Name}
		result.Command = strings.TrimSpace(step.Command + " " + strings.Join(step.Args, " "))
		timeout := g.timeout
		if step.Timeout > 0 {
			timeout = step.Timeout
		}
		res, err := g.runner.Run(ctx, step.Command, step.Args, tool.Options{
			Dir:      dir,
			Timeout:  time.Duration(timeout) * time.Second,
			CleanEnv: true, // credential isolation
		})
		result.Output = combineOutput(res)
		result.Duration = res.Duration
		result.Error = err
		result.ExitCode = res.ExitCode
		result.Success = err == nil && outputClean(step, res)
		report.Results = append(report.Results, result)
		if !result.Success {
			report.Success = false
			if g.failFast {
				break
			}
		}
	}
	report.Duration = time.Since(start)
	return report, nil
}

// outputClean reports whether an output-sensitive step produced no output.
// gofmt -l and go mod tidy -diff report problems via output while still
// exiting 0, so non-empty output means the step failed.
func outputClean(step profile.GateStep, res tool.Result) bool {
	outputSensitive := step.Command == "gofmt" ||
		(step.Command == "go" && slices.Contains(step.Args, "tidy") && slices.Contains(step.Args, "-diff"))
	if !outputSensitive {
		return true
	}
	return strings.TrimSpace(res.Stdout+res.Stderr) == ""
}

// combineOutput merges stdout and stderr into a single string.
func combineOutput(res tool.Result) string {
	switch {
	case res.Stdout == "":
		return res.Stderr
	case res.Stderr == "":
		return res.Stdout
	default:
		return res.Stdout + "\n" + res.Stderr
	}
}
