package domain

import (
	"errors"
	"strings"
	"time"
)

// ErrNoAgent reports that a launch was requested without an assigned agent.
// Callers surface it as "assign a model first" rather than spawning a pane
// that would run an empty command.
var ErrNoAgent = errors.New("no agent assigned")

// interactiveReadyDelay is the minimum wait after the agent command starts
// before the caller may start checking whether its TUI has actually drawn
// (see agentTerminal.waitForQuiet in the tui package): interactive agent
// TUIs (opencode --mini, claude) draw their input box asynchronously, and
// sampling activity too early can't tell "hasn't drawn yet" from "drew
// instantly and is already idle." This alone is not sufficient — a slow
// start (system load, a cold-cache agent, several other agent/tmux
// processes already competing for CPU) can still take longer, and keys
// sent before the box exists are swallowed by the terminal, silently
// dropping the whole prompt — observed live: opencode's own startup can
// render a first frame, go quiet for a beat while it loads, then redraw
// its real input box; on a loaded machine that beat can outlast a short
// quiet window, so waitForQuiet's heuristic falsely calls it "done
// drawing" mid-startup and types into a screen that isn't ready yet. Three
// seconds gives real startup more headroom before sampling even begins.
const interactiveReadyDelay = 3 * time.Second

// interactiveLaunchSpec holds one agent CLI's interactive-launch quirks as
// plain data — InteractiveCommand looks a spec up by agent name instead of
// branching on it in a switch, so a new CLI needing its own flag omissions
// or a longer ReadyDelay is a new map entry here, not a new case.
type interactiveLaunchSpec struct {
	// miniFlag, when set, is appended ahead of --model when the caller asks
	// for the narrow-pane TUI (InteractiveCommand's minimal parameter).
	miniFlag string
	// modelFlag/effortFlag are the flags this agent's interactive launch
	// accepts a model/effort on; "" means the launch has no equivalent (see
	// appendFlag — an empty flag or value is silently skipped).
	modelFlag, effortFlag string
	// readyDelay overrides interactiveReadyDelay for an agent whose TUI is
	// known to take longer to draw its first real, input-ready frame. Zero
	// means use the default.
	readyDelay time.Duration
}

var interactiveSpecs = map[string]interactiveLaunchSpec{
	"opencode": {
		// --mini is opencode's narrow-pane TUI flag. --variant (reasoning
		// effort) is a flag of the headless `opencode run` subcommand only —
		// passing it to the bare/--mini invocation makes opencode print its
		// usage and exit 1 instead of starting. The interactive surface has
		// no equivalent flag, so effortFlag is left unset rather than mapped
		// to something else.
		miniFlag:  "--mini",
		modelFlag: "--model",
	},
	"claude": {
		// claude's bare invocation is already the interactive TUI (-p is
		// what makes it headless), so no mode flag is needed (miniFlag left
		// unset).
		modelFlag:  "--model",
		effortFlag: "--effort",
		// Measured live (claude 2.1.224): the splash paints once around
		// t=1.5s, then goes fully silent for ~2.5s on a real network round
		// trip (auth/org lookup, the weekly-limit banner, the update check)
		// before a loading spinner starts repainting the whole screen every
		// ~300-350ms for several more seconds — the real input box isn't
		// ready until well past that. A 4s ReadyDelay sampled right in the
		// middle of that 2.5s pre-spinner silence, so waitForQuiet's first
		// check found the pty already "quiet" and typed into the still-
		// loading splash — the prompt was silently dropped, and the agent
		// sat at an empty input forever (see promptQuietWindow's doc comment
		// in internal/tui for the other half of this fix). 6s clears that
		// pre-spinner gap with margin.
		readyDelay: 6 * time.Second,
	},
}

// InteractiveLaunch describes how to start a model interactively in a tmux
// pane: the command to run, and how the prompt reaches the agent.
type InteractiveLaunch struct {
	// Argv is the command (argv[0] plus flags) to run in the pane.
	Argv []string
	// PromptKeys is the text to type into the agent with send-keys once its
	// TUI has drawn. Empty means Argv already carries the prompt.
	PromptKeys string
	// ReadyDelay is the minimum wait after starting Argv before the caller
	// starts polling for the agent's TUI to go quiet (see
	// agentTerminal.waitForQuiet) — not, by itself, a guarantee the TUI has
	// drawn.
	ReadyDelay time.Duration
}

// InteractiveCommand builds the interactive launch for agent (opencode,
// claude, …) with an optional model and reasoning effort.
//
// Unlike the headless invocations in internal/model (`opencode run <prompt>`,
// `claude -p <prompt>`), the prompt is never placed in argv here: an
// interactive TUI reads its positional argument as a project/file path, so an
// argv prompt would be misread as a path instead of being asked. The prompt is
// returned in PromptKeys and typed into the running TUI instead.
//
// Optional flags are omitted entirely when their value is empty — passing
// `--model ""` makes opencode exit with an error rather than fall back to its
// configured default.
//
// An unknown agent is not an error: BARON dispatches to whatever CLI the user
// has on PATH, so the agent name alone becomes argv and the prompt is typed
// the same way. Only a missing agent name is an error.
//
// When minimal is true, opencode is launched with --mini (the narrow-pane
// TUI without the sidebar). Prompt Mode passes false to get the full TUI
// in its wider left pane.
func InteractiveCommand(agent, model, effort, prompt string, minimal ...bool) (InteractiveLaunch, error) {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return InteractiveLaunch{}, ErrNoAgent
	}

	launch := InteractiveLaunch{
		Argv:       []string{agent},
		PromptKeys: prompt,
		ReadyDelay: interactiveReadyDelay,
	}

	if spec, ok := interactiveSpecs[agent]; ok {
		if spec.miniFlag != "" && (len(minimal) == 0 || minimal[0]) {
			launch.Argv = append(launch.Argv, spec.miniFlag)
		}
		launch.Argv = appendFlag(launch.Argv, spec.modelFlag, model)
		launch.Argv = appendFlag(launch.Argv, spec.effortFlag, effort)
		if spec.readyDelay > 0 {
			launch.ReadyDelay = spec.readyDelay
		}
	}

	return launch, nil
}

// appendFlag adds "flag value" only when both are non-empty, so an unset
// option (or an agent spec with no equivalent flag at all) stays off the
// command line instead of being passed as an empty string.
func appendFlag(argv []string, flag, value string) []string {
	if flag == "" || strings.TrimSpace(value) == "" {
		return argv
	}
	return append(argv, flag, value)
}
