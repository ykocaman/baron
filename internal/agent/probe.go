package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/tool"
)

// versionProbeTimeout bounds how long an agent's --version probe may run.
const versionProbeTimeout = 5 * time.Second

// ErrNotFound reports that a candidate CLI is not installed on PATH.
var ErrNotFound = errors.New("agent: command not found on PATH")

// candidate is a known CLI coding agent worth probing.
type candidate struct {
	name    string
	command string
	tags    []string
	// args are the default headless invocation; "{{prompt}}" is replaced at
	// launch. Thinking is enabled per CLI the way each tool actually
	// supports it headlessly: opencode --thinking (run subcommand), claude
	// --effort high. gemini has no per-invocation flag or env var — its
	// thinking budget is a settings.json-only option, so it keeps the plain
	// invocation and users opt in via .gemini/settings.json.
	args       []string
	modelFlag  string
	effortFlag string
	// models is a fixed model list for a CLI that can't be asked for its
	// own catalog, and efforts the levels each of those models accepts.
	// A CLI whose catalog is discovered live (opencode) leaves both empty —
	// see discoverModels.
	models  []string
	efforts []string
}

// claudeEfforts are Claude Code's own --effort values (`claude --help`:
// "low, medium, high, xhigh, max"), fixed regardless of which model is
// selected — unlike opencode's --variant, this is a CLI capability, not a
// per-model one.
var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// claudeModels are Claude Code's own --model shortcuts (`claude --help`:
// "an alias for the latest model, e.g. 'fable', 'opus', 'sonnet'", and
// 'haiku' the same way). Claude Code has no scriptable "list models"
// command the way opencode does, so this is a curated, necessarily
// incomplete list — a full model ID (e.g. "claude-haiku-4-5-20251001")
// still works via `work assign --agent claude --model <id>` even though it
// isn't in the catalog.
var claudeModels = []string{"haiku", "sonnet", "opus", "fable"}

const (
	promptPlaceholder = "{{prompt}}"
	modelFlag         = "--model"
)

// candidates lists the CLIs BARON can dispatch to.
var candidates = []candidate{
	{
		name: "claude", command: "claude", tags: []string{"anthropic", "cli"},
		args:      []string{"-p", promptPlaceholder, "--effort", "high"},
		modelFlag: modelFlag, effortFlag: "--effort",
		models: claudeModels, efforts: claudeEfforts,
	},
	{
		name: "codex", command: "codex", tags: []string{"openai", "cli"},
		args: []string{promptPlaceholder}, modelFlag: modelFlag,
	},
	{
		// opencode is the one agent that can list its own catalog, so its
		// models and their per-model --variant keys are discovered rather
		// than hardcoded — see discoverModels.
		name: "opencode", command: "opencode", tags: []string{"cli"},
		args:      []string{"run", promptPlaceholder, "--thinking"},
		modelFlag: modelFlag, effortFlag: "--variant",
	},
	{
		name: "gemini", command: "gemini", tags: []string{"google", "cli"},
		args: []string{"-p", promptPlaceholder}, modelFlag: modelFlag,
	},
	{
		name: "agy", command: "agy", tags: []string{"cli"},
		args: []string{"-p", promptPlaceholder}, modelFlag: modelFlag,
	},
	{
		name: "cline", command: "cline", tags: []string{"cli"},
		args: []string{promptPlaceholder}, modelFlag: modelFlag,
	},
}

// candidatesIndex resolves a probe name to its candidate definition.
var candidatesIndex = func() map[string]candidate {
	index := make(map[string]candidate, len(candidates))
	for _, c := range candidates {
		index[c.name] = c
	}
	return index
}()

// Probe discovers available CLI coding agents.
type Probe struct {
	runner tool.Runner
}

// NewProbe creates a probe.
func NewProbe(runner tool.Runner) *Probe {
	return &Probe{runner: runner}
}

// ProbeAll probes every known candidate and returns all results, name
// sorted, including ones not found on PATH. Refresh keeps the installed
// subset as the usable-agent registry and hands the full list to `baron
// doctor`, which shows it as a checklist (e.g. "✗ opencode — not found"
// rather than silently omitting it).
func (p *Probe) ProbeAll(ctx context.Context) []Agent {
	agents := make([]Agent, 0, len(candidates))
	for _, c := range candidates {
		a, err := p.ProbeSingle(ctx, c.name)
		if errors.Is(err, ErrNotFound) {
			a = c.agent()
			a.Status = StatusNotFound
		}
		agents = append(agents, a)
	}
	sortAgents(agents)
	return agents
}

// ProbeSingle probes a single CLI and returns its status. It returns
// ErrNotFound when the CLI is not installed. A name with no candidate
// definition is probed as a bare command of that name, which is what makes
// an agent registered only in config.toml ([[agents]]) resolvable.
func (p *Probe) ProbeSingle(ctx context.Context, name string) (Agent, error) {
	c, ok := candidatesIndex[name]
	if !ok {
		c = candidate{name: name, command: name}
	}
	if err := p.which(ctx, c.command); err != nil {
		return Agent{}, ErrNotFound
	}

	a := c.agent()
	res, err := p.runner.Run(ctx, c.command, []string{"--version"}, tool.Options{Timeout: versionProbeTimeout})
	if err == nil {
		a.Status = StatusActive
		a.Version = parseVersion(res.Stdout, c.command)
		if a.Version == "" {
			a.Version = parseVersion(res.Stderr, c.command)
		}
		return a, nil
	}
	// Found on PATH but the version probe failed (timeout or error).
	a.Status = StatusPassive
	return a, nil
}

// agent builds the Agent record a candidate describes, minus the probe
// result (Status/Version).
func (c candidate) agent() Agent {
	return Agent{
		Name:       c.name,
		Command:    c.command,
		Tags:       c.tags,
		Backend:    "subprocess",
		Args:       c.args,
		ModelFlag:  c.modelFlag,
		EffortFlag: c.effortFlag,
	}
}

// which reports whether the command is resolvable on PATH.
func (p *Probe) which(ctx context.Context, command string) error {
	_, err := p.runner.Run(ctx, "which", []string{command}, tool.Options{})
	return err
}

// parseVersion extracts the first line of version output, stripping any
// leading "<command>" or "version" label, e.g. "claude 1.0.0" -> "1.0.0".
func parseVersion(out, command string) string {
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	out = strings.TrimSpace(out)
	for _, label := range []string{command, "version"} {
		if len(out) == 0 {
			break
		}
		rest, ok := strings.CutPrefix(strings.ToLower(out), strings.ToLower(label))
		if ok && len(rest) < len(out) {
			out = strings.TrimSpace(out[len(out)-len(rest):])
		}
	}
	return out
}
