package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/baron-cli/baron/internal/persona"
)

// bdCommandPrefixes returns the bd subcommand prefixes a persona with
// authority may run — bd show/bd comments always (personaPrompt tells every
// persona to read the bead and its comments before acting, on every
// subject shape), the write side gated by Authority.Actions. Shared
// vocabulary behind every personaPermissionExtras backend: same contract,
// different CLI syntax to express it in.
//
// reopen/close, like comment/create, are the persona's own agent process
// calling the real bd CLI directly — not routed through BARON's own
// domain.BeadState state machine (transitionStatus and its allowedTransitions
// map, actor-scoped rules included, simply never run for this path). That
// matches every other Authority.Action here: Authority is prompt-and-audit-
// enforced, not technically sandboxed to the bead a persona fired on (see
// personaPermissionExtras' own doc comment) — a persona could reopen/close/
// comment/create against any bead ID, same trust model qa-chromium's
// declared "reopen" already implied before this switch had a case for it.
func bdCommandPrefixes(authority persona.Authority) []string {
	prefixes := []string{"bd show", "bd comments"}
	for _, act := range authority.Actions {
		switch act {
		case "comment":
			prefixes = append(prefixes, "bd comment")
		case "create":
			prefixes = append(prefixes, "bd create")
		case "reopen":
			prefixes = append(prefixes, "bd reopen")
		case "close":
			prefixes = append(prefixes, "bd close")
		}
	}
	return prefixes
}

// personaPermissionExtras returns extra CLI args and/or environment
// variables that let a persona's agent actually run headlessly instead of
// blocking forever on a tool-approval prompt nothing can answer in a
// detached tmux window. Caught for real: a Developer dispatch resolved to
// claude/haiku, Claude Code's own Bash-tool permission gate refused `bd
// show`/`bd comments` (its first, mandatory reads), and since there is no
// TTY to approve it, the process printed "I need your approval..." and
// exited 0 having done nothing — Dispatch had already reported success, so
// nothing anywhere surfaced that the run was actually a no-op.
//
// Deliberately not a blanket bypass (--dangerously-skip-permissions/--auto
// would let the agent run anything). Authority is prompt-and-audit-enforced
// today, not technically enforced (see Authority's doc comment) — this
// makes it a real wall instead: a persona whose Actions is just ["comment"]
// literally cannot invoke anything else through the agent's own Bash tool,
// prompt-injection included.
//
// Per-backend confidence differs:
//   - claude: --allowedTools "Bash(bd show:*)" ... — verified against the
//     real installed binary (a scratch dir, --allowedTools
//     "Bash(echo:*)", confirmed it ran without an approval prompt).
//   - opencode: see opencodePermissionExtras — real documented schema, but
//     not a closed verification loop the way claude's is (the live test
//     that would have proven the bash pattern syntax hit the account's own
//     monthly quota mid-run). Watch the first real dispatch via Prompt
//     Mode's 'z' (now live-refreshing) to confirm it actually completes
//     instead of hanging on "ask".
//   - codex/gemini/cline/agy: no equivalent narrow mechanism found. agy in
//     particular only exposes --dangerously-skip-permissions (blanket) and
//     --mode accept-edits (file-edit-broad) — nothing bd-scoped in its
//     --help output — so it's left alone rather than either guessed at or
//     blanket-bypassed.
func personaPermissionExtras(agentName, personaID string, authority persona.Authority) (args []string, env map[string]string, err error) {
	switch agentName {
	case "claude":
		// claude CLI uses --permission-mode dontAsk for unattended execution
		return []string{"--permission-mode", "dontAsk"}, nil, nil
	case "agy":
		// agy CLI auto-approves tool permission requests in unattended mode
		return []string{"--dangerously-skip-permissions"}, nil, nil
	case "opencode":
		return opencodePermissionExtras(personaID, authority)
	}
	return nil, nil, nil
}

