// Package agent discovers the CLI coding agents installed on the machine
// (claude, codex, opencode, gemini, ...) and the models each one can run.
//
// The two are deliberately different things, and BARON keeps them apart:
//
//   - An Agent is a CLI binary on PATH. Which agents exist is a property of
//     the machine, not of any one project, so the probe result is cached
//     once per user in ~/.cache/baron/agents.json — never inside a
//     project's .baron/ directory.
//   - A Model is something you can actually assign a bead to: "claude/opus",
//     "opencode-go/deepseek-v4-flash". Every Model records which Agent runs
//     it and which reasoning-effort levels it accepts, so the TUI's assign
//     form is built straight from the cached catalog with no subprocess.
//     That catalog lives in ~/.cache/baron/models.json.
//
// Assignment is model-first: you pick a Model and the Agent that runs it
// falls out of the catalog entry (see Catalog.Find).
package agent

import "strings"

// Status represents the availability of an agent CLI.
type Status string

const (
	// StatusActive marks a CLI that responded to a version probe.
	StatusActive Status = "active"
	// StatusPassive marks a CLI found on PATH whose probe failed.
	StatusPassive Status = "passive"
	// StatusNotFound marks a known candidate CLI not found on PATH.
	// Only ProbeAll (doctor's display-only checklist) produces this status;
	// Discover, which feeds the usable-agent registry, never does.
	StatusNotFound Status = "not_found"
)

// Agent is a CLI coding agent discovered on PATH.
type Agent struct {
	Name    string   `json:"name"`    // e.g. "claude", "codex"
	Command string   `json:"command"` // the binary invoked, usually == Name
	Status  Status   `json:"status"`
	Version string   `json:"version,omitempty"`
	Tags    []string `json:"tags,omitempty"` // e.g. ["openai", "cli"]
	Backend string   `json:"backend"`        // "subprocess" (always for v1)
	// Args are the CLI's headless invocation flags, e.g.
	// ["-p", "{{prompt}}"] for `claude -p "<task>"`. "{{prompt}}" is
	// replaced with the bead's task text at launch. Empty means the CLI
	// isn't configured for headless launch yet.
	Args []string `json:"args,omitempty"`
	// ModelFlag is the flag this CLI takes a model name on ("--model" for
	// every agent BARON ships with). "" means the CLI has no model
	// selection and an assigned model is silently ignored.
	ModelFlag string `json:"model_flag,omitempty"`
	// EffortFlag is the flag this CLI takes a reasoning-effort level on:
	// "--effort" for claude, "--variant" for opencode. "" means the CLI
	// has no effort selection.
	EffortFlag string `json:"effort_flag,omitempty"`
}

// ConfigAgent is an agent override from config.toml's [[agents]] blocks —
// the documented no-code way to register a CLI BARON doesn't ship a
// candidate for. Active is a pointer so an omitted field leaves a
// discovered agent's status untouched, distinct from `active = false`.
type ConfigAgent struct {
	Name       string   `toml:"name" json:"name"`
	Command    string   `toml:"command" json:"command"`
	Args       []string `toml:"args" json:"args"`
	Tags       []string `toml:"tags" json:"tags"`
	ModelFlag  string   `toml:"model_flag" json:"model_flag"`
	EffortFlag string   `toml:"effort_flag" json:"effort_flag"`
	Active     *bool    `toml:"active" json:"active"`
}

// Invocation returns a's launch args with the model and effort flags
// applied — the "{{prompt}}" placeholder is left in place for the caller
// (domain.BuildCmd) to substitute. An empty model or effort, or an agent
// with no corresponding flag, contributes nothing.
//
// A flag the agent's default args already carry is replaced rather than
// repeated: claude's defaults ship "--effort high", so assigning
// --effort low must overwrite that, not append a second conflicting flag.
func (a Agent) Invocation(model, effort string) []string {
	args := append([]string{}, a.Args...)
	if model != "" && a.ModelFlag != "" {
		args = setFlag(args, a.ModelFlag, model)
	}
	if effort != "" && a.EffortFlag != "" {
		args = setFlag(args, a.EffortFlag, effort)
	}
	return args
}

