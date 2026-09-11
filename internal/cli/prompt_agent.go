package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/tmux"
)

// PromptAgentIDs returns the machine's active agent CLI names — Prompt
// Mode's left-pane tabs (docs/PRD/crew-mode.md's v7 redesign). Isolated the
// same way Personas/Dispatch are (see runTUI's Deps wiring): loadAgents can
// hit its own cache-miss warning, which must not write to real stderr under
// the TUI's alt-screen.
func (a *app) PromptAgentIDs() ([]string, error) {
	sub := a.isolatedApp()
	sub.loadAgents()
	active := sub.agents.Active()
	ids := make([]string, len(active))
	for i, ag := range active {
		ids[i] = ag.Name
	}
	// ponytail: reuse existing Rank (recent 5 + popular 3 + rest), no new storage
	cat := agent.LoadCatalog()
	recent, popular, rest := cat.Rank(ids)
	out := append(append(recent, popular...), rest...)
	return out, nil
}

// PromptAgentEnsure starts (or, if already running, reattaches to)
// agentID's interactive chat session for Prompt Mode's left pane — the main
// repo checkout, at whatever branch is currently checked out (never a bead
// worktree), tool access scoped to bd only (promptAgentPermissionExtras),
// and deliberately NOT task-agent BEADS_DB-isolated: this pane exists so a
// human can edit real beads/personas through it.
//
// Unlike launchPersona's always-fresh fire-and-forget runs, a chat session
// a human is actively typing into must persist across repeated 't' presses
// rather than being killed and recreated — personaWindowBusy (reused
// as-is: it only cares about a tmux window name, not what launched it)
// tells the two cases apart: busy means "still running, reattach"
// (started=false), not-busy means "start fresh" (started=true).
func (a *app) PromptAgentEnsure(ctx context.Context, agentID string) (window string, started bool, err error) {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return "", false, err
	}
	if !tmux.Usable(ctx, a.runner, cfg.TUI.Tmux) {
		return "", false, fmt.Errorf("agent chat requires tmux (tui.tmux=%q or tmux not installed)", cfg.TUI.Tmux)
	}
	a.loadAgents()
	ag, ok := a.agents.Get(agentID)
	if !ok || ag.Status != agent.StatusActive {
		return "", false, fmt.Errorf("agent %q is not active", agentID)
	}

	launch, err := domain.InteractiveCommand(agentID, "", "", "", false)
	if err != nil {
		return "", false, err
	}
	permArgs, permEnv, err := promptAgentPermissionExtras(agentID)
	if err != nil {
		return "", false, fmt.Errorf("agent chat %s: scoping permissions: %w", agentID, err)
	}
	argv := append(append([]string{}, launch.Argv...), permArgs...)
	script := domain.LaunchScript(argv[0], argv[1:], permEnv) // no TaskAgentEnv: real bd access, by design

	window = "prompt-" + agentID
	session := cfg.TUI.TmuxSession
	if session == "" {
		session = domain.DefaultTmuxSession
	}
	tmx := tmux.New(a.runner)
	tmx.Session = session
	if err := tmx.Ensure(ctx); err != nil {
		return "", false, err
	}

	busy, err := personaWindowBusy(ctx, tmx, window)
	if err != nil {
		return "", false, err
	}
	if busy {
		return window, false, nil
	}
	if err := tmx.Spawn(ctx, window, "sh", a.dir, "-c", script); err != nil {
		return "", false, err
	}
	_ = agent.LoadCatalog().Record(agentID).Save()
	return window, true, nil
}

// promptAgentPermissionExtras scopes agentName's tool access to the bd CLI
// only — the whole surface, not personaPermissionExtras' Authority-derived
// subset, since this is a human-driven session, not an unattended one.
// Deliberately a small, standalone duplicate of personaPermissionExtras'
// per-backend mechanics (not a shared refactor): personaPermissionExtras is
// tested, working code backing the persona trigger engine, which this
// change leaves untouched by design (docs/PRD/crew-mode.md v7 — only
// Prompt Mode's TUI screen is in scope). See personaPermissionExtras' own
// doc comment for the verification story (claude verified against the real
// binary; opencode documented but not independently re-verified here).
func promptAgentPermissionExtras(agentName string) (args []string, env map[string]string, err error) {
	switch agentName {
	case "claude":
		return []string{"--allowedTools", "Bash(bd:*)"}, nil, nil
	case "opencode":
		return promptOpencodePermissionExtras(agentName)
	}
	return nil, nil, nil
}

