package domain

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectContext returns .baron/prompt.md's content from dir (the project
// root) formatted for prepending to a prompt, or "" if the file doesn't
// exist or is empty — see agent-contract.md §3: the one way to hand every
// model and Crew Mode persona the same project rules/architecture notes
// without separate per-agent configuration. Callers prepend the result
// themselves (domain.Prompt stays pure/file-I/O-free; see its own callers
// in internal/cli/run.go, internal/tui/terminal.go, and
// internal/cli/persona_trigger.go's personaPrompt).
func ProjectContext(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".baron", "prompt.md"))
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}
	return content + "\n\n"
}
