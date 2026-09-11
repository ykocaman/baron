package cli

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// hasAuditDetail reports whether any event's Detail contains substr — used
// in place of a bd comment check now that run.go records these events in
// the audit log only, not as bd comments (baron-08x: system-generated
// run/gate/retry results must not clutter the comment thread).
func hasAuditDetail(events []store.AuditEvent, substr string) bool {
	for _, e := range events {
		if strings.Contains(e.Detail, substr) {
			return true
		}
	}
	return false
}

const runBeadJSON = `[{"id":"baron-a1b2c3","title":"Implement auth","status":"open","priority":2,"issue_type":"task"}]`

const runAssignedBeadJSON = `[{"id":"baron-a1b2c3","title":"Implement auth","status":"assigned","priority":2,"issue_type":"task","assignee":"claude"}]`

// runGateRunner scripts the subprocess calls a `baron run` invocation makes:
// bd list, git worktree list/rev-parse/add, an optional model launch, bd
// status updates, then the gate steps. beadJSON serves `bd list --json`
// (defaults to runBeadJSON when empty). gateExit fails every gate step
// (single-shot --gate tests); gateFailAttempts instead fails the gate for
// only the first N retry-loop attempts (attempt number = model launches so
// far), letting retry-recovery and retry-exhaustion be scripted precisely.
type runGateRunner struct {
	gateExit           int
	gateFailAttempts   int
	beadJSON           string
	modelCmd           string
	modelCall          [][]string
	statusCalls        []string
	comments           []string
	diffOut            string
	logOut             string     // served for `git log`; "" defaults to no commits (signed-commit check passes)
	mergeCalls         [][]string // records `git merge` invocations
	statusOut          string     // served for `git status --porcelain`; "" defaults to " M a.go\n"
	noChanges          bool       // when set, `git status --porcelain` reports a clean tree
	addCalls           [][]string // records `git add` invocations
	missingModel       string     // when set, `which <missingModel>` fails (on-demand probe miss)
	missingWhich       []string   // when set, `which <name>` fails for these names
	modelsOut          string     // served for `opencode models` (provider list discovery)
	commentsJSON       string     // served for `bd comments <id> --json`; "" defaults to "[]"
	reviewOut          string     // served for the review-gate's own model launch; "" defaults to "REVIEW: PASS"
	reviewFailAttempts int        // review-gate's own gateFailAttempts: rejects the first N review calls, then passes
	reviewCalls        int        // counts review-gate launches specifically, separate from modelCall
	// assignOverride, once set by a "bd assign" call, patches every bead's
	// assignee field in subsequent `bd list --json` responses — real bd
	// reflects an assign immediately; without this a caller that reassigns
	// and then re-lists (as reconcile's reassign-then-relaunch flow does)
	// would see the stale, pre-reassignment assignee.
	assignOverride string
}

// Run dispatches by command-family method — runWhich/runBD/runGit handle
// their own sub-commands, runModelLaunch handles the coding agent's own
// launch (bead run or review-gate), and runDefault is the shared gate-step
// fallback (gateExit/gateFailAttempts) reached by anything else, including
// a bd/git sub-command none of the family methods recognize. name=="opencode"
// with args[0]!="models" deliberately falls out of the switch with no
// return, continuing to the modelCmd check below — opencode is both a
// models-discovery probe and (in tests that set modelCmd:"opencode") the
// coding agent's own launch command.
func (r *runGateRunner) Run(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
	switch name {
	case "which":
		return r.runWhich(args)
	case "opencode":
		if len(args) > 0 && args[0] == "models" {
			return tool.Result{Stdout: r.modelsOut}, nil
		}
	case "bd":
		return r.runBD(args)
	case "git":
		return r.runGit(args)
	case "gitleaks":
		return tool.Result{}, nil // clean scan by default; secret-scan tests script their own runner
	}
	if r.modelCmd != "" && name == r.modelCmd {
		return r.runModelLaunch(args)
	}
	return r.runDefault()
}

func (r *runGateRunner) runWhich(args []string) (tool.Result, error) {
	if r.missingModel != "" && slices.Contains(args, r.missingModel) {
		return tool.Result{ExitCode: 1}, errors.New("exit status 1")
	}
	if slices.Contains(r.missingWhich, args[0]) {
		return tool.Result{ExitCode: 1}, errors.New("exit status 1")
	}
	return tool.Result{}, nil // installed: on-demand probe succeeds
}

