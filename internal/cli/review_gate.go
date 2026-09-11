package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// reviewGate runs one more, LLM-backed check on a bead's committed diff —
// synchronously, as a real step inside postAgentGate, right alongside the
// profile gate (format/lint/test/build) and the secret/signed-commit
// checks — before the bead is ever marked mergable.
//
// The judgment call is a persona (persona.ReviewerID, "Reviewer") —
// its own editable prompt and model, toggled on/off the same way as every
// other crew member, in the Personas tab — because an LLM verdict belongs
// in the persona system (decision 2026-08-23, agent-contract.md §4.1), not
// hardcoded Go. What stays deliberately OUTSIDE the persona is the
// invocation and the decision it feeds: this runs synchronously, blocking
// postAgentGate's own flow (Backend.Launch, not launchPersona's async tmux
// fire-and-forget), and the reviewer's own agent process is never granted
// any bd write authority (its Authority is empty on purpose) — its reply
// is a structured signal postAgentGate itself parses and acts on, the same
// separation of "an agent's signal is trustworthy input to a decision,
// never the decision-maker" that [ask]/agentAsked already established.
// That split is what makes this deterministic despite being LLM-backed: a
// persona reacting async to *->mergable (or a comment convention a
// human/reconcile pass might notice later) cannot stop tryAutoMerge from
// racing past it moments later in the very same postAgentGate call — a
// gate step has no such race, the bead simply never reaches mergable
// until this returns pass=true, the same guarantee every other gate step
// already gives.
//
// A fail is handed back to postAgentGate exactly like a profile-gate
// failure (through recordFailure, same retry budget, same outcome codes):
// the headless retry loop relaunches with a *fresh* prompt carrying the
// reviewer's reason on the very same branch/worktree (postAgentRetry —
// see run_gate.go's own retry loop, "prompt = basePrompt + gateSummary" then
// "continue"); the human-attended paths (ResumeRun/IdleResumeRun, retry
// budget forced to 0) go straight to human_queue instead, same as any
// other gate failure there — a still-attended or since-abandoned session
// gets no silent auto-relaunch, a person decides.
//
// pass=true, "" with a nil error also covers: the reviewer persona is
// missing or disabled (Enabled=false — the on/off switch), its resolved
// model has no headless launch args configured (agent.Args empty — the
// bead's own work may well have run through the TUI's interactive
// terminal, which never needed headless args at all), and "nothing to
// review" (no diff at all). None of these make the review gate a new hard
// requirement — it's a bonus when configured and enabled, same as before.
func (a *app) reviewGate(ctx context.Context, brn domain.BRN, bead store.Bead, wt domain.Worktree) (pass bool, reason string, err error) {
	personas, err := a.loadPersonas()
	if err != nil {
		return false, "", err
	}
	reviewer, ok := findPersona(personas, persona.ReviewerID)
	if !ok || !reviewer.Enabled {
		return true, "", nil
	}
	ag, ok := a.resolvePersonaAgent(agent.LoadCatalog(), reviewer)
	if !ok || len(ag.Args) == 0 {
		return true, "", nil
	}
	base, err := a.mergeBaseFor(ctx, bead)
	if err != nil {
		return false, "", err
	}
	diff, err := tool.BranchDiff(ctx, a.runner, wt.Path, base, wt.Branch)
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(diff) == "" {
		return true, "", nil
	}
	res, err := a.backend.Launch(ctx, ag, wt.Path, reviewPrompt(reviewer, bead, diff), nil)
	if err != nil {
		return false, "", err
	}
	pass, reason = parseReviewVerdict(res.Stdout)
	if pass {
		a.auditLog("review", string(brn), "review: pass")
	} else {
		a.auditLog("review", string(brn), "review: fail: "+reason)
	}
	return pass, reason, nil
}

// reviewDiffCap bounds how much diff text reviewPrompt hands the reviewer
// model — generous enough for a real bead's change, not so large that one
// oversized diff burns an outsized chunk of the review call's own context/
// cost budget. Clipped from the front (kept: the tail, where a diff's most
// recently touched, still-uncommitted-feeling hunks tend to sit) — the
// same trade-off matchOpencodeFailure makes reading its own log tail.
const reviewDiffCap = 32 << 10

// reviewPrompt builds the one-shot review call's prompt: the reviewer
// persona's own instructions (editable — this is the part a human can
// actually tune, e.g. to relax or tighten how strict the review is), the
// bead's own brief (title/description/acceptance criteria — the same
// three fields domain.Prompt hands the agent that did the work) plus its
// diff, and a fixed, non-editable, strictly-parseable verdict format tail
// (see parseReviewVerdict) — kept out of the persona's own editable Prompt
// so customizing the review criteria can never accidentally break the
// output contract postAgentGate depends on.
func reviewPrompt(reviewer persona.Persona, bead store.Bead, diff string) string {
	if len(diff) > reviewDiffCap {
		diff = diff[len(diff)-reviewDiffCap:]
	}
	var b strings.Builder
	b.WriteString(reviewer.Prompt)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Title: %s\n", bead.Title)
	if bead.Description != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", bead.Description)
	}
	if bead.AcceptanceCriteria != "" {
		fmt.Fprintf(&b, "\nAcceptance criteria:\n%s\n", bead.AcceptanceCriteria)
	}
	fmt.Fprintf(&b, "\nDiff:\n%s\n", diff)
	b.WriteString("\nEnd your reply with exactly one line, and nothing after it:\nREVIEW: PASS\nor\nREVIEW: FAIL: <one-sentence reason>")
	return b.String()
}

// reviewRetrySummary renders a review-gate rejection for the retry prompt
// the headless loop's next attempt opens on — gateFailureSummary's sibling
// for this gate step.
func reviewRetrySummary(reason string) string {
	return "The previous attempt did not pass review: " + reason +
		"\n\nAddress this before finishing again. Do not change the acceptance criteria."
}

// parseReviewVerdict reads reviewPrompt's own required output format —
// the LAST non-empty line, exactly, since a model's reasoning en route to
// the verdict routinely mentions both words ("if this were incomplete I'd
// say REVIEW: FAIL, but..."), and only the final line is the actual
// answer. Anything else — no verdict line at all, or the last non-empty
// line not matching either form — fails closed: an unparseable response
// is not evidence the work is good, and this review step exists
// specifically to catch exactly this kind of "looks plausible but I can't
// actually confirm it" case.
func parseReviewVerdict(output string) (pass bool, reason string) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range slices.Backward(lines) {
		line := strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "REVIEW: PASS" {
			return true, ""
		}
		if strings.HasPrefix(line, "REVIEW: FAIL") {
			reason = strings.TrimSpace(strings.TrimPrefix(line, "REVIEW: FAIL:"))
			if reason == "" {
				reason = "reviewer flagged this diff as incomplete"
			}
			return false, reason
		}
		break
	}
	return false, "reviewer produced no parseable verdict"
}