// setFlag returns args with flag set to value: the existing occurrence is
// overwritten when there is one, otherwise the pair is inserted.
//
// Where it is inserted depends on how the CLI takes its prompt. When
// "{{prompt}}" is positional (opencode's `run {{prompt}}`, codex's bare
// `{{prompt}}`) the flag must go before it, since a positional argument
// ends flag parsing for some CLIs. When it is a flag's value (claude's
// `-p {{prompt}}`) there is no positional to get behind, so the pair is
// appended.
func setFlag(args []string, flag, value string) []string {
	for i, a := range args {
		if a != flag {
			continue
		}
		out := append([]string{}, args...)
		if i+1 < len(out) {
			out[i+1] = value
			return out
		}
		return append(out, value)
	}
	if i := positionalPromptIndex(args); i >= 0 {
		out := make([]string, 0, len(args)+2)
		out = append(out, args[:i]...)
		out = append(out, flag, value)
		return append(out, args[i:]...)
	}
	return append(args, flag, value)
}

// positionalPromptIndex returns the index of the "{{prompt}}" argument when
// it is positional, or -1 when the CLI has none or passes the prompt as a
// flag's value (the preceding argument is a flag).
func positionalPromptIndex(args []string) int {
	for i, a := range args {
		if !strings.Contains(a, "{{prompt}}") {
			continue
		}
		if i > 0 && strings.HasPrefix(args[i-1], "-") {
			return -1
		}
		return i
	}
	return -1
}

// Registry is the in-memory set of known agents, keyed by name. It is
// loaded from the machine-wide cache (LoadAgents) and then has the
// project's [[agents]] overrides merged on top — the merged result stays
// in memory and is never written back to the shared cache, so one
// project's override can't leak into another's agent list.
type Registry struct {
	agents map[string]Agent
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{agents: make(map[string]Agent)}
}

// Add adds or updates an agent.
func (r *Registry) Add(a Agent) {
	r.agents[a.Name] = a
}

// Get returns an agent by name.
func (r *Registry) Get(name string) (Agent, bool) {
	a, ok := r.agents[name]
	return a, ok
}

// List returns all agents, name-sorted.
func (r *Registry) List() []Agent {
	out := make([]Agent, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, a)
	}
	sortAgents(out)
	return out
}

// Active returns only agents whose CLI answered a version probe.
func (r *Registry) Active() []Agent {
	out := []Agent{}
	for _, a := range r.agents {
		if a.Status == StatusActive {
			out = append(out, a)
		}
	}
	sortAgents(out)
	return out
}

// activeStatus maps a config `active` bool to a Status.
func activeStatus(active bool) Status {
	if active {
		return StatusActive
	}
	return StatusPassive
}

// mergeExistingAgent applies ca's non-zero fields on top of a — config
// entries override discovered ones field by field, leaving anything ca
// doesn't set untouched.
func mergeExistingAgent(a Agent, ca ConfigAgent) Agent {
	if ca.Command != "" {
		a.Command = ca.Command
	}
	if ca.Tags != nil {
		a.Tags = ca.Tags
	}
	if ca.Args != nil {
		a.Args = ca.Args
	}
	if ca.ModelFlag != "" {
		a.ModelFlag = ca.ModelFlag
	}
	if ca.EffortFlag != "" {
		a.EffortFlag = ca.EffortFlag
	}
	if ca.Active != nil {
		a.Status = activeStatus(*ca.Active)
	}
	return a
}

// newAgentFromConfig builds a brand-new Agent for a config entry BARON has
// no discovered candidate for — registering it outright, active by default.
func newAgentFromConfig(ca ConfigAgent) Agent {
	command := ca.Command
	if command == "" {
		command = ca.Name
	}
	status := StatusActive
	if ca.Active != nil {
		status = activeStatus(*ca.Active)
	}
	return Agent{
		Name:       ca.Name,
		Command:    command,
		Status:     status,
		Tags:       ca.Tags,
		Backend:    "subprocess",
		Args:       ca.Args,
		ModelFlag:  ca.ModelFlag,
		EffortFlag: ca.EffortFlag,
	}
}

// MergeConfig merges config [[agents]] overrides on top of the registry.
// Config entries take precedence over discovered ones, and an entry naming
// an agent BARON has no candidate for registers it outright.
func (r *Registry) MergeConfig(configAgents []ConfigAgent) {
	for _, ca := range configAgents {
		if ca.Name == "" {
			continue
		}
		if a, ok := r.agents[ca.Name]; ok {
			r.agents[ca.Name] = mergeExistingAgent(a, ca)
			continue
		}
		r.agents[ca.Name] = newAgentFromConfig(ca)
	}
}