func (r *runGateRunner) runBD(args []string) (tool.Result, error) {
	if len(args) == 0 {
		return r.runDefault()
	}
	switch args[0] {
	case "list":
		beadJSON := r.beadJSON
		if beadJSON == "" {
			beadJSON = runBeadJSON
		}
		if r.assignOverride != "" {
			beadJSON = withAssigneeOverride(beadJSON, r.assignOverride)
		}
		return tool.Result{Stdout: beadJSON}, nil
	case "update":
		r.statusCalls = append(r.statusCalls, args[len(args)-1])
		return tool.Result{}, nil
	case "assign":
		r.assignOverride = args[len(args)-1]
		return tool.Result{}, nil
	case "comment":
		r.comments = append(r.comments, args[len(args)-1])
		return tool.Result{}, nil
	case "comments":
		comments := r.commentsJSON
		if comments == "" {
			comments = "[]"
		}
		return tool.Result{Stdout: comments}, nil
	case "config":
		// ensureCustomStatuses: report the custom statuses as unset on
		// config get, and accept the config set call.
		if len(args) > 1 && args[1] == "set" {
			return tool.Result{}, nil
		}
		return tool.Result{Stdout: "status.custom (not set)"}, nil
	}
	return r.runDefault()
}

func (r *runGateRunner) runGit(args []string) (tool.Result, error) {
	if len(args) == 0 {
		return r.runDefault()
	}
	switch args[0] {
	case "worktree":
		if len(args) > 1 && (args[1] == "list" || args[1] == "add") {
			return tool.Result{}, nil
		}
	case "rev-parse":
		return tool.Result{ExitCode: 1}, errors.New("exit status 1")
	case "diff":
		return tool.Result{Stdout: r.diffOut}, nil
	case "status":
		if r.noChanges {
			return tool.Result{}, nil // clean tree: gates must be skipped
		}
		if r.statusOut != "" {
			return tool.Result{Stdout: r.statusOut}, nil
		}
		return tool.Result{Stdout: " M a.go\n"}, nil // changes exist by default
	case "add":
		r.addCalls = append(r.addCalls, args)
		return tool.Result{}, nil
	case "log":
		return tool.Result{Stdout: r.logOut}, nil // "" = no commits in range: signed-commit check passes
	case "merge":
		r.mergeCalls = append(r.mergeCalls, args)
		return tool.Result{}, nil
	case "merge-tree":
		return tool.Result{}, nil // clean preflight by default
	case "ls-files":
		return tool.Result{}, nil // no unresolved conflicts by default
	}
	return r.runDefault()
}

// runModelLaunch handles name==r.modelCmd: reviewGate launches the bead's
// own model command too (same agent, different prompt) — distinguished by
// its own prompt's distinctive instruction text so it isn't treated as the
// coding agent's own run.
func (r *runGateRunner) runModelLaunch(args []string) (tool.Result, error) {
	if len(args) > 0 && strings.Contains(args[len(args)-1], "REVIEW: PASS\nor\nREVIEW: FAIL") {
		r.reviewCalls++
		if r.reviewCalls <= r.reviewFailAttempts {
			return tool.Result{Stdout: "REVIEW: FAIL: incomplete"}, nil
		}
		out := r.reviewOut
		if out == "" {
			out = "REVIEW: PASS"
		}
		return tool.Result{Stdout: out}, nil
	}
	if !slices.Contains(args, "--version") {
		r.modelCall = append(r.modelCall, args)
		return tool.Result{}, nil
	}
	return r.runDefault()
}

func (r *runGateRunner) runDefault() (tool.Result, error) {
	fail := r.gateExit != 0
	if r.modelCmd != "" {
		fail = len(r.modelCall) <= r.gateFailAttempts
	}
	if fail {
		return tool.Result{ExitCode: 1, Stderr: "step failed"}, errors.New("exit status 1")
	}
	return tool.Result{}, nil
}

func TestRunGateSuccess(t *testing.T) {
	a := newTestApp(t, &runGateRunner{})
	report, err := runGateOnly(t, a)
	if err != nil {
		t.Fatalf("runGateReport: %v; stderr: %s", err, stderr(t, a))
	}
	if !report.Success {
		t.Errorf("report = %+v, want the gate to pass", report)
	}
	if !strings.Contains(stdout(t, a), "gate passed") {
		t.Errorf("stdout = %q, want it to mention gate passed", stdout(t, a))
	}
}