// opencodePermissionExtras writes a scoped opencode global config (just a
// permission.bash allowlist — none of the user's own plugins/agents/MCP
// servers, deliberately minimal since this is a machine-generated config
// for one unattended persona run, not a workspace to develop from) to a
// per-persona directory under os.TempDir(), and points opencode at it via
// XDG_CONFIG_HOME. Never the project-local opencode.json (that would scope
// to every opencode invocation in a.dir, including real task-agent bead
// runs that need much broader access to actually write code) and never the
// user's real global config (would mean merging into a file with plugins
// and custom agents this code has no business rewriting). XDG_CONFIG_HOME
// alone is auth-safe: opencode's auth.json lives under XDG_DATA_HOME
// (confirmed on disk — ~/.local/share/opencode/auth.json), a separate
// override this never touches, so a persona launch keeps the user's real
// provider credentials. Regenerated on every launch so an Authority.Actions
// edit takes effect on the next fire without a stale leftover allowlist.
func opencodePermissionExtras(personaID string, authority persona.Authority) ([]string, map[string]string, error) {
	rules := map[string]string{"*": "deny"}
	for _, prefix := range bdCommandPrefixes(authority) {
		rules[prefix+" *"] = "allow"
	}
	cfg := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"permission": map[string]any{"bash": rules},
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	xdgConfigHome := filepath.Join(os.TempDir(), "baron-persona-permissions", personaID)
	configDir := filepath.Join(xdgConfigHome, "opencode")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), body, 0o600); err != nil {
		return nil, nil, err
	}
	return nil, map[string]string{"XDG_CONFIG_HOME": xdgConfigHome}, nil
}

// personaSkillExtras returns the --plugin-dir/--plugin-url args that expose
// p.Skills to a persona's claude session — claude's own scoped,
// session-only plugin loader ("Load a plugin from a directory or .zip for
// this session only", confirmed via `claude --help`; Skills ship inside a
// plugin, so this is how a persona gets one without ever touching the real
// project's .claude/skills, which every session — including a human's own —
// would otherwise pick up). Only claude exposes this; every other agent
// gets no args and no error, same as personaPermissionExtras' unhandled
// agents.
//
// Each entry in skills is resolved by this priority:
//  1. A plugin zip URL (http:// or https://) → --plugin-url, no fetch needed.
//  2. A catalog-known skill name (persona.KnownSkill) → installCatalogSkill
//     (openskills install anthropics/skills --skill <name>) → --plugin-dir.
//  3. Anything else → treated as an npm package spec ("pkg", "pkg@1.2.3",
//     "@scope/pkg@1.0.0") → installSkillPackage (npm install) → --plugin-dir.
//
// The catalog branch exists because the official anthropics/skills monorepo
// (github.com/anthropics/skills) is not itself npm-published; the closest
// bridge confirmed during this feature's planning is the community openskills
// package (npx openskills install anthropics/skills --skill <name>). Caveat:
// this resolution path is verified against openskills' own published
// documentation only, not by a live end-to-end dispatch — the same
// "not a closed verification loop" caveat opencodePermissionExtras already
// carries for its permission model; real confirmation needs a live run once
// built.
func personaSkillExtras(agentName, personaID string, skills []string) ([]string, error) {
	if agentName != "claude" || len(skills) == 0 {
		return nil, nil
	}
	args := make([]string, 0, len(skills)*2)
	for _, ref := range skills {
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
			args = append(args, "--plugin-url", ref)
			continue
		}
		if persona.KnownSkill(ref) {
			dir, err := installCatalogSkill(personaID, ref)
			if err != nil {
				return nil, fmt.Errorf("skill %q (catalog): %w", ref, err)
			}
			args = append(args, "--plugin-dir", dir)
			continue
		}
		dir, err := installSkillPackage(personaID, ref)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", ref, err)
		}
		args = append(args, "--plugin-dir", dir)
	}
	return args, nil
}

