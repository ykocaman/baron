package domain

import (
	"context"
	"fmt"
	"strings"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/tool"
)

// Backend launches a model's official CLI as a subprocess in a bead's
// worktree, on the user's own account (BYOK/headless).
type Backend struct {
	runner tool.Runner
}

// NewBackend creates a Backend that launches models via runner.
func NewBackend(runner tool.Runner) *Backend {
	return &Backend{runner: runner}
}

// Launch runs m's headless launch command in dir, substituting prompt into
// every "{{prompt}}" placeholder in m.Args. Unlike the gate runner this does
// not use CleanEnv: the model needs the user's own credentials (BYOK). Args
// is user-configured per model ([[models]] in config.toml); a model with
// none configured can't be launched headlessly yet. onOutput, if set, is
// called on every byte the subprocess writes (SilentDeathMonitor uses it
// to track liveness); nil is fine for a plain launch.
//
// env optionally overrides the BEADS_DB isolation every task-agent gets by
// default (TaskAgentEnv(dir), applied when env is omitted entirely) — see
// LaunchInTmux's doc comment for the same convention and why Crew Mode
// persona launches need it.
func (b *Backend) Launch(ctx context.Context, m agent.Agent, dir, prompt string, onOutput func(), env ...map[string]string) (tool.Result, error) {
	name, args, err := BuildCmd(m, prompt)
	if err != nil {
		return tool.Result{}, err
	}
	launchEnv := TaskAgentEnv(dir)
	if len(env) > 0 {
		launchEnv = env[0]
	}
	return b.runner.Run(ctx, name, args, tool.Options{Dir: dir, OnOutput: onOutput, Env: launchEnv})
}

// BuildCmd resolves m's launch command and its args with prompt substituted
// into every "{{prompt}}" placeholder. Backend.Launch (direct subprocess)
// and LaunchInTmux (tmux window) share it so both paths run the same command.
func BuildCmd(m agent.Agent, prompt string) (string, []string, error) {
	if len(m.Args) == 0 {
		return "", nil, fmt.Errorf("model %q has no args configured; add args under [[models]] in config.toml", m.Name)
	}
	args := make([]string, len(m.Args))
	for i, a := range m.Args {
		args[i] = strings.ReplaceAll(a, "{{prompt}}", prompt)
	}
	return m.Command, args, nil
}

// Prompt builds the task text handed to a model for a bead: title,
// description and acceptance criteria are what a coding agent needs to
// start work. No longer appends any Hunk-steering instructions — every
// agent invocation used to carry a fixed "leave inline review notes via
// `hunk session comment add`" paragraph regardless of whether the bead
// actually needed it, permanently taxing every prompt's signal-to-noise
// for a feature most beads never touch. Hunk itself (the Diff tab, and
// pulling in comments a human leaves via HunkComments) is unaffected —
// only the agent-directed prompt instruction is gone.
func Prompt(title, description, acceptance string) string {
	var b strings.Builder
	b.WriteString(title)
	if description != "" {
		b.WriteString("\n\n")
		b.WriteString(description)
	}
	if acceptance != "" {
		b.WriteString("\n\nAcceptance criteria:\n")
		b.WriteString(acceptance)
	}
	return b.String()
}