func TestRunGateFailure(t *testing.T) {
	a := newTestApp(t, &runGateRunner{gateExit: 1})
	report, err := runGateOnly(t, a)
	if err != nil {
		t.Fatalf("runGateReport: %v", err)
	}
	if report.Success {
		t.Errorf("report = %+v, want the gate to fail", report)
	}
	if !strings.Contains(stdout(t, a), "gate failed") {
		t.Errorf("stdout = %q, want it to mention gate failed", stdout(t, a))
	}
}

func TestRunGateJSON(t *testing.T) {
	a := newTestApp(t, &runGateRunner{})
	a.json = true
	if _, err := runGateOnly(t, a); err != nil {
		t.Fatalf("runGateReport: %v", err)
	}
	if !strings.Contains(stdout(t, a), `"brn"`) {
		t.Errorf("stdout = %q, want JSON with brn field", stdout(t, a))
	}
}

func TestRunUnassignedBead(t *testing.T) {
	a := newTestApp(t, &runGateRunner{})
	err := a.StartRun(context.Background(), "baron-a1b2c3")
	if err == nil {
		t.Fatal("run on an unassigned bead: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not assigned") {
		t.Errorf("error = %v, want it to mention the bead is not assigned", err)
	}
}

func TestRunAgentNotInRegistry(t *testing.T) {
	a := newTestApp(t, &runGateRunner{beadJSON: runAssignedBeadJSON, missingModel: "claude"})
	err := a.StartRun(context.Background(), "baron-a1b2c3")
	if err == nil {
		t.Fatal("run with an unregistered agent not on PATH: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not in the registry") {
		t.Errorf("error = %v, want it to mention the agent isn't registered", err)
	}
}

// runOpencodeLLMBeadJSON assigns the bead to the "opencode" agent with a
// specific model in metadata. It deliberately uses the legacy "llm"
// metadata key, so this doubles as the regression test for beads assigned
// before the key was renamed to "model": they must keep running the model
// they were assigned. Assignee is always the bare agent, never a compound
// "agent/model" string.
const runOpencodeLLMBeadJSON = `[{"id":"baron-a1b2c3","title":"Implement auth","status":"assigned","priority":2,"issue_type":"task","assignee":"opencode","metadata":{"llm":"deepseek-v4-flash-free"}}]`

// TestRunResolvesAssignedModel: a bead with a bare agent assignee and a
// model in metadata must inject the model name into that agent's launch
// args, not fail with a registry error (assignee alone is a clean registry
// lookup key; the model is a separate field entirely).
func TestRunResolvesAssignedModel(t *testing.T) {
	runner := &runGateRunner{beadJSON: runOpencodeLLMBeadJSON, modelCmd: "opencode"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testOpencode())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.modelCall) != 1 {
		t.Fatalf("model calls = %d, want 1", len(runner.modelCall))
	}
	got := runner.modelCall[0]
	if len(got) != 4 || got[0] != "run" || got[1] != "--model" || got[2] != "deepseek-v4-flash-free" {
		t.Errorf("model launch args = %v, want [run --model deepseek-v4-flash-free <prompt>]", got)
	}
	if len(got) == 4 && !strings.Contains(got[3], "Implement auth") {
		t.Errorf("prompt arg = %q, want the bead prompt after --model", got[3])
	}
}

// runClaudeLLMBeadJSON assigns the bead to "claude" with model/effort
// metadata (`work assign --model claude/haiku --effort high`), again via
// the legacy "llm" key.
const runClaudeLLMBeadJSON = `[{"id":"baron-a1b2c3","title":"Implement auth","status":"assigned","priority":2,"issue_type":"task","assignee":"claude","metadata":{"llm":"haiku","effort":"high"}}]`

// TestRunResolvesNonOpencodeModelAndEffort: claude's launch args must get
// both --model <name> (appended, since its prompt is a flag value, not
// opencode's positional "run --model X" form) and --effort <level>. See
// agent.Agent.Invocation, which decides where each flag goes from the
// shape of the agent's own argument list.
func TestRunResolvesNonOpencodeModelAndEffort(t *testing.T) {
	runner := &runGateRunner{beadJSON: runClaudeLLMBeadJSON, modelCmd: "claude"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.modelCall) != 1 {
		t.Fatalf("model calls = %d, want 1", len(runner.modelCall))
	}
	got := runner.modelCall[0]
	if len(got) != 6 || got[0] != "-p" || got[2] != "--model" || got[3] != "haiku" || got[4] != "--effort" || got[5] != "high" {
		t.Errorf("model launch args = %v, want [-p <prompt> --model haiku --effort high]", got)
	}
	if !strings.Contains(got[1], "Implement auth") {
		t.Errorf("prompt arg = %q, want the bead prompt", got[1])
	}
}

// TestRunProbesModelOnDemand: a model assigned before `baron doctor` ran
// (empty registry cache) must still launch when its CLI is on PATH — the
// resolve step falls back to an on-demand probe instead of failing.
func TestRunProbesModelOnDemand(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"}
	a := newTestApp(t, runner)

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.modelCall) != 1 {
		t.Fatalf("model calls = %d, want 1 (resolved via on-demand probe)", len(runner.modelCall))
	}
	if got := runner.modelCall[0]; len(got) != 4 || got[0] != "-p" || !strings.Contains(got[1], "Implement auth") || got[2] != "--effort" || got[3] != "high" {
		t.Errorf("model launch args = %v, want [-p <prompt with bead title> --effort high]", got)
	}
	if !strings.Contains(stdout(t, a), "gate passed") {
		t.Errorf("stdout = %q, want the gate to run after the model launch", stdout(t, a))
	}
}

