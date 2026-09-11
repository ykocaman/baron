package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tmux"
)

// editResult is the JSON shape of `baron work edit`.
type editResult struct {
	BRN         domain.BRN `json:"brn"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
}

// EditBead updates brn's title and/or description, cobra-free. Callers
// decide whether "both empty" is worth rejecting for their context (the CLI
// treats it as a usage error since it means the user forgot a flag).
func (a *app) EditBead(ctx context.Context, brn domain.BRN, title, description string) (editResult, error) {
	if err := a.beads.Edit(ctx, a.idOf(brn), title, description); err != nil {
		return editResult{}, err
	}
	return editResult{BRN: brn, Title: title, Description: description}, nil
}

// assignResult is the JSON shape of `baron work assign`.
type assignResult struct {
	BRN    domain.BRN `json:"brn"`
	Agent  string     `json:"agent"`
	Model  string     `json:"model,omitempty"`
	Effort string     `json:"effort,omitempty"`
}

// assignment is a resolved assign target: the agent CLI that will run the
// bead, the model to hand it, and the catalog entry the user picked.
type assignment struct {
	agent agent.Agent
	// model is the value passed on the agent's model flag. "" means the
	// agent runs whatever model it is configured to by default.
	model string
	// catalogID is the picked catalog entry, recorded so the assign
	// picker's recent/popular ranking reflects CLI assigns too.
	catalogID string
	// efforts are the levels the picked model accepts, for validation.
	efforts []string
}

// newWorkAssignCmd builds `baron work assign`.
//
// Assignment is model-first: --model takes a catalog ID (`baron doctor
// --models` lists them) and the agent that runs it comes from the catalog
// entry, because that mapping is not recoverable from the ID itself —
// opencode's own IDs are "provider/model" pairs like
// "opencode-go/deepseek-v4-flash" whose prefix names a provider, not the
// CLI. --agent assigns to an agent CLI directly, using its default model;
// passing both pins the agent and sends --model to it verbatim, which is
// how a model too new or too specific to be in the catalog is assigned.
// AssignBead resolves modelID/agentName to an agent+model, confirms via the
// injected confirm func, and assigns brn to it. cobra-free: no flag
// parsing, no --json branching, no a.confirm called directly (the caller
// supplies confirm — a CLI stdin prompt, a TUI huh.Confirm screen, or a
// func that always approves for a context nothing should block on).
// ok=false with err=nil means confirm declined — not an error, same as the
// original RunE's "aborted, return nil" path.
func (a *app) AssignBead(ctx context.Context, brn domain.BRN, modelID, agentName, effort string, confirm func(string) (bool, error)) (res assignResult, ok bool, err error) {
	as, err := a.resolveAssignment(ctx, modelID, agentName)
	if err != nil {
		return assignResult{}, false, err
	}
	a.warnEffort(as, effort)

	desc := "assign " + string(brn) + " to " + as.agent.Name
	if as.model != "" {
		desc += " (model: " + as.model + ")"
	}
	if effort != "" {
		desc += " (effort: " + effort + ")"
	}
	approved, err := confirm(desc)
	if err != nil {
		return assignResult{}, false, err
	}
	if !approved {
		return assignResult{}, false, nil
	}
	// bd's assignee is the agent CLI — the thing that runs. The model and
	// effort are separate metadata rather than crammed into one compound
	// assignee string, so each stays its own field (see
	// store.Bead.Model/Effort).
	if err := a.beads.Assign(ctx, a.idOf(brn), as.agent.Name); err != nil {
		return assignResult{}, false, err
	}
	a.auditLog("assign", string(brn), "assigned to "+as.agent.Name)
	if as.model != "" || effort != "" {
		modelP, effortP := &as.model, &effort
		if as.model == "" {
			modelP = nil
		}
		if effort == "" {
			effortP = nil
		}
		if err := a.beads.SetModelMetadata(ctx, a.idOf(brn), modelP, effortP); err != nil {
			return assignResult{}, false, err
		}
		a.auditLog("assign-metadata", string(brn), fmt.Sprintf("model=%s effort=%s", as.model, effort))
	}
	if as.catalogID != "" {
		// Best-effort: the ranking is a convenience, and a cache that can't
		// be written must never fail an assign.
		_ = agent.LoadCatalog().Record(as.catalogID).Save()
	}
	return assignResult{BRN: brn, Agent: as.agent.Name, Model: as.model, Effort: effort}, true, nil
}

// assignLabel renders an assignment for the human-readable confirmation.
func assignLabel(agentName, model string) string {
	if model == "" {
		return agentName
	}
	return agentName + " (" + model + ")"
}

// resolveAssignment turns the --model/--agent flags into a concrete agent
// and model. A --model with no --agent is looked up in the machine-wide
// catalog; a --agent pins the agent and passes --model through untouched,
// which is how a model the catalog doesn't know is assigned.
func (a *app) resolveAssignment(ctx context.Context, modelID, agentName string) (assignment, error) {
	if agentName != "" {
		ag := a.agentNamed(ctx, agentName)
		as := assignment{agent: ag, model: modelID, catalogID: modelID}
		if m, ok := agent.LoadCatalog().Find(modelID); ok && m.Agent == ag.Name {
			// The picked ID is a catalog entry for this very agent, so use
			// the flag value it records rather than the ID: for claude the
			// two differ ("claude/opus" vs "opus").
			as.model, as.efforts = m.Name, m.Efforts
		}
		return as, nil
	}
	m, ok := agent.LoadCatalog().Find(modelID)
	if !ok {
		// The catalog may simply predate this model: a newly installed CLI,
		// or an [[agents]] block added since the last discovery. Rebuild it
		// once before giving up, so "unknown model" always means unknown
		// rather than un-refreshed.
		d, err := agent.Refresh(ctx, a.runner, a.configAgents())
		if err != nil {
			a.warn("agent cache: %v", err)
		}
		if m, ok = d.Catalog.Find(modelID); !ok {
			return assignment{}, fmt.Errorf(
				"unknown model %q — `baron doctor --models` lists what this machine can run, "+
					"or name the agent explicitly: `--agent <name> --model %s`", modelID, modelID,
			)
		}
	}
	ag := a.agentNamed(ctx, m.Agent)
	return assignment{agent: ag, model: m.Name, catalogID: m.ID, efforts: m.Efforts}, nil
}

// agentNamed resolves an agent by name: the registry first, then an
// on-demand PATH probe so an agent installed since the last `baron doctor`
// still works. An agent that is neither is still returned (bare, with no
// launch args) with a warning rather than an error — recording the
// assignment is useful even when the CLI isn't installed on this machine,
// and `baron run` reports the real problem at launch time.
func (a *app) agentNamed(ctx context.Context, name string) agent.Agent {
	a.loadAgents()
	if ag, ok := a.agents.Get(name); ok {
		return ag
	}
	ag, err := agent.NewProbe(a.runner).ProbeSingle(ctx, name)
	if err != nil {
		a.warn("agent %q is not in the registry and not on PATH; run `baron doctor` to discover agents", name)
		return agent.Agent{Name: name, Command: name, Backend: "subprocess"}
	}
	return ag
}

// warnEffort reports an effort level the target can't honour. Both cases
// are warnings, not errors: the assignment itself is still valid and the
// level is simply dropped at launch.
func (a *app) warnEffort(as assignment, effort string) {
	if effort == "" {
		return
	}
	if as.agent.EffortFlag == "" {
		a.warn("%s has no known reasoning-effort flag; --effort %q will be ignored at run time", as.agent.Name, effort)
		return
	}
	if len(as.efforts) > 0 && !slices.Contains(as.efforts, effort) {
		a.warn("%s does not list effort %q (known: %s)", as.agent.Name, effort, strings.Join(as.efforts, ", "))
	}
}

// commentResult is the JSON shape of `baron work comment`.
type commentResult struct {
	BRN     domain.BRN `json:"brn"`
	Comment string     `json:"comment"`
}

// AddComment adds a comment to brn, cobra-free.
func (a *app) AddComment(ctx context.Context, brn domain.BRN, text string) (commentResult, error) {
	if err := a.beads.Comment(ctx, a.idOf(brn), text); err != nil {
		return commentResult{}, err
	}
	a.auditLog("comment", string(brn), text)
	return commentResult{BRN: brn, Comment: text}, nil
}

// closeResult is the JSON shape of `baron work close`.
type closeResult struct {
	BRN domain.BRN `json:"brn"`
}

// checkEpicCloseAllowed enforces 's epic-closure rule: a bead with
// open children, or ready to merge in its own right, can't close (this
// applies to any bead with children, not only ones typed "epic" — bd's
// hierarchical IDs don't restrict --parent to a particular issue type).
func (a *app) checkEpicCloseAllowed(ctx context.Context, brn domain.BRN) error {
	id := a.idOf(brn)
	all, err := a.beads.List(ctx)
	if err != nil {
		return err
	}
	if open := store.EpicOpenChildren(id, all); len(open) > 0 {
		names := make([]string, len(open))
		for i, b := range open {
			names[i] = string(b.BRN)
		}
		return fmt.Errorf("%s has open children, cannot close: %s", brn, strings.Join(names, ", "))
	}
	for _, b := range all {
		if string(b.BRN) == string(brn) {
			if b.Status == store.BeadStatusMergable {
				return fmt.Errorf("%s is ready to merge, cannot close", brn)
			}
			break
		}
	}
	return nil
}

// lastOpenChildParent returns bead's parent epic out of all when bead was
// the last open child — the shared "is this epic done" check autoCloseEpic
// and tryAdvanceParent each run right after a child reaches a terminal
// state. ok is false when bead has no parent, the parent isn't in its
// "open" resting state (already terminal, or already claimed mergable by
// the other path), or a sibling is still open.
func lastOpenChildParent(bead store.Bead, all []store.Bead) (store.Bead, bool) {
	parent, ok := store.ParentOf(bead, all)
	if !ok || parent.Status != store.BeadStatusOpen {
		return store.Bead{}, false
	}
	if open := store.EpicOpenChildren(parent.ID, all); len(open) > 0 {
		return store.Bead{}, false
	}
	return parent, true
}

// autoCloseEpic implements : when the last open child of an
// epic closes (via `bd work close`, not a merge — see tryAdvanceParent for
// that path), the epic itself closes. Runs right after the child's own
// close, so the child is already terminal when the check reads the list.
// Failures warn and return: the child close already succeeded, so a lookup
// problem must not fail the command after the fact.
func (a *app) autoCloseEpic(ctx context.Context, childID string) {
	all, err := a.beads.List(ctx)
	if err != nil {
		a.warn("epic auto-close: %v", err)
		return
	}
	var child store.Bead
	found := false
	for _, b := range all {
		if b.ID == childID {
			child, found = b, true
			break
		}
	}
	if !found {
		return
	}
	// Only fires from the epic's open resting state: already terminal means
	// nothing to do, and mergable means tryAdvanceParent already claimed
	// this epic for a human-reviewed branch merge — closing it here instead
	// would silently drop that branch's accumulated work.
	parent, ok := lastOpenChildParent(child, all)
	if !ok {
		return
	}
	// ponytail: direct Status write, transitionStatus has no open->closed
	// graph edge (BeadStateOpen allows only assigned/cancelled/mergable),
	// so it would reject the realistic case of an open epic. Same bd write
	// either way: `bd update <epic> --status closed`. Ceiling: a child
	// closed while still ready to merge still counts as closed here — the
	// constraint is approximated by status (mergable children are
	// non-terminal and block the close).
	if err := a.beads.Status(ctx, parent.ID, store.BeadStatusClosed); err != nil {
		a.warn("epic auto-close: %v", err)
		return
	}
	a.recordStateSnapshot(parent.ID, domain.BeadStateClosed)
	a.auditLogAs(storeActor(a.managerActor()), "close", string(parent.BRN), "auto-closed (all children closed)")
	a.killBeadWindow(ctx, parent.BRN)
	if !a.json {
		a.outf("epic %s auto-closed (all children closed)\n", parent.BRN)
	}
}

// tryAdvanceParent checks whether bead's parent (if any) has just lost its
// last open child — bead just merged, so it's terminal (see
// store.isTerminalStatus) — and if so marks the parent ready to merge. A
// parent bead never runs its own agent pipeline, so this is its "gate": a
// human still reviews and merges the parent's own accumulated branch via
// the normal `baron merge` flow, same as any other bead — unlike
// autoCloseEpic's plain-close path, real work landed in the parent's
// branch here, so it can't jump straight to closed. Guarded to only fire
// from the "open" resting state, so it never fires twice or races
// autoCloseEpic.
func (a *app) tryAdvanceParent(ctx context.Context, bead store.Bead) error {
	all, err := a.beads.List(ctx)
	if err != nil {
		return err
	}
	parent, ok := lastOpenChildParent(bead, all)
	if !ok {
		return nil
	}
	if err := a.transitionStatus(ctx, parent.ID, domain.BeadStateOpen, domain.BeadStateMergable, a.managerActor()); err != nil {
		return err
	}
	detail := fmt.Sprintf("ready to merge: all children done, branch %s", domain.BranchName(parent.IssueType, parent.ID))
	a.auditLogAs(storeActor(a.managerActor()), "mergable", string(parent.BRN), detail)
	a.outf("%s\n", detail)
	return nil
}

// killBeadWindow reaps the tmux window holding brn's agent run, if tmux is
// in use. Windows persist after a run (the wrapper shell keeps the pane
// alive) so the output stays inspectable; a bead reaching a terminal state —
// closed, or merged — is the point where that window is torn down. Without
// this the windows accumulate without bound, each holding a live shell.
// Best-effort: a missing window or tmux is fine, and a tmux failure only
// warns — the close or merge itself already succeeded.
func (a *app) killBeadWindow(ctx context.Context, brn domain.BRN) {
	cfg, err := a.loadConfig(a.dir)
	if err != nil || !tmux.Usable(ctx, a.runner, cfg.TUI.Tmux) {
		return
	}
	session := cfg.TUI.TmuxSession
	if session == "" {
		session = domain.DefaultTmuxSession
	}
	tmx := tmux.New(a.runner)
	tmx.Session = session
	// TmuxWindowName, not the raw BRN: a dotted child ("baron-4al.1") runs in
	// a tmux-safe window ("baron-4al-1"), so killing the raw name matches
	// nothing and leaves exactly the windows this is meant to reap.
	if err := tmx.KillWindow(ctx, domain.TmuxWindowName(string(brn))); err != nil {
		a.warn("tmux kill-window: %v", err)
	}
}

// CloseBead closes brn after confirming via the injected confirm func,
// cobra-free. ok=false with err=nil means confirm declined.
func (a *app) CloseBead(ctx context.Context, brn domain.BRN, confirm func(string) (bool, error)) (res closeResult, ok bool, err error) {
	if err := a.checkEpicCloseAllowed(ctx, brn); err != nil {
		return closeResult{}, false, err
	}
	approved, err := confirm(fmt.Sprintf("close %s", brn))
	if err != nil {
		return closeResult{}, false, err
	}
	if !approved {
		return closeResult{}, false, nil
	}
	if err := a.beads.Close(ctx, a.idOf(brn)); err != nil {
		return closeResult{}, false, err
	}
	a.recordStateSnapshot(a.idOf(brn), domain.BeadStateClosed)
	a.auditLog("close", string(brn), "closed")
	a.killBeadWindow(ctx, brn)
	a.autoCloseEpic(ctx, a.idOf(brn))
	return closeResult{BRN: brn}, true, nil
}
