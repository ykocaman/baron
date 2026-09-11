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

// mergeCloseGate runs one more, LLM-backed check right after a merge lands
// — reviewer's own counterpart on the other side of the merge, same
// shape and same reasoning (see reviewGate's own doc comment): the
// judgment call is a persona (persona.MergeCloserID, "Closer"), but
// the invocation is synchronous (Backend.Launch, not launchPersona's async
// tmux fire-and-forget) and the persona's own agent process is never
// granted bd write authority (Authority is empty) — its reply is a
// structured OK/REOPEN signal this function itself parses and acts on,
// closing or reopening the bead with BARON's own trusted code
// (transitionStatus, actor:system) rather than trusting the persona's own
// process to call `bd close`/`bd reopen` directly. That split is what lets
// this actually close beads out: merged is otherwise a resting state
// nothing auto-advances (see MergeCloserID's own doc comment).
//
// Called fire-and-forget from runMerge/tryAutoMerge after a merge has
// already landed and the bead is already domain.BeadStateMerged — nothing
// here can block or undo the merge itself, only decide what happens next.
// Errors are non-fatal by design (the caller just warns and moves on, same
// as the worktree-cleanup/tryAdvanceParent steps beside it): a merge
// having already succeeded is real, tracked-git-history work, and this is
// strictly a bonus follow-up check, never a new hard requirement.
//
// Does nothing (no launch, no transition) when: the persona is missing or
// disabled, its resolved model has no headless launch args configured, or
// beforeSHA is empty (the caller's own HeadSHA lookup failed before the
// merge ran — nothing to diff).
func (a *app) mergeCloseGate(ctx context.Context, brn domain.BRN, bead store.Bead, dir, beforeSHA string) error {
	if beforeSHA == "" {
		return nil
	}
	personas, err := a.loadPersonas()
	if err != nil {
		return err
	}
	closer, ok := findPersona(personas, persona.MergeCloserID)
	if !ok || !closer.Enabled {
		return nil
	}
	ag, ok := a.resolvePersonaAgent(agent.LoadCatalog(), closer)
	if !ok || len(ag.Args) == 0 {
		return nil
	}
	diff, err := tool.RangeDiff(ctx, a.runner, dir, beforeSHA, "HEAD")
	if err != nil {
		return fmt.Errorf("closer: diffing the merge: %w", err)
	}

	verdict, reason := a.runMergeCloser(ctx, ag, dir, mergeClosePrompt(closer, bead, diff))
	return a.applyMergeCloseVerdict(ctx, brn, verdict, reason)
}

func (a *app) runMergeCloser(ctx context.Context, ag agent.Agent, dir, prompt string) (mergeCloseVerdict, string) {
	for attempt := 1; attempt <= 2; attempt++ {
		res, err := a.backend.Launch(ctx, ag, dir, prompt, nil)
		if err != nil {
			a.warn("closer: attempt %d/2: %v", attempt, err)
			continue
		}
		verdict, reason := parseMergeCloseVerdict(res.Stdout)
		if verdict != mergeCloseUnparsed {
			return verdict, reason
		}
	}
	return mergeCloseUnparsed, ""
}

// mergeCloseAuditAction is the auditLog action name for every verdict this
// function reaches.
const mergeCloseAuditAction = "merge_close"

func (a *app) applyMergeCloseVerdict(ctx context.Context, brn domain.BRN, verdict mergeCloseVerdict, reason string) error {
	id := a.idOf(brn)
	switch verdict {
	case mergeCloseOK:
		if err := a.transitionStatus(ctx, id, domain.BeadStateMerged, domain.BeadStateClosed, a.systemActor()); err != nil {
			return err
		}
		a.auditLog(mergeCloseAuditAction, string(brn), "closed: merge holds")
	case mergeCloseReopen:
		if err := a.transitionStatus(ctx, id, domain.BeadStateMerged, domain.BeadStateOpen, a.systemActor()); err != nil {
			return err
		}
		a.auditLog(mergeCloseAuditAction, string(brn), "reopened: "+reason)
	default:
		a.auditLog(mergeCloseAuditAction, string(brn), "no parseable verdict after retries — left merged for a human")
	}
	return nil
}

// mergeCloseDiffCap mirrors reviewDiffCap (internal/cli/review_gate.go):
// generous enough for a real merge's worth of change, not so large that
// one oversized diff burns an outsized chunk of the closer call's own
// context/cost budget. Clipped from the front (kept: the tail) for the
// same reason reviewPrompt does.
const mergeCloseDiffCap = 32 << 10

// mergeClosePrompt builds the one-shot merge-close call's prompt: the
// closer persona's own instructions (editable), the bead's own brief
// (title/description/acceptance criteria), the merge's diff, and a fixed,
// non-editable, strictly-parseable verdict format tail (see
// parseMergeCloseVerdict) — kept out of the persona's own editable Prompt
// so customizing the check criteria can never accidentally break the
// output contract mergeCloseGate depends on.
func mergeClosePrompt(closer persona.Persona, bead store.Bead, diff string) string {
	if len(diff) > mergeCloseDiffCap {
		diff = diff[len(diff)-mergeCloseDiffCap:]
	}
	var b strings.Builder
	b.WriteString(closer.Prompt)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Title: %s\n", bead.Title)
	if bead.Description != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", bead.Description)
	}
	if bead.AcceptanceCriteria != "" {
		fmt.Fprintf(&b, "\nAcceptance criteria:\n%s\n", bead.AcceptanceCriteria)
	}
	fmt.Fprintf(&b, "\nMerged diff:\n%s\n", diff)
	b.WriteString("\nEnd your reply with exactly one line, and nothing after it:\nCLOSE: OK\nor\nCLOSE: REOPEN: <one-sentence reason>")
	return b.String()
}

// mergeCloseVerdict is parseMergeCloseVerdict's own result: which of the
// two things mergeCloseGate should do next, or neither.
type mergeCloseVerdict int

const (
	mergeCloseUnparsed mergeCloseVerdict = iota
	mergeCloseOK
	mergeCloseReopen
)

// parseMergeCloseVerdict reads mergeClosePrompt's own required output
// format — the LAST non-empty line, exactly, since a model's reasoning en
// route to the verdict routinely mentions both words ("if this were
// incomplete I'd say CLOSE: REOPEN, but..."), and only the final line is
// the actual answer (see parseReviewVerdict's own doc comment — same
// reasoning). Anything else — no verdict line at all, or the last
// non-empty line not matching either form — fails closed as
// mergeCloseUnparsed: an unparseable response is not evidence the merge is
// fine, and mergeCloseGate's own retry loop exists specifically to give a
// hung or malformed reply one more chance before giving up and leaving the
// bead merged for a human.
func parseMergeCloseVerdict(output string) (verdict mergeCloseVerdict, reason string) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range slices.Backward(lines) {
		line := strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "CLOSE: OK" {
			return mergeCloseOK, ""
		}
		if strings.HasPrefix(line, "CLOSE: REOPEN") {
			reason = strings.TrimSpace(strings.TrimPrefix(line, "CLOSE: REOPEN:"))
			if reason == "" {
				reason = "closer flagged this merge as incomplete"
			}
			return mergeCloseReopen, reason
		}
		break
	}
	return mergeCloseUnparsed, ""
}
