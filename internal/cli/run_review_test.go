package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
)

// enabledReviewer is the "reviewer" persona (persona.ReviewerID),
// enabled and pointed straight at the "claude" test fixture agent — the
// review gate skips entirely (reviewCalls stays 0) unless a test opts in
// via this, matching newTestApp's own loadPersonas default (nil, nil).
func enabledReviewer() []persona.Persona {
	return []persona.Persona{{
		ID: persona.ReviewerID, Name: "Reviewer",
		Prompt:  "Review this diff.",
		Model:   persona.Model{Agent: "claude"},
		Enabled: true,
	}}
}

// TestReviewGateRejectionRetriesWithFreshPrompt: a review-gate rejection
// is handled exactly like a profile-gate rejection — postAgentRetry, the
// headless loop relaunches with a fresh prompt on the same worktree/branch
// carrying the reviewer's own reason, and a later attempt that passes
// review still reaches mergable.
func TestReviewGateRejectionRetriesWithFreshPrompt(t *testing.T) {
	runner := &runGateRunner{
		beadJSON: runAssignedBeadJSON, modelCmd: "claude",
		diffOut: "diff --git a/x b/x\n+hello\n", reviewFailAttempts: 1,
	}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) { return enabledReviewer(), nil }

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(runner.modelCall) != 2 {
		t.Fatalf("coding-agent model calls = %d, want 2 (initial + 1 retry after the review rejection)", len(runner.modelCall))
	}
	if !strings.Contains(runner.modelCall[1][1], "did not pass review") || !strings.Contains(runner.modelCall[1][1], "incomplete") {
		t.Errorf("retry prompt = %q, want it to carry the reviewer's own reason", runner.modelCall[1][1])
	}
	if runner.reviewCalls != 2 {
		t.Fatalf("review calls = %d, want 2 (rejected once, then passed)", runner.reviewCalls)
	}
	if !hasAuditDetail(auditEvents(t, a), "review: fail: incomplete") {
		t.Errorf("audit events = %+v, want the review rejection recorded", auditEvents(t, a))
	}
	if !strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want the retry to eventually pass review and reach mergable", stdout(t, a))
	}
}

// TestReviewGateExhaustsToHumanQueue: a review that keeps rejecting the
// work exhausts the same retry budget a profile-gate failure would, and
// parks in human_queue rather than looping forever.
func TestReviewGateExhaustsToHumanQueue(t *testing.T) {
	runner := &runGateRunner{
		beadJSON: runAssignedBeadJSON, modelCmd: "claude",
		diffOut: "diff --git a/x b/x\n+hello\n", reviewFailAttempts: 99,
	}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) { return enabledReviewer(), nil }

	code := runBRN(t, a)
	if code != ExitGateFail {
		t.Fatalf("exit code = %d, want %d (ExitGateFail); stdout: %s", code, ExitGateFail, stdout(t, a))
	}
	if len(runner.modelCall) != 4 {
		t.Fatalf("coding-agent model calls = %d, want 4 (initial + 3 retries, gate.retry_budget default 3)", len(runner.modelCall))
	}
	if !hasAuditDetail(auditEvents(t, a), "parked in human queue") {
		t.Errorf("audit events = %+v, want a human-queue event recorded", auditEvents(t, a))
	}
}

// TestReviewGateSkippedWhenNoHeadlessArgsConfigured: a bead's own work may
// well have run through the TUI's interactive terminal (ResumeRun's own
// path — domain.InteractiveCommand, never Backend.Launch), whose agent
// invocation never needed headless launch args at all — the review gate
// must degrade to "skip, pass" rather than block every such merge on a
// config gap this step didn't cause. Modeled on ResumeRun/IdleResumeRun
// (not StartRun): those never launch the coding agent themselves, so an
// Args-less registry entry only ever reaches reviewGate's own check, not
// the coding-agent launch that headless runs would need Args for too.
func TestReviewGateSkippedWhenNoHeadlessArgsConfigured(t *testing.T) {
	runner := &runGateRunner{beadJSON: runWorkingBeadJSON, diffOut: "diff --git a/x b/x\n+hello\n"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, agent.Agent{
		Name: "claude", Command: "claude", Status: agent.StatusActive, Backend: "subprocess",
		// No Args: never configured for headless launch.
	})
	a.loadPersonas = func() ([]persona.Persona, error) { return enabledReviewer(), nil }

	if err := a.ResumeRun(context.Background(), domain.BRN("baron-a1b2c3")); err != nil {
		t.Fatalf("resume error: %v", err)
	}
	if runner.reviewCalls != 0 {
		t.Errorf("review calls = %d, want 0 — no headless args means the review gate must skip, not fail", runner.reviewCalls)
	}
	if !strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want the run to still reach mergable without a review", stdout(t, a))
	}
}

