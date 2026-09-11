package domain

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestInteractiveCommandOpencodeUsesMiniAndKeepsPromptOutOfArgv(t *testing.T) {
	// effort ("high") is deliberately dropped for opencode: --variant is a
	// flag of the headless `opencode run` subcommand only — passing it to
	// the bare/--mini invocation makes opencode print its usage and exit 1
	// instead of starting, which is exactly the bug this pins down.
	got, err := InteractiveCommand("opencode", "anthropic/claude-sonnet-4-5", "high", "fix the parser")
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	want := []string{"opencode", "--mini", "--model", "anthropic/claude-sonnet-4-5"}
	if !slices.Equal(got.Argv, want) {
		t.Errorf("argv = %q, want %q", got.Argv, want)
	}
	if slices.Contains(got.Argv, "--variant") {
		t.Errorf("argv = %q, must not contain --variant (invalid on the bare/--mini invocation, only valid under `opencode run`)", got.Argv)
	}
	// The prompt must never be an argument: interactive opencode reads its
	// positional argument as a project/file path, so an argv prompt would be
	// treated as a bogus path instead of being asked.
	if slices.Contains(got.Argv, "fix the parser") {
		t.Errorf("argv contains the prompt: %q", got.Argv)
	}
	if got.PromptKeys != "fix the parser" {
		t.Errorf("PromptKeys = %q, want the prompt", got.PromptKeys)
	}
	if got.ReadyDelay <= 0 {
		t.Errorf("ReadyDelay = %v, want a positive wait for the TUI to draw", got.ReadyDelay)
	}
}

func TestInteractiveCommandOpencodeOmitsFlagsWhenModelAndEffortAreEmpty(t *testing.T) {
	// `--model ""` makes opencode exit with an error, so an unset model must
	// leave the flag off entirely and let opencode use its own default.
	got, err := InteractiveCommand("opencode", "", "", "do the thing")
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	want := []string{"opencode", "--mini"}
	if !slices.Equal(got.Argv, want) {
		t.Errorf("argv = %q, want %q", got.Argv, want)
	}
}

func TestInteractiveCommandOpencodeOmitsOnlyTheUnsetFlag(t *testing.T) {
	got, err := InteractiveCommand("opencode", "openai/gpt-5", "", "prompt")
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	want := []string{"opencode", "--mini", "--model", "openai/gpt-5"}
	if !slices.Equal(got.Argv, want) {
		t.Errorf("argv = %q, want %q", got.Argv, want)
	}
}

func TestInteractiveCommandClaudeRunsBareTUIWithEffortFlag(t *testing.T) {
	// Bare `claude` is already the interactive TUI — -p is what makes it
	// headless — so no mode flag belongs here.
	got, err := InteractiveCommand("claude", "opus", "high", "review this diff")
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	want := []string{"claude", "--model", "opus", "--effort", "high"}
	if !slices.Equal(got.Argv, want) {
		t.Errorf("argv = %q, want %q", got.Argv, want)
	}
	if got.PromptKeys != "review this diff" {
		t.Errorf("PromptKeys = %q, want the prompt", got.PromptKeys)
	}
}

func TestInteractiveCommandClaudeOmitsFlagsWhenUnset(t *testing.T) {
	got, err := InteractiveCommand("claude", "", "", "prompt")
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	want := []string{"claude"}
	if !slices.Equal(got.Argv, want) {
		t.Errorf("argv = %q, want %q", got.Argv, want)
	}
}

func TestInteractiveCommandUnknownAgentRunsBareCommandInsteadOfFailing(t *testing.T) {
	// BARON dispatches to whatever CLI the user has on PATH; an unrecognised
	// name must still launch rather than block the run.
	got, err := InteractiveCommand("codex", "gpt-5", "high", "ship it")
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	want := []string{"codex"}
	if !slices.Equal(got.Argv, want) {
		t.Errorf("argv = %q, want %q", got.Argv, want)
	}
	if got.PromptKeys != "ship it" {
		t.Errorf("PromptKeys = %q, want the prompt", got.PromptKeys)
	}
	if got.ReadyDelay <= 0 {
		t.Errorf("ReadyDelay = %v, want a positive wait", got.ReadyDelay)
	}
}

func TestInteractiveCommandEmptyAgentIsAnError(t *testing.T) {
	// An unassigned bead must not spawn a pane running an empty command.
	for _, agent := range []string{"", "   "} {
		got, err := InteractiveCommand(agent, "opus", "high", "prompt")
		if !errors.Is(err, ErrNoAgent) {
			t.Errorf("InteractiveCommand(%q) error = %v, want ErrNoAgent", agent, err)
		}
		if got.Argv != nil {
			t.Errorf("InteractiveCommand(%q) argv = %q, want none", agent, got.Argv)
		}
	}
}

func TestInteractiveCommandReadyDelayLeavesTimeForTheTUIToDraw(t *testing.T) {
	// Keys sent before the input box exists are swallowed, so the delay is
	// load-bearing, not cosmetic.
	got, err := InteractiveCommand("opencode", "", "", "prompt")
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	if got.ReadyDelay < time.Second {
		t.Errorf("ReadyDelay = %v, want at least a second", got.ReadyDelay)
	}
}

func TestInteractiveCommandOpencodeOmitsMiniWhenMinimalFalse(t *testing.T) {
	got, err := InteractiveCommand("opencode", "anthropic/claude-sonnet-4-5", "high", "fix the parser", false)
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	want := []string{"opencode", "--model", "anthropic/claude-sonnet-4-5"}
	if !slices.Equal(got.Argv, want) {
		t.Errorf("argv = %q, want %q", got.Argv, want)
	}
	if slices.Contains(got.Argv, "--mini") {
		t.Errorf("argv = %q, must not contain --mini when minimal=false", got.Argv)
	}
}

func TestInteractiveCommandOpencodeIncludesMiniWhenMinimalTrue(t *testing.T) {
	got, err := InteractiveCommand("opencode", "", "", "prompt", true)
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	want := []string{"opencode", "--mini"}
	if !slices.Equal(got.Argv, want) {
		t.Errorf("argv = %q, want %q", got.Argv, want)
	}
}

func TestInteractiveCommandOpencodeDefaultIncludesMini(t *testing.T) {
	got, err := InteractiveCommand("opencode", "", "", "prompt")
	if err != nil {
		t.Fatalf("InteractiveCommand: %v", err)
	}
	want := []string{"opencode", "--mini"}
	if !slices.Equal(got.Argv, want) {
		t.Errorf("argv = %q, want %q (default should include --mini)", got.Argv, want)
	}
}
