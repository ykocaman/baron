package domain

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/profile"
	"github.com/baron-cli/baron/internal/tool"
)

// fakeRunner records Options and delegates to a scripted function.
type fakeRunner struct {
	fn   func(context.Context, string, []string, tool.Options) (tool.Result, error)
	opts []tool.Options
}

func (f *fakeRunner) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	f.opts = append(f.opts, opts)
	return f.fn(ctx, name, args, opts)
}

// exitRunner returns results in order, mapping a non-zero exit code to an error.
func exitRunner(results ...tool.Result) *fakeRunner {
	i := 0
	return &fakeRunner{fn: func(context.Context, string, []string, tool.Options) (tool.Result, error) {
		res := results[i]
		i++
		if res.ExitCode != 0 {
			return res, fmt.Errorf("exit status %d", res.ExitCode)
		}
		return res, nil
	}}
}

func TestGateRunAllPass(t *testing.T) {
	runner := exitRunner(tool.Result{ExitCode: 0}, tool.Result{ExitCode: 0})
	g := NewGateRunner(runner, 60)
	profile := profile.Profile{Name: "go", Steps: []profile.GateStep{
		{Name: "format", Command: "gofmt", Args: []string{"-l", "."}},
		{Name: "test", Command: "go", Args: []string{"test", "-count=1", "./..."}},
	}}

	report, err := g.Run(context.Background(), t.TempDir(), profile)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !report.Success {
		t.Errorf("report.Success = false, want true")
	}
	if len(report.Results) != 2 {
		t.Errorf("len(Results) = %d, want 2", len(report.Results))
	}
	for _, r := range report.Results {
		if !r.Success {
			t.Errorf("step %q Success = false, want true", r.Name)
		}
	}
	if report.Profile != "go" {
		t.Errorf("report.Profile = %q, want %q", report.Profile, "go")
	}
}

func TestGateRunOneFails(t *testing.T) {
	runner := exitRunner(tool.Result{ExitCode: 0}, tool.Result{ExitCode: 1})
	g := NewGateRunner(runner, 60)
	profile := profile.Profile{Name: "go", Steps: []profile.GateStep{
		{Name: "lint", Command: "go", Args: []string{"vet", "./..."}},
		{Name: "test", Command: "go", Args: []string{"test", "-count=1", "./..."}},
	}}

	report, err := g.Run(context.Background(), t.TempDir(), profile)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Success {
		t.Errorf("report.Success = true, want false")
	}
	if len(report.Results) != 2 {
		t.Fatalf("len(Results) = %d, want 2 (full-report continues past failures)", len(report.Results))
	}
	if !report.Results[0].Success {
		t.Errorf("step 1 Success = false, want true")
	}
	if report.Results[1].Success {
		t.Errorf("step 2 Success = true, want false")
	}
	if report.Results[1].Error == nil {
		t.Errorf("step 2 Error = nil, want exit error")
	}
}

func TestGateRunFailFast(t *testing.T) {
	runner := exitRunner(tool.Result{ExitCode: 1}, tool.Result{ExitCode: 0})
	g := NewGateRunner(runner, 60, true)
	profile := profile.Profile{Name: "go", Steps: []profile.GateStep{
		{Name: "lint", Command: "go", Args: []string{"vet", "./..."}},
		{Name: "test", Command: "go", Args: []string{"test", "-count=1", "./..."}},
	}}

	report, err := g.Run(context.Background(), t.TempDir(), profile)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Success {
		t.Errorf("report.Success = true, want false")
	}
	if len(report.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1 (fail-fast stops after the first failure)", len(report.Results))
	}
	if report.Results[0].ExitCode != 1 {
		t.Errorf("step 1 ExitCode = %d, want 1", report.Results[0].ExitCode)
	}
	if report.Results[0].Command != "go vet ./..." {
		t.Errorf("step 1 Command = %q, want %q", report.Results[0].Command, "go vet ./...")
	}
}

func TestGateGofmtOutputFails(t *testing.T) {
	runner := &fakeRunner{fn: func(context.Context, string, []string, tool.Options) (tool.Result, error) {
		// gofmt -l exits 0 but lists dirty files on stdout.
		return tool.Result{Stdout: "unformatted.go\n", ExitCode: 0}, nil
	}}
	g := NewGateRunner(runner, 60)
	profile := profile.Profile{Steps: []profile.GateStep{
		{Name: "format", Command: "gofmt", Args: []string{"-l", "."}},
	}}

	report, err := g.Run(context.Background(), t.TempDir(), profile)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Success {
		t.Errorf("report.Success = true, want false (gofmt listed dirty files)")
	}
	if report.Results[0].Success {
		t.Errorf("step Success = true, want false")
	}
	if !strings.Contains(report.Results[0].Output, "unformatted.go") {
		t.Errorf("Output = %q, want it to list unformatted.go", report.Results[0].Output)
	}
}

func TestGateRunCleanEnv(t *testing.T) {
	runner := &fakeRunner{fn: func(context.Context, string, []string, tool.Options) (tool.Result, error) {
		return tool.Result{ExitCode: 0}, nil
	}}
	g := NewGateRunner(runner, 60)
	profile := profile.Profile{Steps: []profile.GateStep{
		{Name: "test", Command: "go", Args: []string{"test", "./..."}},
	}}

	if _, err := g.Run(context.Background(), t.TempDir(), profile); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(runner.opts) != 1 {
		t.Fatalf("runner called %d times, want 1", len(runner.opts))
	}
	if !runner.opts[0].CleanEnv {
		t.Errorf("opts.CleanEnv = false, want true")
	}
	if runner.opts[0].Dir == "" {
		t.Errorf("opts.Dir = %q, want project dir", runner.opts[0].Dir)
	}
}

func TestGateStepTimeoutOverridesDefault(t *testing.T) {
	runner := &fakeRunner{fn: func(context.Context, string, []string, tool.Options) (tool.Result, error) {
		return tool.Result{ExitCode: 0}, nil
	}}
	g := NewGateRunner(runner, 60)
	profile := profile.Profile{Steps: []profile.GateStep{
		{Name: "lint", Command: "go", Args: []string{"vet", "./..."}, Timeout: 10},
	}}

	if _, err := g.Run(context.Background(), t.TempDir(), profile); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got := runner.opts[0].Timeout; got != 10*time.Second {
		t.Errorf("opts.Timeout = %v, want 10s", got)
	}
}

func TestGateProfileTimeout(t *testing.T) {
	p := profile.Profile{Steps: []profile.GateStep{
		{Name: "format"},             // no override -> defaultSeconds
		{Name: "test", Timeout: 300}, // explicit override
		{Name: "build", Timeout: 60}, // explicit override
	}}
	// default(120) + 300 + 60 = 480s, times margin 1.5 = 720s
	got := GateProfileTimeout(p, 120, 1.5)
	want := 720 * time.Second
	if got != want {
		t.Errorf("GateProfileTimeout() = %v, want %v", got, want)
	}
}

func TestGateProfileTimeoutEmptyProfile(t *testing.T) {
	if got := GateProfileTimeout(profile.Profile{}, 120, 1.5); got != 0 {
		t.Errorf("GateProfileTimeout(empty) = %v, want 0", got)
	}
}
