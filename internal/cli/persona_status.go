package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tmux"
)

func (a *app) PersonaOutput(ctx context.Context, id string) (string, error) {
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return "", err
	}
	if !tmux.Usable(ctx, a.runner, cfg.TUI.Tmux) {
		return "", nil
	}
	session := cfg.TUI.TmuxSession
	if session == "" {
		session = domain.DefaultTmuxSession
	}
	tmx := tmux.New(a.runner)
	tmx.Session = session
	window := "persona-" + id
	exists, err := tmx.WindowExists(ctx, window)
	if err == nil && exists {
		return tmx.CapturePane(ctx, window, personaOutputCaptureLines)
	}
	return "", nil
}

// PersonaStatus is one persona's live run state — Running is a real,
// tmux-checked fact (personaWindowBusy), Since is BARON's own best guess at
// when that run started (personaState.LastRun, the only timestamp BARON
// tracks — tmux itself exposes no pane-start time worth querying). Since is
// the zero time when Running is false.
type PersonaStatus struct {
	Running bool
	Since   time.Time
}

// PersonaStatuses reports every persona's live run state in one pass — the
// Crew Roster's status-first rows (§2.5 of crew-mode.md's redesign: "the
// list isn't badly formatted, it's showing the wrong kind of information")
// need this for every row at once, not per-row on demand, so this is one
// bulk fetch (one tmux round-trip per persona, still cheap relative to a
// human's glance cadence) rather than N separate Deps calls.
func (a *app) PersonaStatuses(ctx context.Context, ids []string) (map[string]PersonaStatus, error) {
	out := make(map[string]PersonaStatus, len(ids))
	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return out, err
	}
	if !tmux.Usable(ctx, a.runner, cfg.TUI.Tmux) {
		return out, nil
	}
	session := cfg.TUI.TmuxSession
	if session == "" {
		session = domain.DefaultTmuxSession
	}
	tmx := tmux.New(a.runner)
	tmx.Session = session
	state := a.loadPersonaState()
	for _, id := range ids {
		busy, err := personaWindowBusy(ctx, tmx, "persona-"+id)
		if err != nil {
			continue // best-effort — one stuck tmux query never blocks the rest of the roster
		}
		if busy {
			out[id] = PersonaStatus{Running: true, Since: state.LastRun[id]}
		}
	}
	return out, nil
}

// PersonaActivity returns persona id's recent audit trail — every bead it
// has been pointed at and why (dispatched / reacted to a transition / cron
// sweep), newest first. This is the Crew Roster's answer to "where are the
// beads": not a live pane, but a feed of exactly what the persona has done
// to the board, each entry a jump-off point to the real bead thread where
// its actual comments/created beads live.
func (a *app) PersonaActivity(id string) ([]store.AuditEvent, error) {
	if a.audit == nil {
		return nil, nil
	}
	all, err := a.audit.All()
	if err != nil {
		return nil, err
	}
	var out []store.AuditEvent
	for _, e := range slices.Backward(all) {
		if e.Actor.Type == store.ActorPersona && e.Actor.Name == id {
			out = append(out, e)
		}
	}
	return out, nil
}

// resolvePersonaAgent resolves p.Model to an installed, active agent.Agent:
// p.Model.Agent names one directly if set, otherwise p.Model.Tier (default
// agent.TierStandard) picks one via pickModelForTier from the catalog.
func (a *app) resolvePersonaAgent(cat *agent.Catalog, p persona.Persona) (agent.Agent, bool) {
	a.loadAgents()
	if p.Model.Agent != "" && p.Model.Agent != "auto" {
		if ag, ok := a.agents.Get(p.Model.Agent); ok && ag.Status == agent.StatusActive {
			return ag, true
		}
	}
	tier := agent.Tier(p.Model.Tier)
	if tier == "" {
		tier = agent.TierStandard
	}
	if m, ok := a.pickModelForTier(cat, tier); ok {
		if ag, ok := a.agents.Get(m.Agent); ok && ag.Status == agent.StatusActive {
			return ag, true
		}
	}
	// Prefer installed, active agent CLIs: agy, opencode, gemini, codex, claude
	for _, preferred := range []string{"agy", "opencode", "gemini", "codex", "claude"} {
		if ag, ok := a.agents.Get(preferred); ok && ag.Status == agent.StatusActive {
			return ag, true
		}
	}
	for _, ag := range a.agents.Active() {
		return ag, true
	}
	return agent.Agent{}, false
}