func TestRunModelWithoutArgs(t *testing.T) {
	a := newTestApp(t, &runGateRunner{beadJSON: runAssignedBeadJSON})
	writeAgentRegistry(t, agent.Agent{Name: "claude", Command: "claude", Status: agent.StatusActive, Backend: "subprocess"})
	err := a.StartRun(context.Background(), "baron-a1b2c3")
	if err == nil {
		t.Fatal("run with a model missing args: want error, got nil")
	}
	if !strings.Contains(err.Error(), "args") {
		t.Errorf("error = %v, want it to mention args", err)
	}
}

func TestRunLaunchesModelThenGate(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.modelCall) != 1 {
		t.Fatalf("model calls = %d, want 1", len(runner.modelCall))
	}
	if got := runner.modelCall[0]; len(got) != 2 || got[0] != "-p" || !strings.Contains(got[1], "Implement auth") {
		t.Errorf("model launch args = %v, want [-p <prompt with bead title>]", got)
	}
	if !strings.Contains(stdout(t, a), "gate passed") {
		t.Errorf("stdout = %q, want the gate to run after the model launch", stdout(t, a))
	}
}

func TestRunPrintsDiffAfterSuccess(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", diffOut: "diff --git a/x b/x\n+hello\n"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !strings.Contains(stdout(t, a), "+hello") {
		t.Errorf("stdout = %q, want the diff printed after a successful run", stdout(t, a))
	}
}

func TestRunNoDiffPrintedInJSONMode(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", diffOut: "diff --git a/x b/x\n+hello\n"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	a.json = true
	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run --json error: %v", err)
	}
	if strings.Contains(stdout(t, a), "+hello") {
		t.Errorf("stdout = %q, want no diff text in --json mode", stdout(t, a))
	}
}

func TestRunSkipsGatesWhenNoChanges(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", noChanges: true}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.modelCall) != 1 {
		t.Fatalf("model calls = %d, want 1 (the skip check runs after the model exits)", len(runner.modelCall))
	}
	out := stdout(t, a)
	if !strings.Contains(out, "gates skipped") {
		t.Errorf("stdout = %q, want the skip notice when the worktree is clean", out)
	}
	if strings.Contains(out, "gate passed") {
		t.Errorf("stdout = %q, want no gate run for a no-op prompt", out)
	}
	if len(runner.addCalls) != 0 {
		t.Errorf("git add calls = %v, want none when gates are skipped", runner.addCalls)
	}
	// The working transition happened, but the run stops before Validating.
	if len(runner.statusCalls) != 1 {
		t.Errorf("status calls = %v, want only the working transition", runner.statusCalls)
	}
}