// TestReviewGateSkippedWhenNoDiff: nothing to review has nothing to
// verify — this is the same "no unstaged changes and no new commits"
// no-op every other test without diffOut implicitly already exercises
// (reviewCalls stays 0 throughout this whole file's other tests), made
// explicit here.
func TestReviewGateSkippedWhenNoDiff(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"} // diffOut unset: ""
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) { return enabledReviewer(), nil }

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if runner.reviewCalls != 0 {
		t.Errorf("review calls = %d, want 0 — nothing to review", runner.reviewCalls)
	}
}

// TestReviewGateSkippedWhenNotConfigured: newTestApp's own loadPersonas
// default (nil, nil) — no "reviewer" persona at all — is exactly the
// same as a real project that has never enabled it: the review gate must
// stay off, not error.
func TestReviewGateSkippedWhenNotConfigured(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", diffOut: "diff --git a/x b/x\n+hello\n"}
	a := newTestApp(t, runner) // loadPersonas untouched: nil, nil

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if runner.reviewCalls != 0 {
		t.Errorf("review calls = %d, want 0 — no reviewer persona configured", runner.reviewCalls)
	}
	if !strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want the run to still reach mergable without a reviewer configured", stdout(t, a))
	}
}

// TestReviewGateSkippedWhenDisabled: the reviewer persona exists but its
// Enabled flag is off — the same on/off toggle every other persona has
// (space in the Personas tab) must genuinely disable the review gate.
func TestReviewGateSkippedWhenDisabled(t *testing.T) {
	runner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", diffOut: "diff --git a/x b/x\n+hello\n"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) {
		p := enabledReviewer()
		p[0].Enabled = false
		return p, nil
	}

	if err := a.StartRun(context.Background(), "baron-a1b2c3"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if runner.reviewCalls != 0 {
		t.Errorf("review calls = %d, want 0 — reviewer persona is disabled", runner.reviewCalls)
	}
}

// runWorkingBeadJSON is a bead already in "working" (bd's "in_progress")
// with an assignee, matching the state the TUI's embedded terminal leaves a
// bead in while its agent session is live — what `run --resume` (see
// terminal.go's agentExitMsg) fires against once that session's process
// exits.
const runWorkingBeadJSON = `[{"id":"baron-a1b2c3","title":"Implement auth","status":"in_progress","priority":2,"issue_type":"task","assignee":"claude"}]`

// TestRunResumeMarksReadyForMergeAfterGatePass: --resume runs the same
// post-agent pipeline as the headless retry loop (validate -> gate ->
// ready-to-merge), just without ever launching a model — the agent already
// ran, interactively, outside baron's own launch.
func TestRunResumeMarksReadyForMergeAfterGatePass(t *testing.T) {
	runner := &runGateRunner{beadJSON: runWorkingBeadJSON}
	a := newTestApp(t, runner)

	if err := a.ResumeRun(context.Background(), domain.BRN("baron-a1b2c3")); err != nil {
		t.Fatalf("run --resume error: %v", err)
	}
	if !strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want the ready-to-merge branch reported", stdout(t, a))
	}
	if len(runner.modelCall) != 0 {
		t.Errorf("model calls = %v, want none — --resume must not launch an agent", runner.modelCall)
	}
}

// TestRunResumeGoesStraightToHumanQueueOnGateFailure: --resume forces the
// retry budget to 0 — a still-focused human was driving the session
// directly, so a gate failure parks the bead for a person to decide,
// instead of a background loop silently relaunching a new session.
func TestRunResumeGoesStraightToHumanQueueOnGateFailure(t *testing.T) {
	runner := &runGateRunner{beadJSON: runWorkingBeadJSON, gateExit: 1}
	a := newTestApp(t, runner)

	code := runResumeBRN(t, a, "baron-a1b2c3")
	if code != ExitHuman {
		t.Fatalf("exit code = %d, want %d (ExitHuman); stdout: %s", code, ExitHuman, stdout(t, a))
	}
	if !slices.Contains(runner.statusCalls, "human_queue") {
		t.Errorf("status calls = %v, want the bead parked in human_queue directly (no retry state in between)", runner.statusCalls)
	}
	if slices.Contains(runner.statusCalls, "retry") {
		t.Errorf("status calls = %v, want no retry status — --resume never retries", runner.statusCalls)
	}
}