func (a *app) personaCandidates(cat *agent.Catalog, p persona.Persona) []agent.Agent {
	a.loadAgents()
	seen := map[string]bool{}
	var out []agent.Agent
	add := func(ag agent.Agent) {
		if ag.Name == "" || len(ag.Args) == 0 || seen[ag.Name] {
			return
		}
		seen[ag.Name] = true
		out = append(out, ag)
	}
	if ag, ok := a.resolvePersonaAgent(cat, p); ok {
		add(ag)
	}
	if m, ok := a.pickModelForTier(cat, agent.TierFree); ok {
		if ag, ok := a.agents.Get(m.Agent); ok && ag.Status == agent.StatusActive {
			ag.Args = ag.Invocation(m.Name, "")
			add(ag)
		}
	}
	for _, name := range []string{"agy", "opencode", "gemini", "codex", "claude"} {
		if ag, ok := a.agents.Get(name); ok && ag.Status == agent.StatusActive {
			add(ag)
		}
	}
	for _, ag := range a.agents.Active() {
		add(ag)
	}
	return out
}

// personaPrompt builds the text handed to a persona's agent: p.Authority's
// allowed bd actions, then a mechanical "here is your subject, and here is
// your entire vocabulary of action" contract derived from subj, then the
// persona's own instructions last.
func (a *app) personaPrompt(p persona.Persona, subj personaSubject) string {
	var b strings.Builder
	if len(p.Authority.Actions) > 0 {
		b.WriteString("Yalnızca şu bd işlemlerini kullanabilirsin: ")
		b.WriteString(strings.Join(p.Authority.Actions, ", "))
		b.WriteString(". Başka hiçbir durum değişikliği yapma (özellikle bead durumunu bd update ile değiştirme).\n\n")
	}
	actor := "persona:" + p.ID
	switch {
	case subj.brn != "":
		fmt.Fprintf(&b, "Konu bead'in: %s (%s).\n", subj.brn, subj.reason)
		if subj.branch != "" {
			fmt.Fprintf(&b, "İlgili Git Branch'i: %s (iş bu branch üzerinde yürütülüyor).\n", subj.branch)
		}
		fmt.Fprintf(&b, "Aksiyon almadan önce `bd show %s` ve `bd comments %s` ile bead'i ve tüm yorum geçmişini oku "+
			"— en son insan yorumu genelde senin talimatındır.\n", subj.brn, subj.brn)
		fmt.Fprintf(&b, "Tek çıktın: bu bead'e `bd comment %s \"...\" --actor %q` ile yorum atmak, ya da bulduğun "+
			"ayrı bir konu için `bd create \"...\" --parent %s --actor %q` ile yeni bir bead açmak. "+
			"Bead durumunu bd update ile değiştirme yetkinin dışında.\n\n", subj.brn, actor, subj.brn, actor)
	case len(subj.candidates) > 0:
		fmt.Fprintf(&b, "İncelenecek adaylar (%s):\n", subj.reason)
		for _, c := range subj.candidates {
			branch := domain.BranchName(c.IssueType, a.idOf(c.BRN))
			fmt.Fprintf(&b, "- %s — %q (durum: %s, branch: %s)\n", c.BRN, c.Title, c.Status, branch)
		}
		fmt.Fprintf(&b, "\nBulgun varsa ilgili bead'e `bd comment <id> \"...\" --actor %q` ile yorum at ya da "+
			"`bd create \"...\" --parent <id> --actor %q` ile yeni bead aç. Başka hiçbir durum değişikliği yapma.\n\n", actor, actor)
	default:
		fmt.Fprintf(&b, "Genel repo/main sweep (%s).\n\n", subj.reason)
	}
	b.WriteString(p.Prompt)
	return b.String()
}

// isPersonaStartupFailure checks whether a spawned candidate agent CLI failed
// immediately at startup (e.g. hit weekly limit, quota, rate limit, auth error,
// or exited with non-zero exit code).
func isPersonaStartupFailure(out string) bool {
	low := strings.ToLower(out)
	// Check for non-zero exit sentinel: baron-run-exit=N where N != 0
	if _, after, ok := strings.Cut(low, "baron-run-exit="); ok {
		rest := strings.TrimSpace(after)
		if len(rest) > 0 && rest[0] != '0' {
			return true
		}
	}
	// Check for common quota / rate-limit / provider error indicators
	indicators := []string{
		"quota", "rate limit", "overloaded", "too many requests",
		"hit your weekly limit", "weekly limit", "usage limit", "hit your limit",
		"reached your limit", "credit balance", "insufficient credits",
		"unauthorized", "authentication failed", "invalid api key",
		"429", "529", "500", "502", "503", "504",
		"service unavailable", "bad gateway", "temporarily unavailable",
	}
	for _, ind := range indicators {
		if strings.Contains(low, ind) {
			return true
		}
	}
	return false
}