func TestRunStagesAfterGatePass(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", statusOut: " M a.go\n?? new.go\n"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	out := stdout(t, a)
	if !strings.Contains(out, "gate passed") {
		t.Errorf("stdout = %q, want the gate to run when changes exist", out)
	}
	if !strings.Contains(out, "staged 2 file(s)") {
		t.Errorf("stdout = %q, want the staged-file count after a passing gate", out)
	}
	if len(runner.addCalls) != 1 {
		t.Fatalf("git add calls = %v, want exactly one", runner.addCalls)
	}
	if got := runner.addCalls[0]; !slices.Contains(got, "a.go") || !slices.Contains(got, "new.go") {
		t.Errorf("git add args = %v, want a.go and new.go staged", got)
	}
}

// secretFindingRunner wraps runGateRunner, making the first `gitleaks`
// invocation report a finding (writing a report to --report-path, as the
// real binary would) instead of the default clean scan.
type secretFindingRunner struct {
	*runGateRunner
	scanned bool
}

func (r *secretFindingRunner) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	if name == "gitleaks" && !r.scanned {
		r.scanned = true
		var reportPath string
		for i, a := range args {
			if a == "--report-path" && i+1 < len(args) {
				reportPath = args[i+1]
			}
		}
		if reportPath != "" {
			_ = os.WriteFile(reportPath, []byte(`[{"RuleID":"aws-key","File":"a.go","StartLine":3,"Description":"AWS key"}]`), 0o600)
		}
		return tool.Result{ExitCode: 1}, errors.New("exit status 1")
	}
	return r.runGateRunner.Run(ctx, name, args, opts)
}

func TestRunBlocksOnSecretFinding(t *testing.T) {
	inner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"}
	runner := &secretFindingRunner{runGateRunner: inner}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	code := runBRN(t, a)
	if code != ExitHuman {
		t.Fatalf("exit code = %d, want %d (ExitHuman); stdout: %s", code, ExitHuman, stdout(t, a))
	}
	if !hasAuditDetail(auditEvents(t, a), "aws-key") {
		t.Errorf("audit events = %+v, want the finding's rule ID", auditEvents(t, a))
	}
	if len(inner.modelCall) != 1 {
		t.Errorf("model calls = %d, want 1 (a secret finding must not trigger a retry)", len(inner.modelCall))
	}
}

func TestRunParksOnAgentQuestion(t *testing.T) {
	runner := &runGateRunner{
		beadJSON:     runAssignedBeadJSON,
		modelCmd:     "claude",
		commentsJSON: `[{"id":"c1","issue_id":"baron-a1b2c3","author":"opencode","text":"[ask] hangi API?"}]`,
	}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	code := runBRN(t, a)
	if code != ExitHuman {
		t.Fatalf("exit code = %d, want %d (ExitHuman); stdout: %s", code, ExitHuman, stdout(t, a))
	}
	if !slices.Contains(runner.statusCalls, "human_queue") {
		t.Errorf("status calls = %v, want the bead parked in human_queue (custom status)", runner.statusCalls)
	}
	if !hasAuditDetail(auditEvents(t, a), "ask") {
		t.Errorf("audit events = %+v, want an ask event", auditEvents(t, a))
	}
	if len(runner.addCalls) != 0 {
		t.Errorf("git add calls = %v, want none (no gate on an unanswered question)", runner.addCalls)
	}
}

func TestRunOpensPRAfterAllChecksPass(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want the ready-to-merge branch reported", stdout(t, a))
	}
	if !hasAuditDetail(auditEvents(t, a), "ready to merge") {
		t.Errorf("audit events = %+v, want the branch recorded", auditEvents(t, a))
	}
}

func TestRunBlocksOnUnsignedCommit(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"}
	runner.logOut = "deadbeef N\n" // one unsigned commit
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	code := runBRN(t, a)
	if code != ExitHuman {
		t.Fatalf("exit code = %d, want %d (ExitHuman); stdout: %s", code, ExitHuman, stdout(t, a))
	}
	if !hasAuditDetail(auditEvents(t, a), "deadbeef") {
		t.Errorf("audit events = %+v, want the unsigned commit SHA", auditEvents(t, a))
	}
	if slices.Contains(runner.statusCalls, "mergable") {
		t.Error("bead reached mergable despite an unsigned commit")
	}
}

// autoMergeConfig returns a loadConfig func with merge.auto enabled and
// requiring the "auto-merge" bd tag (which autoMergeBeadJSON's bead carries).
func autoMergeConfig(t *testing.T) func(string) (*store.Config, error) {
	t.Helper()
	return func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.Merge.Auto.Enabled = true
		cfg.Merge.Auto.RequireTags = []string{"auto-merge"}
		return cfg, nil
	}
}

