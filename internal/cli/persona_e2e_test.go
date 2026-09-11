package cli

// End-to-end verification of the persona → bead movement loop, hermetic:
// no real tmux, no AI agents. The e2eRunner scripts a stateful `bd` backend
// so the store's List/Status round-trips actually reflect mutations, and
// every bead movement goes through the same public ChangeStatus /
// transitionStatus path the CLI and TUI use.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// e2eBeadJSON is the JSON shape bd list --json emits for one issue — only
// the keys parseBeads and resolveDomainState need.
type e2eBeadJSON struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Type     string            `json:"type"`
	Status   string            `json:"status"`
	Assignee string            `json:"assignee,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// e2eRunner is a stateful fake for both backends personas touch: `bd`
// (an in-memory bead table mutated by update/assign/comment) and `tmux`
// (a healthy server that records spawns, mirroring tmuxPersonaRunner).
type e2eRunner struct {
	tmuxOut string
	calls   [][]string
	beads   []e2eBeadJSON
}

func newE2ERunner(ids ...string) *e2eRunner {
	r := &e2eRunner{tmuxOut: "tmux 3.4"}
	for _, id := range ids {
		r.beads = append(r.beads, e2eBeadJSON{ID: id, Title: "E2E bead " + id, Type: "task", Status: "open"})
	}
	return r
}

func (r *e2eRunner) statusOf(id string) string {
	for _, b := range r.beads {
		if b.ID == id {
			return b.Status
		}
	}
	return ""
}

func (r *e2eRunner) setStatus(id, status string) {
	for i := range r.beads {
		if r.beads[i].ID == id {
			r.beads[i].Status = status
			return
		}
	}
}

func (r *e2eRunner) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	switch name {
	case "bd":
		return r.runBD(args)
	case "tmux":
		return r.runTmux(args)
	}
	return tool.Result{}, nil // the agent CLI itself; launches never wait on it
}

func (r *e2eRunner) runBD(args []string) (tool.Result, error) {
	if len(args) == 0 {
		return tool.Result{}, nil
	}
	switch args[0] {
	case "list", "ready":
		out, err := json.Marshal(r.beads)
		if err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Stdout: string(out)}, nil
	case "update":
		// bd update <id> --status <status> — Status() is its only caller here.
		if len(args) >= 4 && args[2] == "--status" {
			r.setStatus(args[1], args[3])
		}
		return tool.Result{}, nil
	case "config":
		// ensureCustomStatuses probes config get first; "not set" triggers
		// the once-per-process registration write.
		if len(args) >= 3 && args[1] == "get" && args[2] == "status.custom" {
			return tool.Result{Stdout: "not set"}, nil
		}
		return tool.Result{}, nil
	default:
		return tool.Result{}, nil // assign, comment, close, create — recorded, not simulated
	}
}

func (r *e2eRunner) runTmux(args []string) (tool.Result, error) {
	switch {
	case slices.Equal(args, []string{"-V"}):
		return tool.Result{Stdout: r.tmuxOut}, nil
	case len(args) > 2 && args[0] == "has-session" && strings.Contains(args[2], ":"):
		// Must FAIL like real tmux: if absence reads as existence,
		// personaWindowBusy silently skips the spawn as "window busy".
		return tool.Result{ExitCode: 1, Stderr: "can't find window: " + args[2]}, errors.New("exit status 1")
	}
	return tool.Result{}, nil
}

// personaWindows counts successful "qa" persona window spawns.
func (r *e2eRunner) personaWindows() int {
	n := 0
	for _, c := range r.calls {
		if c[0] == "tmux" && c[1] == "new-window" && slices.Contains(c, "persona-qa") {
			n++
		}
	}
	return n
}

// personaRunEvents counts persona_run audit events across every fire.
func personaRunEvents(t *testing.T, a *app, target string) []store.AuditEvent {
	t.Helper()
	var out []store.AuditEvent
	for _, e := range auditEvents(t, a) {
		if e.Action == "persona_run" && (target == "" || e.Target == target) {
			out = append(out, e)
		}
	}
	return out
}

// listBeads re-reads the store the way the next Reconcile pass would.
func listBeads(t *testing.T, a *app) []store.Bead {
	t.Helper()
	beads, err := a.beads.List(context.Background())
	if err != nil {
		t.Fatalf("beads.List: %v", err)
	}
	return beads
}

// move pushes brn to target through the public API with the given actor,
// exactly as a human, manager or persona action would land in BARON.
func move(t *testing.T, a *app, brn domain.BRN, to domain.BeadState, actor domain.Actor) error {
	t.Helper()
	_, err := a.ChangeStatus(context.Background(), brn, to, actor)
	return err
}

// mergeToMerged walks brn open→mergable→merged with user then manager
// actors, reconciling between hops so each leg lands as its own observed
// transition (mirrors how passes see real pipeline movement).
func mergeToMerged(t *testing.T, a *app, cat *agent.Catalog, brn domain.BRN, cfg *store.Config) {
	t.Helper()
	reconcile := func() {
		a.reconcilePersonaTriggers(context.Background(), cfg, listBeads(t, a), cat)
	}
	if err := move(t, a, brn, domain.BeadStateMergable, a.systemActor()); err != nil {
		t.Fatalf("user open→mergable: %v", err)
	}
	reconcile()
	if err := move(t, a, brn, domain.BeadStateMerged, a.managerActor()); err != nil {
		t.Fatalf("manager mergable→merged: %v", err)
	}
	reconcile()
}

func personaQA(minIntervalMinutes int) persona.Persona {
	return persona.Persona{
		ID: "qa", Name: "QA", Prompt: "check",
		Enabled: true,
		Model:   persona.Model{Agent: "claude"},
		Trigger: persona.Trigger{
			On:                 []persona.TransitionRule{{From: "*", To: "merged"}},
			MinIntervalMinutes: minIntervalMinutes,
		},
		Authority: persona.Authority{BDWrite: true},
	}
}

// S1 — happy path: enabling an On:*→merged persona fires it exactly once
// when a bead merges, with a persona-actor audit row, and never again on
// unchanged follow-up passes.
func TestPersonaE2EFiresOnMergeAndDoesNotRefire(t *testing.T) {
	r := newE2ERunner("f1")
	a := newTestApp(t, r)
	writeAgentRegistry(t, testClaude())
	cat := loadTestCatalog(t)
	p := personaQA(0)
	a.loadPersonas = func() ([]persona.Persona, error) { return []persona.Persona{p}, nil }
	cfg := store.DefaultConfig()

	mergeToMerged(t, a, cat, "f1", cfg)

	if got := r.personaWindows(); got != 1 {
		t.Fatalf("persona-qa spawns = %d, want exactly 1 after mergable→merged", got)
	}
	events := personaRunEvents(t, a, "f1")
	if len(events) != 1 {
		t.Fatalf("persona_run events = %+v, want exactly 1 targeting f1", events)
	}
	if events[0].Actor.Type != store.ActorPersona {
		t.Errorf("persona_run actor = %q, want %q", events[0].Actor.Type, store.ActorPersona)
	}
	if !strings.Contains(events[0].Detail, "mergable→merged") {
		t.Errorf("persona_run detail = %q, want it to cite the mergable→merged transition", events[0].Detail)
	}

	// A quiet pass must not refire on already-seen states.
	a.reconcilePersonaTriggers(context.Background(), cfg, listBeads(t, a), cat)
	if got := r.personaWindows(); got != 1 {
		t.Fatalf("after quiet pass persona-qa spawns = %d, want still 1 (no double fire)", got)
	}

	// The bead really moved in the scripted backend.
	if got := r.statusOf("f1"); got != "merged" {
		t.Fatalf("bd backend f1 status = %q, want merged", got)
	}
}

// S2 — authority boundaries: a persona may reopen a merged bead (merged→open)
// through the public API, is structurally denied any →merged hop, and the
// backend reflects both outcomes.
func TestPersonaE2EAuthorityReopenAllowedMergeDenied(t *testing.T) {
	r := newE2ERunner("f1")
	a := newTestApp(t, r)
	writeAgentRegistry(t, testClaude())
	cat := loadTestCatalog(t)
	a.loadPersonas = func() ([]persona.Persona, error) { return []persona.Persona{personaQA(0)}, nil }
	cfg := store.DefaultConfig()

	mergeToMerged(t, a, cat, "f1", cfg)

	// Persona reopen via public API: merged→open is allowed and persists.
	persona := domain.Actor{Type: domain.ActorPersona, Name: "qa"}
	if err := move(t, a, "f1", domain.BeadStateOpen, persona); err != nil {
		t.Fatalf("persona merged→open reopen: %v", err)
	}
	if got := r.statusOf("f1"); got != "open" {
		t.Errorf("backend f1 status after persona reopen = %q, want open", got)
	}
	var statusEvents []store.AuditEvent
	for _, e := range auditEvents(t, a) {
		if e.Action == "status" && e.Target == "f1" && e.Detail == "merged -> open" {
			statusEvents = append(statusEvents, e)
		}
	}
	if len(statusEvents) != 1 || statusEvents[0].Actor.Type != store.ActorPersona {
		t.Fatalf("reopen audit events = %+v, want exactly one actor-persona 'merged -> open'", statusEvents)
	}

	// Persona cannot merge: fresh bead sits at mergable, denial expected,
	// backend unchanged afterwards.
	if err := move(t, a, "f1", domain.BeadStateMergable, a.systemActor()); err != nil {
		t.Fatalf("user open→mergable (f1): %v", err)
	}
	err := move(t, a, "f1", domain.BeadStateMerged, persona)
	if err == nil {
		t.Fatal("persona mergable→merged succeeded, want denial")
	}
	if !strings.Contains(err.Error(), "for actor persona") {
		t.Errorf("denial error = %v, want it to name the persona actor", err)
	}
	if got := r.statusOf("f1"); got != "mergable" {
		t.Errorf("backend f1 status after denied merge = %q, want still mergable", got)
	}
}

// S3 — debounce: MinIntervalMinutes suppresses a second matching fire even
// when a later bead hits the same subscribed transition inside the window
// (LastRun is keyed per persona).
func TestPersonaE2EDebounceSuppressesSecondFire(t *testing.T) {
	r := newE2ERunner("f1", "f2")
	a := newTestApp(t, r)
	writeAgentRegistry(t, testClaude())
	cat := loadTestCatalog(t)
	a.loadPersonas = func() ([]persona.Persona, error) { return []persona.Persona{personaQA(60)}, nil }
	cfg := store.DefaultConfig()

	mergeToMerged(t, a, cat, "f1", cfg)
	if got := r.personaWindows(); got != 1 {
		t.Fatalf("first merge fires = %d, want 1", got)
	}

	// Second bead merges minutes later within the debounce window.
	if err := move(t, a, "f2", domain.BeadStateMergable, a.systemActor()); err != nil {
		t.Fatalf("user open→mergable (f2): %v", err)
	}
	a.reconcilePersonaTriggers(context.Background(), cfg, listBeads(t, a), cat)
	if err := move(t, a, "f2", domain.BeadStateMerged, a.managerActor()); err != nil {
		t.Fatalf("manager mergable→merged (f2): %v", err)
	}
	a.reconcilePersonaTriggers(context.Background(), cfg, listBeads(t, a), cat)

	if got := r.personaWindows(); got != 1 {
		t.Fatalf("persona-qa spawns after debounced second merge = %d, want still 1", got)
	}
	events := personaRunEvents(t, a, "")
	if len(events) != 1 {
		t.Fatalf("persona_run events total = %d (%+v), want 1 — debounce suppressed the rest", len(events), events)
	}
}
