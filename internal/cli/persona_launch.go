package cli

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tmux"
)

type personaSubject struct {
	brn        string
	branch     string
	bead       *store.Bead
	candidates []store.Bead
	reason     string
}

// launchPersona runs p in the main repo checkout (a.dir — never a
// worktree, Crew personas are trusted with real bd access by design, see
// crew-mode.md §4), fire-and-forget in a tmux window named "persona-<id>"
// (the NAME is reused across triggers, never left to pile up one window
// per fire — but the window's CONTENTS are always fresh: see
// personaWindowBusy below). Requires tmux — there is no direct-subprocess
// fallback here the way task-agent launches have one, since Reconcile
// can't block waiting for a foreground process to exit.
func (a *app) launchPersona(ctx context.Context, cfg *store.Config, cat *agent.Catalog, p persona.Persona, subj personaSubject) error {
	if !tmux.Usable(ctx, a.runner, cfg.TUI.Tmux) {
		return fmt.Errorf("persona launches require tmux (tui.tmux=%q or tmux not installed)", cfg.TUI.Tmux)
	}
	candidates := a.personaCandidates(cat, p)
	if len(candidates) == 0 {
		return fmt.Errorf("no active agent resolves persona model (agent=%q tier=%q)", p.Model.Agent, p.Model.Tier)
	}
	session := cfg.TUI.TmuxSession
	if session == "" {
		session = domain.DefaultTmuxSession
	}
	tmx := tmux.New(a.runner)
	tmx.Session = session
	window := "persona-" + p.ID

	if err := tmx.Ensure(ctx); err != nil {
		return err
	}

	// EnsureRunning's "skip if the window exists" is wrong here: LaunchScript
	// wraps every run in `exec ${SHELL:-/bin/sh}` so the window survives
	// forever after the agent exits (Crew Roster's output view depends on
	// that survival — see PersonaOutput) — which means the window "exists"
	// permanently after the very first fire, and EnsureRunning would
	// silently no-op every trigger after that: no error, no new process, a
	// stale toast, a stale output pane. Caught for real: a manual dispatch
	// against an already-once-fired persona window did exactly this —
	// "fired" toast, zero new output. Busy is the one case worth skipping
	// (an in-flight run, still executing); a finished or missing window is
	// always safe to replace.
	if busy, err := personaWindowBusy(ctx, tmx, window); err != nil {
		return err
	} else if busy {
		a.auditLogAs(store.Actor{Type: store.ActorPersona, Name: p.ID}, "persona_run", subj.brn,
			"skipped ("+subj.reason+", already running)")
		return nil
	}
	var lastErr error
	for idx, ag := range candidates {
		isLast := idx == len(candidates)-1
		if err := a.tryLaunchCandidate(ctx, tmx, window, ag, p, subj, isLast); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("persona %s: all candidates exhausted", p.ID)
}

// tryLaunchCandidate is launchPersona's per-candidate attempt: build the
// command, scope its permissions/skills, write the launch script, spawn it
// in window, and — for every candidate but the last — give it
// personaQuotaCheckDelay to fail fast on a startup/quota error before
// committing to it. A nil return means the candidate is running and the
// launch has already been audit-logged; a non-nil return means the caller
// should try the next candidate.
func (a *app) tryLaunchCandidate(ctx context.Context, tmx *tmux.Client, window string, ag agent.Agent, p persona.Persona, subj personaSubject, isLast bool) error {
	name, args, err := domain.BuildCmd(ag, domain.ProjectContext(a.dir)+a.personaPrompt(p, subj))
	if err != nil {
		return err
	}
	env := map[string]string{}
	if !p.Authority.BDWrite {
		env = domain.TaskAgentEnv(a.dir)
	}
	permArgs, permEnv, err := personaPermissionExtras(ag.Name, p.ID, p.Authority)
	if err != nil {
		return fmt.Errorf("persona %s: scoping %s permissions: %w", p.ID, ag.Name, err)
	}
	maps.Copy(env, permEnv)
	skillArgs, err := personaSkillExtras(ag.Name, p.ID, p.Skills)
	if err != nil {
		return fmt.Errorf("persona %s: loading skills: %w", p.ID, err)
	}
	var finalArgs []string
	finalArgs = append(finalArgs, permArgs...)
	finalArgs = append(finalArgs, skillArgs...)
	finalArgs = append(finalArgs, args...)
	script := domain.LaunchScript(name, finalArgs, env)
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("baron-persona-%s.sh", p.ID))
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"+script+"\n"), 0o600); err != nil {
		return err
	}
	if err := tmx.Spawn(ctx, window, "sh", a.dir, scriptPath); err != nil {
		return err
	}
	if !isLast {
		select {
		case <-time.After(personaQuotaCheckDelay):
		case <-ctx.Done():
		}
		if out, err := tmx.CapturePane(ctx, window, 80); err == nil {
			if isPersonaStartupFailure(out) {
				_ = tmx.KillWindow(ctx, window)
				return fmt.Errorf("persona %s: %s startup failure/quota, trying next candidate", p.ID, ag.Name)
			}
		}
	}
	a.auditLogAs(store.Actor{Type: store.ActorPersona, Name: p.ID}, "persona_run", subj.brn, subj.reason)
	return nil
}

// personaWindowBusy reports whether window's persona run is still actually
// executing: a missing window is never busy (nothing to skip), and neither
// is one whose wrapper shell has already echoed domain.DoneSentinel — the
// window surviving past that point is deliberate (Crew Roster's output
// view reads it, see PersonaOutput), not a sign the agent is still
// working. Only "exists, no sentinel yet" means a real in-flight run worth
// not interrupting.
func personaWindowBusy(ctx context.Context, tmx *tmux.Client, window string) (bool, error) {
	exists, err := tmx.WindowExists(ctx, window)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	content, err := tmx.CapturePane(ctx, window, personaOutputCaptureLines)
	if err == nil {
		return !strings.Contains(content, domain.DoneSentinel), nil
	}
	return false, nil
}

// personaOutputCaptureLines caps how far back PersonaOutput reads a
// persona's tmux pane — the Crew Roster only ever displays the last
// handful of lines that fit its output panel (view_split.go's fixedBox clips the
// rest), so this stays far smaller than paneHistoryLines
// (internal/tui/terminal.go), which backs the full-screen Terminal tab for
// a task-agent.
const personaOutputCaptureLines = 200

// PersonaOutput returns a snapshot of persona id's tmux pane — the Crew
// Roster's zoom view of what a persona did after firing it. Empty string
// with a nil error means the persona has never run, or its window was
// since closed — the UI shows a hint for that case, not an error; only a
// real lookup failure (tmux unavailable, session gone) is returned as an
// error.