// TestRunResumeParksOnAgentQuestion: the [ask]-comment convention works the
// same regardless of how the agent ran.
func TestRunResumeParksOnAgentQuestion(t *testing.T) {
	runner := &runGateRunner{
		beadJSON:     runWorkingBeadJSON,
		commentsJSON: `[{"id":"c1","issue_id":"baron-a1b2c3","author":"claude","text":"[ask] which API?"}]`,
	}
	a := newTestApp(t, runner)

	code := runResumeBRN(t, a, "baron-a1b2c3")
	if code != ExitHuman {
		t.Fatalf("exit code = %d, want %d (ExitHuman); stdout: %s", code, ExitHuman, stdout(t, a))
	}
	if len(runner.addCalls) != 0 {
		t.Errorf("git add calls = %v, want none (no gate on an unanswered question)", runner.addCalls)
	}
}

// TestRunResumeNoChangesSkipsGate: an interactive session that produced no
// diff (a pure conversation, say) has nothing to validate — same as the
// headless path, the bead is left as-is rather than blocked or parked.
func TestRunResumeNoChangesSkipsGate(t *testing.T) {
	runner := &runGateRunner{beadJSON: runWorkingBeadJSON, noChanges: true}
	a := newTestApp(t, runner)

	if err := a.ResumeRun(context.Background(), domain.BRN("baron-a1b2c3")); err != nil {
		t.Fatalf("run --resume error: %v", err)
	}
	if !strings.Contains(stdout(t, a), "gates skipped") {
		t.Errorf("stdout = %q, want the no-changes message", stdout(t, a))
	}
	if slices.Contains(runner.statusCalls, "human_queue") {
		t.Errorf("status calls = %v, want no human-queue transition for a no-op session", runner.statusCalls)
	}
}

// TestIdleResumeNoChangesParksHumanQueue: IdleResumeRun's one deliberate
// difference from ResumeRun (see postAgentGate's parkIfNoChange doc
// comment) — a real process exit producing no diff is a legitimate no-op
// (TestRunResumeNoChangesSkipsGate, above), but an idle-detected trigger
// finding the exact same "nothing changed" means an agent that went quiet
// without doing (or asking about) anything, which is exactly the case a
// human needs to notice rather than have silently stay "working" forever.
func TestIdleResumeNoChangesParksHumanQueue(t *testing.T) {
	runner := &runGateRunner{beadJSON: runWorkingBeadJSON, noChanges: true}
	a := newTestApp(t, runner)

	code := runIdleResumeBRN(t, a, "baron-a1b2c3")
	if code != ExitHuman {
		t.Fatalf("exit code = %d, want %d (ExitHuman); stdout: %s", code, ExitHuman, stdout(t, a))
	}
	if !slices.Contains(runner.statusCalls, "human_queue") {
		t.Errorf("status calls = %v, want the idle no-op bead parked in human_queue", runner.statusCalls)
	}
	if !hasAuditDetail(auditEvents(t, a), "idle-detected") {
		t.Errorf("audit events = %+v, want the idle trigger tagged in the detail", auditEvents(t, a))
	}
}

// TestIdleResumeWithChangesValidatesLikeResume: when there IS a diff to
// validate, idle-triggered and exit-triggered resume behave identically —
// parkIfNoChange only changes the "nothing happened at all" branch.
func TestIdleResumeWithChangesValidatesLikeResume(t *testing.T) {
	runner := &runGateRunner{beadJSON: runWorkingBeadJSON}
	a := newTestApp(t, runner)

	if err := a.IdleResumeRun(context.Background(), domain.BRN("baron-a1b2c3")); err != nil {
		t.Fatalf("idle resume error: %v", err)
	}
	if !strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want the ready-to-merge branch reported", stdout(t, a))
	}
	if len(runner.modelCall) != 0 {
		t.Errorf("model calls = %v, want none — idle resume must not launch an agent", runner.modelCall)
	}
}

// TestIdleResumeParksOnAgentQuestion: the [ask] fast path fires identically
// regardless of trigger — an idle-detected check must not wait for or
// require anything beyond the existing convention.
func TestIdleResumeParksOnAgentQuestion(t *testing.T) {
	runner := &runGateRunner{
		beadJSON:     runWorkingBeadJSON,
		commentsJSON: `[{"id":"c1","issue_id":"baron-a1b2c3","author":"claude","text":"[ask] which API?"}]`,
	}
	a := newTestApp(t, runner)

	code := runIdleResumeBRN(t, a, "baron-a1b2c3")
	if code != ExitHuman {
		t.Fatalf("exit code = %d, want %d (ExitHuman); stdout: %s", code, ExitHuman, stdout(t, a))
	}
	if len(runner.addCalls) != 0 {
		t.Errorf("git add calls = %v, want none (no gate on an unanswered question)", runner.addCalls)
	}
}