// runNpmInstall fetches spec into dir via `npm install`, reporting the path
// npm actually installed it to (read straight from npm's own --json output,
// not reconstructed from spec — a scoped ("@org/pkg@1.0") or unscoped
// package's on-disk folder name isn't a simple suffix of its spec, so
// asking npm avoids re-inventing that parsing). --ignore-scripts skips the
// package's own install/postinstall hooks: fetching a skill's files is not
// the same risk as running its code, and this package has no reason to run
// arbitrary code from an npm registry (same reasoning persona.Install's own
// doc comment gives for not being a URL fetcher itself — this is the
// "network-fetch front end" it deferred, taking on that risk narrowly and
// deliberately here). A var so tests can stub it out instead of hitting the
// real npm registry, same pattern openRouterFetch (internal/agent/tiers.go)
// uses for its own live network call.
var runNpmInstall = func(dir, spec string) (installedPath string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	cmd := exec.Command("npm", "install", spec, "--prefix", dir, "--no-save", "--ignore-scripts", "--json")
	out, runErr := cmd.Output()
	var result struct {
		Add []struct {
			Path string `json:"path"`
		} `json:"add"`
		Error struct {
			Summary string `json:"summary"`
		} `json:"error"`
	}
	// npm still writes a JSON body on failure (an "error" object) — parse it
	// first so a real registry error (package not found, network down)
	// surfaces as that message instead of a bare "exit status 1".
	if jsonErr := json.Unmarshal(out, &result); jsonErr == nil && result.Error.Summary != "" {
		return "", fmt.Errorf("npm install %s: %s", spec, result.Error.Summary)
	}
	if runErr != nil {
		return "", fmt.Errorf("npm install %s: %w", spec, runErr)
	}
	if len(result.Add) == 0 {
		return "", fmt.Errorf("npm install %s: no package reported installed", spec)
	}
	return result.Add[0].Path, nil
}

// runOpenskillsInstall fetches a catalog skill into dir via
// `npx openskills install anthropics/skills --skill <name>`, reporting the
// installed path. A var so tests can stub it without hitting the real
// openskills registry, same pattern runNpmInstall uses.
var runOpenskillsInstall = func(dir, skillName string) (installedPath string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	cmd := exec.Command("npx", "openskills", "install", "anthropics/skills", "--skill", skillName, "--out-dir", dir)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return "", fmt.Errorf("openskills install %s: %w: %s", skillName, runErr, strings.TrimSpace(string(out)))
	}
	// openskills writes the skill into a subdirectory named after the skill.
	installed := filepath.Join(dir, skillName)
	if _, statErr := os.Stat(installed); statErr != nil {
		return "", fmt.Errorf("openskills install %s: expected output at %s: %w", skillName, installed, statErr)
	}
	return installed, nil
}

// installSkillPackage materializes an npm-package skill reference under a
// per-persona scratch directory (os.TempDir(), same convention
// opencodePermissionExtras uses for its own generated config — never the
// real project checkout), returning the installed package's own directory
// for personaSkillExtras to hand claude via --plugin-dir. Re-installs on
// every launch (no version pinning beyond whatever spec itself carries),
// matching opencodePermissionExtras' "regenerated every fire, never a stale
// leftover" posture.
func installSkillPackage(personaID, spec string) (string, error) {
	dir := filepath.Join(os.TempDir(), "baron-persona-skills", personaID)
	return runNpmInstall(dir, spec)
}

// installCatalogSkill materializes a catalog-known skill (persona.KnownSkill)
// under a per-persona scratch directory, using openskills as the fetch
// mechanism — see runOpenskillsInstall's doc comment and personaSkillExtras'
// catalog-branch caveat for the verification story.
func installCatalogSkill(personaID, skillName string) (string, error) {
	dir := filepath.Join(os.TempDir(), "baron-persona-skills", personaID, "catalog")
	return runOpenskillsInstall(dir, skillName)
}