// promptOpencodePermissionExtras writes a scoped opencode global config (a
// permission.bash allowlist for "bd *" only) to a per-session directory
// under os.TempDir(), pointed at via XDG_CONFIG_HOME — same isolation
// opencodePermissionExtras uses for personas (never the project-local
// opencode.json, never the user's real global config; XDG_DATA_HOME, where
// opencode's own auth.json lives, is untouched so real provider credentials
// still apply). Regenerated on every ensure call, so it never goes stale.
func promptOpencodePermissionExtras(agentName string) ([]string, map[string]string, error) {
	cfg := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"permission": map[string]any{"bash": map[string]string{"*": "deny", "bd *": "allow"}},
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	xdgConfigHome := filepath.Join(os.TempDir(), "baron-prompt-permissions", agentName)
	configDir := filepath.Join(xdgConfigHome, "opencode")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), body, 0o600); err != nil {
		return nil, nil, err
	}
	return nil, map[string]string{"XDG_CONFIG_HOME": xdgConfigHome}, nil
}

// CreatePersonaByFields creates a brand-new persona — Prompt Mode's
// Personas-tab 'n', the only persona-create path in the app (install.go's
// Install/Update are a separate, not-yet-wired-to-any-UI-action library
// path for installing from a local file). f.ID is the form's own optional
// ID field ("leave blank to auto-generate"): blank derives the id from
// f.Name, non-blank runs the user's own typed text through persona.Slug the
// same way — same filesystem/tmux-safety sanitizing and collision-suffixing
// either way, so a human can pin a specific id without being able to
// produce an unsafe or colliding one. Authority defaults conservatively
// ({BDWrite:true, Actions:["comment"]}, matching the built-in non-Developer
// personas' shape) since a human just typed this persona into existence
// with no review step the way an install source might get; Model/Source
// are left to their zero values / persona.SourceUser.
func (a *app) CreatePersonaByFields(f persona.FormFields) (string, error) {
	existing, err := a.loadPersonas()
	if err != nil {
		return "", err
	}
	id := f.ID
	if id == "" {
		id = f.Name
	}
	id = persona.Slug(id, existing)
	on, err := persona.ParseTransitionRules(f.Events)
	if err != nil {
		return "", err
	}
	p := persona.Persona{
		ID:          id,
		Name:        f.Name,
		Description: f.Description,
		Prompt:      f.Prompt,
		Trigger: persona.Trigger{
			Schedule: f.Schedule,
			On:       on,
		},
		Skills:    persona.ParseSkills(f.Skills),
		Model:     persona.Model{Tier: f.ModelTier, Agent: f.ModelAgent},
		Authority: persona.Authority{BDWrite: f.BDWrite, Actions: f.BDActions},
		Enabled:   f.Enabled,
		Source:    persona.SourceUser,
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	if err := a.savePersona(p); err != nil {
		return "", err
	}
	return id, nil
}

// DeletePersonaByID removes a user-layer persona (~/.config/baron/personas)
// entirely — Prompt Mode's Personas-tab delete key. Only a persona that
// actually has a file there can be deleted this way: a repo/embedded
// built-in has nothing on that layer to remove (its definition is compiled
// into the binary, or committed in the project's own personas/ directory —
// see persona.LoadAll's own doc comment) and can only be disabled, never
// deleted, from the TUI. Deleting a forked (content-edited) copy of a
// built-in reverts it back to that built-in's own repo/embedded content on
// the next load — not a way to "delete" the built-in itself, just to
// discard the local edit.
func (a *app) DeletePersonaByID(id string) error {
	userDir, err := persona.Dir()
	if err != nil {
		return err
	}
	forked, err := personaHasUserFile(userDir, id)
	if err != nil {
		return err
	}
	if !forked {
		return fmt.Errorf("persona %q is built in — disable it instead of deleting (there is no local copy to remove)", id)
	}
	return persona.Delete(userDir, id)
}