const autoMergeBeadJSON = `[{"id":"baron-a1b2c3","title":"Implement auth","status":"assigned","priority":2,"issue_type":"task","assignee":"claude","tags":["auto-merge"]}]`

func TestRunAutoMergesWhenPolicySatisfied(t *testing.T) {
	runner := &runGateRunner{beadJSON: autoMergeBeadJSON, modelCmd: "claude"}
	a := newTestApp(t, runner)
	a.loadConfig = autoMergeConfig(t)
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.mergeCalls) != 1 {
		t.Fatalf("merge calls = %d, want 1 (policy satisfied)", len(runner.mergeCalls))
	}
	events := auditEvents(t, a)
	if !hasAuditDetail(events, "auto-merged") {
		t.Errorf("audit events = %+v, want the auto-merge recorded", events)
	}
	// merged is a resting terminal state, not a hop through to closed — the
	// bead stays distinguishably "merged" after an auto-merge.
	if !strings.Contains(strings.Join(runner.statusCalls, ","), "merged") {
		t.Errorf("statusCalls = %v, want the bead merged after auto-merge", runner.statusCalls)
	}
	// auto-merge is a policy decision the system made, not something a human
	// typed — the audit entry must say so (actor:manager), not collapse into
	// actor:user like a real human action. This is what makes a runaway
	// automated merge diagnosable instead of looking like someone's own
	// `baron merge` in the log.
	found := false
	for _, e := range events {
		if e.Action == "merge_auto" {
			found = true
			if e.Actor.Type != store.ActorManager {
				t.Errorf("merge_auto actor = %q, want %q", e.Actor.Type, store.ActorManager)
			}
		}
	}
	if !found {
		t.Fatalf("audit events = %+v, want a merge_auto event", events)
	}
}

func TestRunAutoMergeSkippedWithoutRequiredTag(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"}
	a := newTestApp(t, runner)
	a.loadConfig = func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.Merge.Auto.Enabled = true
		cfg.Merge.Auto.RequireTags = []string{"needs-a-tag-the-bead-does-not-have"}
		return cfg, nil
	}
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.mergeCalls) != 0 {
		t.Fatalf("merge calls = %d, want 0 (bead lacks the required tag)", len(runner.mergeCalls))
	}
	if !strings.Contains(stdout(t, a), "auto-merge policy not satisfied") {
		t.Errorf("stdout = %q, want a policy-not-satisfied message", stdout(t, a))
	}
}

func TestRunAutoMergeDisabledByDefault(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.mergeCalls) != 0 {
		t.Fatalf("merge calls = %d, want 0 (merge.auto.enabled defaults false)", len(runner.mergeCalls))
	}
}

func TestRunRetryRecoversAfterGateFailure(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", gateFailAttempts: 1}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.modelCall) != 2 {
		t.Fatalf("model calls = %d, want 2 (initial + 1 retry)", len(runner.modelCall))
	}
	if !strings.Contains(runner.modelCall[1][1], "previous gate run failed") {
		t.Errorf("retry prompt = %q, want it to include the prior gate failure", runner.modelCall[1][1])
	}
	if !hasAuditDetail(auditEvents(t, a), "retry 1/3") {
		t.Errorf("audit events = %+v, want a retry event recorded", auditEvents(t, a))
	}
	if !strings.Contains(stdout(t, a), "gate passed") {
		t.Errorf("stdout = %q, want the retry to eventually pass", stdout(t, a))
	}
}

func TestRunRetryExhaustsToHumanQueue(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", gateFailAttempts: 99}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())

	code := runBRN(t, a)
	if code != ExitGateFail {
		t.Fatalf("exit code = %d, want %d (ExitGateFail); stdout: %s", code, ExitGateFail, stdout(t, a))
	}
	if len(runner.modelCall) != 4 {
		t.Fatalf("model calls = %d, want 4 (initial + 3 retries, gate.retry_budget default 3)", len(runner.modelCall))
	}
	if !hasAuditDetail(auditEvents(t, a), "parked in human queue") {
		t.Errorf("audit events = %+v, want a human-queue event recorded", auditEvents(t, a))
	}
	if !strings.Contains(stdout(t, a), "parked in the human queue") {
		t.Errorf("stdout = %q, want a human queue message", stdout(t, a))
	}
}
