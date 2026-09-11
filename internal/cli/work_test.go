package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// closeSafeRunner returns a fakeRunner whose `bd list --json` call
// (checkEpicCloseAllowed's precondition) comes back empty, so a close
// proceeds exactly as it did before that check existed.
func closeSafeRunner() *fakeRunner {
	return &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		if name == "bd" && len(args) > 0 && args[0] == "list" {
			return tool.Result{Stdout: "[]"}, nil
		}
		return tool.Result{}, nil
	}}
}

// testConfirm replicates the old app.confirm's exact TTY/--yes semantics as
// a standalone test helper — production code now always supplies its own
// confirm func (alwaysApprove for TUI-triggered actions), but AssignBead/
// CloseBead's respect for a real interactive decline is still worth testing
// against something that behaves like a human would have.
func testConfirm(a *app) func(string) (bool, error) {
	return func(action string) (bool, error) {
		if a.yes || !a.inTTY() {
			return true, nil
		}
		var value bool
		confirm := huh.NewConfirm().Title(action).Affirmative("yes").Negative("no").Value(&value)
		err := huh.NewForm(huh.NewGroup(confirm)).WithInput(a.in).WithOutput(a.out).Run()
		return value, err
	}
}

func TestWorkCreatePassesAcceptanceAndPriority(t *testing.T) {
	fr := &fakeRunner{out: func([]string) string { return "baron-a1b2c3\n" }}
	a := newTestApp(t, fr)
	if _, err := a.CreateBead(context.Background(), CreateBeadInput{Title: "My task", Accept: "tests pass", Priority: "P1", Tier: "standard"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !hasCall(fr, "create", "My task", "--json", "--acceptance", "tests pass", "--priority", "0", "--metadata", `{"tier":"standard"}`) {
		t.Fatalf("calls = %v, want bd create with acceptance and priority", fr.calls)
	}
}

func TestWorkCreatePriorityDefaultsToBD(t *testing.T) {
	fr := &fakeRunner{out: func([]string) string { return "baron-a1b2c3\n" }}
	a := newTestApp(t, fr)
	if _, err := a.CreateBead(context.Background(), CreateBeadInput{Title: "My task", Tier: "standard"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if hasCall(fr, "create", "My task", "--priority", "2") {
		t.Fatalf("calls = %v, default priority must not pass --priority", fr.calls)
	}
}

func TestWorkCreateInvalidPriority(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	_, err := a.CreateBead(context.Background(), CreateBeadInput{Title: "My task", Priority: "urgent", Tier: "standard"})
	if err == nil {
		t.Fatal("CreateBead with invalid priority: want error, got nil")
	}
	var ue usageError
	if !errors.As(err, &ue) {
		t.Errorf("error = %v, want a usageError", err)
	}
}

// TestWorkCreateRequiresTier: tier is mandatory, so omitting it must fail
// before bd is ever invoked.
func TestWorkCreateRequiresTier(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	if _, err := a.CreateBead(context.Background(), CreateBeadInput{Title: "My task"}); err == nil {
		t.Fatal("CreateBead with no tier: want error, got nil")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("calls = %v, want none — missing tier must fail before bd runs", fr.calls)
	}
}

// TestWorkCreateInvalidTier: an unrecognized tier is a usage error, the
// same way an invalid priority is.
func TestWorkCreateInvalidTier(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	_, err := a.CreateBead(context.Background(), CreateBeadInput{Title: "My task", Tier: "legendary"})
	if err == nil {
		t.Fatal("CreateBead with invalid tier: want error, got nil")
	}
	var ue usageError
	if !errors.As(err, &ue) {
		t.Errorf("error = %v, want a usageError", err)
	}
}

func TestWorkEdit(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	if _, err := a.EditBead(context.Background(), "baron-a1b2c3", "New title", "new desc"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !hasCall(fr, "update", "baron-a1b2c3", "--title", "New title", "--description", "new desc") {
		t.Fatalf("calls = %v, want bd update", fr.calls)
	}
}

func TestWorkAssignNonTTY(t *testing.T) {
	fr := &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		if name == "which" {
			return tool.Result{ExitCode: 1}, errors.New("exit status 1")
		}
		return tool.Result{}, nil
	}}
	a := newTestApp(t, fr)
	if _, _, err := a.AssignBead(context.Background(), "baron-a1b2c3", "", "codex", "", testConfirm(a)); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !hasCall(fr, "assign", "baron-a1b2c3", "codex") {
		t.Fatalf("calls = %v, want bd assign", fr.calls)
	}
	if !strings.Contains(stderr(t, a), "not on PATH") {
		t.Fatalf("stderr = %q, want the unknown-agent warning", stderr(t, a))
	}
}

// TestWorkAssignResolvesAgentFromCatalog: a model ID resolves the agent
// that runs it from the catalog entry, not from splitting the ID. That
// mapping is not recoverable from the string: an opencode ID like
// "opencode-go/deepseek-v4-flash" names a provider, not a CLI.
func TestWorkAssignResolvesAgentFromCatalog(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	writeCatalog(t, agent.Model{
		ID: "opencode-go/deepseek-v4-flash", Agent: "opencode",
		Name: "opencode-go/deepseek-v4-flash", Efforts: []string{"high", "low"},
	})
	if _, _, err := a.AssignBead(context.Background(), "baron-a1b2c3", "opencode-go/deepseek-v4-flash", "", "", testConfirm(a)); err != nil {
		t.Fatalf("assign: %v", err)
	}
	// bd's assignee is the agent CLI; the model rides along as metadata.
	if !hasCall(fr, "assign", "baron-a1b2c3", "opencode") {
		t.Fatalf("calls = %v, want bd assign to the agent from the catalog entry", fr.calls)
	}
	if !hasCall(fr, "update", "baron-a1b2c3", "--set-metadata", "model=opencode-go/deepseek-v4-flash",
		"--unset-metadata", "llm") {
		t.Fatalf("calls = %v, want the model recorded as metadata", fr.calls)
	}
}

// TestWorkAssignUnknownModelIsRejected: a model that is in no catalog fails
// immediately with a pointer at both fixes, rather than being recorded and
// failing much later at run time with a confusing lookup error.
func TestWorkAssignUnknownModelIsRejected(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	_, _, err := a.AssignBead(context.Background(), "baron-a1b2c3", "opencode/big-pickle", "", "", testConfirm(a))
	if err == nil {
		t.Fatal("assign model opencode/big-pickle: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "baron doctor") || !strings.Contains(err.Error(), "--agent") {
		t.Errorf("error = %v, want it to point at the catalog and an explicit agent", err)
	}
	if hasCall(fr, "assign", "baron-a1b2c3", "opencode/big-pickle") {
		t.Error("bd assign was called with an unknown model; want it rejected before any bd call")
	}
}

// TestWorkAssignAgentWithExplicitModel: naming the agent pins the CLI and
// the model is passed to it verbatim, which is how a model too new for the
// catalog is assigned.
func TestWorkAssignAgentWithExplicitModel(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	if _, _, err := a.AssignBead(context.Background(), "baron-a1b2c3", "claude-haiku-4-5-20251001", "claude", "", testConfirm(a)); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !hasCall(fr, "assign", "baron-a1b2c3", "claude") {
		t.Fatalf("calls = %v, want bd assign to claude", fr.calls)
	}
	if !hasCall(fr, "update", "baron-a1b2c3", "--set-metadata", "model=claude-haiku-4-5-20251001",
		"--unset-metadata", "llm") {
		t.Fatalf("calls = %v, want the raw model passed through", fr.calls)
	}
}

// TestWorkAssignAgentOnPATH: assigning an agent whose CLI is on PATH (but
// not in the cache) must not warn — the on-demand probe resolves it.
func TestWorkAssignAgentOnPATH(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	if _, _, err := a.AssignBead(context.Background(), "baron-a1b2c3", "", "codex", "", testConfirm(a)); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !hasCall(fr, "assign", "baron-a1b2c3", "codex") {
		t.Fatalf("calls = %v, want bd assign", fr.calls)
	}
	if strings.Contains(stderr(t, a), "not on PATH") {
		t.Fatalf("stderr = %q, want no warning when the CLI is on PATH", stderr(t, a))
	}
}

func TestWorkAssignTTYDecline(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	a.in = strings.NewReader("n\n")
	a.inTTY = func() bool { return true }
	_, ok, err := a.AssignBead(context.Background(), "baron-a1b2c3", "", "codex", "", testConfirm(a))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if ok {
		t.Fatal("AssignBead: ok = true, want false after decline")
	}
	if hasCall(fr, "assign", "baron-a1b2c3", "codex") {
		t.Fatalf("calls = %v, want no bd assign after decline (the on-demand probe may run)", fr.calls)
	}
}

func TestWorkAssignTTYAccept(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	a.in = strings.NewReader("y\n")
	a.inTTY = func() bool { return true }
	if _, ok, err := a.AssignBead(context.Background(), "baron-a1b2c3", "", "codex", "", testConfirm(a)); err != nil || !ok {
		t.Fatalf("assign: ok=%v err=%v", ok, err)
	}
	if !hasCall(fr, "assign", "baron-a1b2c3", "codex") {
		t.Fatalf("calls = %v, want bd assign after confirm", fr.calls)
	}
}

func TestWorkCloseNonTTY(t *testing.T) {
	fr := closeSafeRunner()
	a := newTestApp(t, fr)
	if _, _, err := a.CloseBead(context.Background(), "baron-a1b2c3", testConfirm(a)); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !hasCall(fr, "close", "baron-a1b2c3") {
		t.Fatalf("calls = %v, want bd close", fr.calls)
	}
}

func TestWorkCloseTTYDecline(t *testing.T) {
	fr := closeSafeRunner()
	a := newTestApp(t, fr)
	a.in = strings.NewReader("n\n")
	a.inTTY = func() bool { return true }
	if _, _, err := a.CloseBead(context.Background(), "baron-a1b2c3", testConfirm(a)); err != nil {
		t.Fatalf("close: %v", err)
	}
	// checkEpicCloseAllowed's list call runs before the prompt; only `bd
	// close` itself must not happen after a decline.
	if hasCall(fr, "close", "baron-a1b2c3") {
		t.Fatalf("calls = %v, want no bd close after decline", fr.calls)
	}
}

func TestWorkComment(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	if _, err := a.AddComment(context.Background(), "baron-a1b2c3", "looks good"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if !hasCall(fr, "comment", "baron-a1b2c3", "looks good") {
		t.Fatalf("calls = %v, want bd comment", fr.calls)
	}
}

func TestWorkAssignAudits(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	if _, _, err := a.AssignBead(context.Background(), "baron-a1b2c3", "", "codex", "", testConfirm(a)); err != nil {
		t.Fatalf("assign: %v", err)
	}
	events := auditEvents(t, a)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Action != "assign" || e.Target != "baron-a1b2c3" || e.Actor.Type != store.ActorUser {
		t.Fatalf("event = %+v, want user assign on baron-a1b2c3", e)
	}
	if !strings.Contains(e.Detail, "codex") {
		t.Fatalf("detail = %q, want the agent name", e.Detail)
	}
}

func TestWorkCloseAudits(t *testing.T) {
	a := newTestApp(t, closeSafeRunner())
	if _, _, err := a.CloseBead(context.Background(), "baron-a1b2c3", testConfirm(a)); err != nil {
		t.Fatalf("close: %v", err)
	}
	events := auditEvents(t, a)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Action != "close" || e.Target != "baron-a1b2c3" || e.Actor.Type != store.ActorUser {
		t.Fatalf("event = %+v, want user close on baron-a1b2c3", e)
	}
}

func TestWorkCommentAudits(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	if _, err := a.AddComment(context.Background(), "baron-a1b2c3", "looks good"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	events := auditEvents(t, a)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Action != "comment" || e.Target != "baron-a1b2c3" || e.Detail != "looks good" {
		t.Fatalf("event = %+v, want comment with detail", e)
	}
}

func TestWorkDepAddAudits(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	if _, err := a.AddDep(context.Background(), "baron-a1b2c3", "baron-d4e5f6", "related"); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	events := auditEvents(t, a)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Action != "dep_add" || e.Target != "baron-a1b2c3" || !strings.Contains(e.Detail, "baron-d4e5f6") {
		t.Fatalf("event = %+v, want dep_add on baron-a1b2c3", e)
	}
}

func TestWorkDepAddBlocks(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	if _, err := a.AddDep(context.Background(), "baron-a1b2c3", "baron-d4e5f6", "blocks"); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	// blocks is bd's default; DepAdd must not pass --type through for it.
	if !hasCall(fr, "dep", "add", "baron-a1b2c3", "baron-d4e5f6") {
		t.Fatalf("calls = %v, want plain bd dep add", fr.calls)
	}
}

func TestWorkDepAddRelated(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	if _, err := a.AddDep(context.Background(), "baron-a1b2c3", "baron-d4e5f6", "related"); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	if !hasCall(fr, "dep", "add", "baron-a1b2c3", "baron-d4e5f6", "--type", "related") {
		t.Fatalf("calls = %v, want related dep type", fr.calls)
	}
}

// TestWorkDepAddDiscoveredFrom: discovered-from is a valid dependency type
// alongside blocks/related/parent-child.
func TestWorkDepAddDiscoveredFrom(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	if _, err := a.AddDep(context.Background(), "baron-a1b2c3", "baron-d4e5f6", "discovered-from"); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	if !hasCall(fr, "dep", "add", "baron-a1b2c3", "baron-d4e5f6", "--type", "discovered-from") {
		t.Fatalf("calls = %v, want discovered-from dep type", fr.calls)
	}
}

func TestWorkDepAddInvalidType(t *testing.T) {
	if slices.Contains(validDepTypes, "wat") {
		t.Fatal("validDepTypes unexpectedly contains \"wat\"")
	}
}

// validatingList serves a list where baron-d4e5f6 is in validating state, the
// only state from which a mergable transition is legal.
func validatingList(args []string) string {
	if len(args) > 0 && args[0] == "ready" {
		return fixtureReady
	}
	return `[{"id":"baron-a1b2c3","title":"Implement auth","status":"open","priority":2,"issue_type":"task","owner":"git@baron.test","created_at":"2026-08-09T10:00:00Z","updated_at":"2026-08-09T10:00:00Z"},` +
		`{"id":"baron-d4e5f6","title":"Fix login bug","status":"validating","priority":1,"issue_type":"task","created_at":"2026-08-09T11:00:00Z","updated_at":"2026-08-09T12:00:00Z"}]`
}

// workingList is validatingList's sibling with the second bead in the
// working state, the valid predecessor of the validating transition.
func workingList(args []string) string {
	return `[{"id":"baron-a1b2c3","title":"Implement auth","status":"open","priority":2,"issue_type":"task","owner":"git@baron.test","created_at":"2026-08-09T10:00:00Z","updated_at":"2026-08-09T10:00:00Z"},` +
		`{"id":"baron-d4e5f6","title":"Fix login bug","status":"working","priority":1,"issue_type":"task","created_at":"2026-08-09T11:00:00Z","updated_at":"2026-08-09T12:00:00Z"}]`
}

func TestWorkStatusValidTransition(t *testing.T) {
	fr := &fakeRunner{out: workingList}
	a := newTestApp(t, fr)
	if _, err := a.ChangeStatus(context.Background(), "baron-d4e5f6", domain.BeadStateValidating, a.systemActor()); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !hasCall(fr, "update", "baron-d4e5f6", "--status", "validating") {
		t.Fatalf("calls = %v, want bd update --status validating", fr.calls)
	}
}

// TestWorkStatusRetryExitCode: a manual retry trigger (validating->retry,
// the only real retry edge) reports ExitRetry, not ExitOK, at the layer
// that maps typed-method errors to a process exit code (see mapExitCode) —
// the typed method itself just returns success.
func TestWorkStatusRetryExitCode(t *testing.T) {
	fr := &fakeRunner{out: validatingList}
	a := newTestApp(t, fr)
	res, err := a.ChangeStatus(context.Background(), "baron-d4e5f6", domain.BeadStateRetry, a.systemActor())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if res.To != string(domain.BeadStateRetry) {
		t.Fatalf("res = %+v, want To=retry", res)
	}
}

func TestWorkStatusInvalidTransition(t *testing.T) {
	fr := &fakeRunner{out: listOut}
	a := newTestApp(t, fr)
	if _, err := a.ChangeStatus(context.Background(), "baron-d4e5f6", domain.BeadStateRetry, a.systemActor()); err == nil {
		t.Fatal("illegal transition: want error, got nil")
	}
	if hasCall(fr, "update") {
		t.Fatalf("calls = %v, want no bd update on illegal transition", fr.calls)
	}
}

// TestWorkStatusRejectsAssigned: "assigned" is derived from an assignee, not
// a writable bd status column — ChangeStatus(open -> assigned) used to
// report success while silently leaving the bead unassigned and its status
// at "open" (bdStatus maps assigned -> open). AssignBead is the only path
// that actually reaches the assigned state.
func TestWorkStatusRejectsAssigned(t *testing.T) {
	fr := &fakeRunner{out: listOut}
	a := newTestApp(t, fr)
	_, err := a.ChangeStatus(context.Background(), "baron-a1b2c3", domain.BeadStateAssigned, a.systemActor())
	if err == nil {
		t.Fatal("status(open -> assigned): want an error directing to assign")
	}
	if !strings.Contains(err.Error(), "work assign") {
		t.Errorf("error = %v, want it to mention assign", err)
	}
	if hasCall(fr, "update") {
		t.Errorf("calls = %v, want no bd update for a rejected assigned transition", fr.calls)
	}
}

func TestWorkStatusNoOp(t *testing.T) {
	fr := &fakeRunner{out: validatingList}
	a := newTestApp(t, fr)
	if _, err := a.ChangeStatus(context.Background(), "baron-a1b2c3", domain.BeadStateOpen, a.systemActor()); err != nil {
		t.Fatalf("status: %v", err)
	}
	if hasCall(fr, "update") {
		t.Fatalf("calls = %v, want no bd update for a no-op transition", fr.calls)
	}
}

// TestWorkStatusResetAssignedToOpenClearsAssignee: an "assigned" bead
// (bd status=open plus a non-empty assignee — see store.Bead.DomainState)
// reset to "open" via ChangeStatus must clear the assignee and model
// metadata, not just no-op the bd status write — bd's status is already
// "open" for an assigned bead, so without this the bead would read right
// back as "assigned" the instant anything re-lists it, defeating the whole
// point of the manual reset (send it through fresh tier resolution).
func TestWorkStatusResetAssignedToOpenClearsAssignee(t *testing.T) {
	beadJSON := `[{"id":"baron-a1b2c3","title":"Implement auth","status":"open","priority":2,"issue_type":"task","assignee":"claude","metadata":{"tier":"free","model":"muse-spark"}}]`
	fr := &fakeRunner{out: func(args []string) string { return beadJSON }}
	a := newTestApp(t, fr)

	if _, err := a.ChangeStatus(context.Background(), "baron-a1b2c3", domain.BeadStateOpen, a.systemActor()); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !hasCall(fr, "assign", "baron-a1b2c3", "") {
		t.Errorf("calls = %v, want the assignee cleared (bd assign baron-a1b2c3 \"\")", fr.calls)
	}
	if !hasCall(fr, "update", "baron-a1b2c3", "--unset-metadata", "model", "--unset-metadata", "llm") {
		t.Errorf("calls = %v, want the model metadata cleared", fr.calls)
	}
}

// TestWorkStatusResetRetryToOpenClearsAssignee: same reset, from "retry"
// instead of "assigned" — the other state this manual escape hatch applies
// to (see allowedTransitions' retry -> open comment).
func TestWorkStatusResetRetryToOpenClearsAssignee(t *testing.T) {
	beadJSON := `[{"id":"baron-a1b2c3","title":"Implement auth","status":"retry","priority":2,"issue_type":"task","assignee":"claude","metadata":{"tier":"free","model":"muse-spark"}}]`
	fr := &fakeRunner{out: func(args []string) string { return beadJSON }}
	a := newTestApp(t, fr)

	if _, err := a.ChangeStatus(context.Background(), "baron-a1b2c3", domain.BeadStateOpen, a.systemActor()); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !hasCall(fr, "update", "baron-a1b2c3", "--status", "open") {
		t.Errorf("calls = %v, want bd update --status open", fr.calls)
	}
	if !hasCall(fr, "assign", "baron-a1b2c3", "") {
		t.Errorf("calls = %v, want the assignee cleared (bd assign baron-a1b2c3 \"\")", fr.calls)
	}
}

func TestWorkCloseBlockedByOpenChild(t *testing.T) {
	fr := &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		if name == "bd" && len(args) > 0 && args[0] == "list" {
			return tool.Result{Stdout: `[{"id":"baron-a1b2c3","title":"epic","status":"open","issue_type":"epic"},` +
				`{"id":"baron-a1b2c3.1","title":"child","status":"open","issue_type":"task"}]`}, nil
		}
		return tool.Result{}, nil
	}}
	a := newTestApp(t, fr)
	_, _, err := a.CloseBead(context.Background(), "baron-a1b2c3", testConfirm(a))
	if err == nil || !strings.Contains(err.Error(), "open children") {
		t.Fatalf("close error = %v, want an open-children rejection", err)
	}
	if hasCall(fr, "close", "baron-a1b2c3") {
		t.Fatalf("calls = %v, want no bd close when a child is open", fr.calls)
	}
}

func TestWorkCloseBlockedByOpenPR(t *testing.T) {
	fr := &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		if name == "bd" && len(args) > 0 && args[0] == "list" {
			return tool.Result{Stdout: `[{"id":"baron-a1b2c3","title":"t","status":"mergable","priority":2,"issue_type":"task"}]`}, nil
		}
		return tool.Result{}, nil
	}}
	a := newTestApp(t, fr)
	_, _, err := a.CloseBead(context.Background(), "baron-a1b2c3", testConfirm(a))
	if err == nil || !strings.Contains(err.Error(), "ready to merge") {
		t.Fatalf("close error = %v, want a ready-to-merge rejection", err)
	}
	if hasCall(fr, "close", "baron-a1b2c3") {
		t.Fatalf("calls = %v, want no bd close when the bead is ready to merge", fr.calls)
	}
}

func TestWorkCloseAllowedWhenChildrenClosed(t *testing.T) {
	fr := &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		if name == "bd" && len(args) > 0 && args[0] == "list" {
			return tool.Result{Stdout: `[{"id":"baron-a1b2c3","title":"epic","status":"open","issue_type":"epic"},` +
				`{"id":"baron-a1b2c3.1","title":"child","status":"closed","issue_type":"task"}]`}, nil
		}
		return tool.Result{}, nil
	}}
	a := newTestApp(t, fr)
	if _, _, err := a.CloseBead(context.Background(), "baron-a1b2c3", testConfirm(a)); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !hasCall(fr, "close", "baron-a1b2c3") {
		t.Fatalf("calls = %v, want bd close once children are closed", fr.calls)
	}
}
