package cli

import (
	"context"
	"slices"
	"testing"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// runGateOnly replicates what the deleted `run --gate` cobra command used
// to do: resolve the fixed test bead "baron-a1b2c3", prepare its worktree,
// run the gate. Used by tests that only care about the gate step, not the
// full agent-launch pipeline.
func runGateOnly(t *testing.T, a *app) (domain.GateReport, error) {
	t.Helper()
	const brn = "baron-a1b2c3"
	ctx := context.Background()
	bead, err := a.findBead(ctx, domain.BRN(brn), brn)
	if err != nil {
		t.Fatalf("findBead: %v", err)
	}
	parentBranch, err := a.mergeBaseFor(ctx, bead)
	if err != nil {
		t.Fatalf("mergeBaseFor: %v", err)
	}
	wt, err := a.worktrees.Create(ctx, a.idOf(domain.BRN(brn)), bead.IssueType, parentBranch)
	if err != nil {
		t.Fatalf("worktrees.Create: %v", err)
	}
	return a.runGateReport(ctx, domain.BRN(brn), wt)
}

// gateRecordingRunner wraps runGateRunner, recording every invocation so a
// test can assert exactly which gate steps ran (name + args).
type gateRecordingRunner struct {
	*runGateRunner
	calls [][]string
}

func (r *gateRecordingRunner) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.runGateRunner.Run(ctx, name, args, opts)
}

// customProfileConfig returns a loadConfig func whose [profiles.custom]
// section is wired into the gate and selected as the project profile.
func customProfileConfig(t *testing.T) func(string) (*store.Config, error) {
	t.Helper()
	return func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.General.Profile = "custom"
		cfg.Profiles["custom"] = store.Profile{
			Formatter: "my-fmt",
			Linter:    "my-lint --strict",
			Typecheck: "",
			Test:      "my-test",
			Build:     "my-build",
		}
		return cfg, nil
	}
}

func TestRunGateUsesConfigDefinedProfile(t *testing.T) {
	r := &gateRecordingRunner{runGateRunner: &runGateRunner{}}
	a := newTestApp(t, r)
	a.loadConfig = customProfileConfig(t)

	report, err := runGateOnly(t, a)
	if err != nil {
		t.Fatalf("runGateReport: %v; stderr: %s", err, stderr(t, a))
	}
	if !report.Success {
		t.Errorf("report = %+v, want the gate to pass", report)
	}
	for _, want := range [][]string{
		{"my-fmt"},
		{"my-lint", "--strict"},
		{"my-test"},
		{"my-build"},
	} {
		if !slices.ContainsFunc(r.calls, func(c []string) bool { return slices.Equal(c, want) }) {
			t.Errorf("gate did not run %v; calls:\n%v", want, r.calls)
		}
	}
	if slices.ContainsFunc(r.calls, func(c []string) bool { return c[0] == "gofmt" }) {
		t.Errorf("built-in go profile steps leaked into a config-defined profile:\n%v", r.calls)
	}
}

func TestRunGateFallsBackToBuiltinProfile(t *testing.T) {
	r := &gateRecordingRunner{runGateRunner: &runGateRunner{}}
	a := newTestApp(t, r)

	if _, err := runGateOnly(t, a); err != nil {
		t.Fatalf("runGateReport: %v", err)
	}
	if !slices.ContainsFunc(r.calls, func(c []string) bool { return slices.Equal(c, []string{"gofmt", "-l", "."}) }) {
		t.Errorf("default run did not use the built-in go profile:\n%v", r.calls)
	}
}
