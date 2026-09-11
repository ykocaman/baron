package cli

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/tmux"
	"github.com/baron-cli/baron/internal/tool"
)

// TestAssignableModelsComesFromTheCache: the picker's list is a read of
// the machine-wide catalog, not a live probe. This is the whole point of
// caching it — opening the picker used to spawn `opencode models` every
// time, which is what made pressing 'a' lag.
func TestAssignableModelsComesFromTheCache(t *testing.T) {
	spawned := 0
	runner := &fakeRunner{run: func(string, []string) (tool.Result, error) {
		spawned++
		return tool.Result{}, nil
	}}
	a := newTestApp(t, runner)
	writeCatalog(
		t,
		agent.Model{ID: "claude/opus", Agent: "claude", Name: "opus", Efforts: claudeEffortLevels()},
		agent.Model{ID: "opencode-go/deepseek-v4-flash", Agent: "opencode", Name: "opencode-go/deepseek-v4-flash", Efforts: []string{"high", "low", "max"}},
		agent.Model{ID: "gemini", Agent: "gemini"},
	)

	got, err := a.assignableModels(context.Background())
	if err != nil {
		t.Fatalf("assignableModels() error: %v", err)
	}
	if spawned != 0 {
		t.Errorf("assignableModels spawned %d subprocesses, want 0 (cache read only)", spawned)
	}
	for _, want := range []string{"claude/opus", "opencode-go/deepseek-v4-flash", "gemini"} {
		if !slices.Contains(got, want) {
			t.Errorf("assignableModels() = %v, want it to contain %q", got, want)
		}
	}
}

// TestAssignableModelsRefreshesStaleCatalog: an empty or expired catalog is
// rebuilt once, so a freshly installed agent shows up without the user
// having to know to run `baron doctor` first.
func TestAssignableModelsRefreshesStaleCatalog(t *testing.T) {
	runner := &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		switch {
		case name == "which" && len(args) > 0 && args[0] == "claude":
			return tool.Result{Stdout: "/usr/local/bin/claude\n"}, nil
		case name == "claude" && len(args) > 0 && args[0] == "--version":
			return tool.Result{Stdout: "claude 1.0.0\n"}, nil
		}
		return tool.Result{}, errors.New("not found")
	}}
	a := newTestApp(t, runner)
	// No catalog written at all: the stalest possible state.
	got, err := a.assignableModels(context.Background())
	if err != nil {
		t.Fatalf("assignableModels() error: %v", err)
	}
	if !slices.Contains(got, "claude/opus") {
		t.Errorf("assignableModels() = %v, want a refresh to discover claude's models", got)
	}
	if len(agent.LoadCatalog().Models) == 0 {
		t.Error("catalog still empty after a refresh, want the result cached for the next open")
	}
}

// TestEffortChoicesFor: the effort levels come off the picked catalog entry
// itself, so no subprocess runs and no prefix parsing is involved — an
// opencode ID like "opencode-go/deepseek-v4-flash" names a provider, not
// the agent, so a prefix split could never have answered this in general.
func TestEffortChoicesFor(t *testing.T) {
	spawned := 0
	runner := &fakeRunner{run: func(string, []string) (tool.Result, error) {
		spawned++
		return tool.Result{}, nil
	}}
	a := newTestApp(t, runner)
	writeCatalog(
		t,
		agent.Model{ID: "claude/haiku", Agent: "claude", Name: "haiku", Efforts: claudeEffortLevels()},
		agent.Model{ID: "gemini", Agent: "gemini"},
		agent.Model{ID: "opencode-go/deepseek-v4-flash", Agent: "opencode", Name: "opencode-go/deepseek-v4-flash", Efforts: []string{"high", "low", "max"}},
		agent.Model{ID: "opencode/big-pickle", Agent: "opencode", Name: "opencode/big-pickle"},
	)

	tests := []struct {
		picked string
		want   []string
	}{
		{"claude/haiku", claudeEffortLevels()},
		{"gemini", nil}, // no selectable effort
		{"opencode-go/deepseek-v4-flash", []string{"high", "low", "max"}},
		{"opencode/big-pickle", nil}, // opencode model with no variants
		{"not-in-the-catalog", nil},
	}
	for _, tt := range tests {
		t.Run(tt.picked, func(t *testing.T) {
			got, err := a.effortChoicesFor(context.Background(), tt.picked)
			if err != nil {
				t.Fatalf("effortChoicesFor(%q): %v", tt.picked, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("effortChoicesFor(%q) = %v, want %v", tt.picked, got, tt.want)
			}
		})
	}
	if spawned != 0 {
		t.Errorf("effortChoicesFor spawned %d subprocesses, want 0 (cache read only)", spawned)
	}
}

// claudeEffortLevels is claude's fixed --effort scale, as the catalog
// records it.
func claudeEffortLevels() []string {
	return []string{"low", "medium", "high", "xhigh", "max"}
}

// TestAgentHostAliveNeedsARunningAgent is the regression test for beads
// stuck in "working": the pane wrapper keeps the tmux window alive after the
// agent exits (`exec $SHELL`), so window existence read as liveness kept the
// TUI reattaching to a dead shell instead of resolving the bead.
func TestAgentHostAliveNeedsARunningAgent(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want bool
	}{
		{"agent still running", "thinking…\nwriting greet.go\n", true},
		{"agent exited", "done\nbaron-run-exit=0\n~/project %\n", false},
		{"agent failed", "Error: Aborted\nbaron-run-exit=1\n~/project %\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
				if name == "tmux" && len(args) > 0 && args[0] == "capture-pane" {
					return tool.Result{Stdout: tc.pane}, nil
				}
				return tool.Result{}, nil // has-session succeeds: the window exists
			}}
			host := &tmuxAgentHost{tmx: tmux.New(runner)}
			got, err := host.Alive(context.Background(), "baron-a1b2c3")
			if err != nil {
				t.Fatalf("Alive error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Alive() = %v, want %v (pane: %q)", got, tc.want, tc.pane)
			}
		})
	}
}

// TestAgentHostAliveMissingWindow: no window at all is unambiguously dead.
func TestAgentHostAliveMissingWindow(t *testing.T) {
	runner := &fakeRunner{run: func(string, []string) (tool.Result, error) {
		return tool.Result{ExitCode: 1, Stderr: "can't find window: baron-a1b2c3"}, errors.New("exit status 1")
	}}
	host := &tmuxAgentHost{tmx: tmux.New(runner)}
	alive, err := host.Alive(context.Background(), "baron-a1b2c3")
	if err != nil {
		t.Fatalf("Alive error: %v", err)
	}
	if alive {
		t.Error("Alive() = true for a window that does not exist")
	}
}
