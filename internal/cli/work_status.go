package cli

import (
	"context"
	"fmt"

	"github.com/baron-cli/baron/internal/domain"
)

// statusResult is the JSON shape of `baron work status`, and the typed
// result ChangeStatus returns to every caller (CLI, and eventually the TUI
// and reconciler) so each can report it however fits — text, JSON, a toast.
type statusResult struct {
	BRN  domain.BRN `json:"brn"`
	From string     `json:"from"`
	To   string     `json:"to"`
}

// ChangeStatus transitions brn to the target status, cobra-free: no arg
// parsing, no --json branching, no printing. "assigned" is rejected here
// (not just at the CLI's arg-parsing layer) because it isn't a real bd
// status column — bdStatus maps it to "open", and DomainState only reads a
// bead back as assigned when its assignee field is non-empty (see
// store.Bead.DomainState). Writing status=assigned directly would report
// success while leaving the bead exactly as it was, assignee untouched —
// `work assign` is the only path that actually produces the assigned state.
// This guard must hold for every caller, not just ones that went through
// cobra's usage-error formatting.
func (a *app) ChangeStatus(ctx context.Context, brn domain.BRN, to domain.BeadState, actor domain.Actor) (statusResult, error) {
	if to == domain.BeadStateAssigned {
		return statusResult{}, fmt.Errorf("cannot set status to assigned directly: bd has no assigned status of its own, it's derived from an assignee — use work assign (%s) instead", brn)
	}
	b, err := a.findBead(ctx, brn, string(brn))
	if err != nil {
		return statusResult{}, err
	}
	from := resolveDomainState(b)
	if err := a.transitionStatus(ctx, a.idOf(brn), from, to, actor); err != nil {
		return statusResult{}, err
	}
	if to == domain.BeadStateOpen && (from == domain.BeadStateAssigned || from == domain.BeadStateRetry) {
		// bd's own status is already "open" for an assigned bead (assigned is
		// derived from open + a non-empty assignee, not a real bd status —
		// see store.Bead.DomainState), so the transition above alone is a
		// no-op at the bd level: without also clearing the assignee, the
		// bead would read right back as "assigned" the instant anything
		// re-lists it. Clearing it (and the stale model metadata that went
		// with it) is what makes this manual reset send the bead through
		// fresh tier resolution instead of just sitting there relabeled.
		id := a.idOf(brn)
		if err := a.beads.Assign(ctx, id, ""); err != nil {
			return statusResult{}, err
		}
		empty := ""
		if err := a.beads.SetModelMetadata(ctx, id, &empty, nil); err != nil {
			return statusResult{}, err
		}
	}
	a.auditLogAs(storeActor(actor), "status", string(brn), fmt.Sprintf("%s -> %s", from, to))
	if to == domain.BeadStateClosed {
		a.killBeadWindow(ctx, brn)
	}
	return statusResult{BRN: brn, From: string(from), To: string(to)}, nil
}
