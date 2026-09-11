package cli

import (
	"context"
	"fmt"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

func toDomainState(s store.BeadStatus) domain.BeadState {
	return domain.BeadState(s)
}

func toStoreStatus(s domain.BeadState) store.BeadStatus {
	return store.BeadStatus(s)
}

func resolveDomainState(b store.Bead) domain.BeadState {
	return b.DomainState()
}

func (a *app) transitionStatus(ctx context.Context, id string, from, to domain.BeadState, actor domain.Actor) error {
	if from == to {
		return nil
	}

	allBeads, err := a.beads.List(ctx)
	if err != nil {
		return fmt.Errorf("list beads: %w", err)
	}

	domainBeads := make([]*domain.Bead, len(allBeads))
	for i := range allBeads {
		domainBeads[i] = storeBeadToDomain(&allBeads[i])
	}

	var bead *domain.Bead
	for i := range domainBeads {
		if string(domainBeads[i].BRN) == id {
			bead = domainBeads[i]
			break
		}
	}
	if bead == nil {
		return fmt.Errorf("bead %s not found", id)
	}

	sm := domain.NewBeadStateMachine()

	if err := validateBlockTransition(sm, bead, domainBeads, id, from, to); err != nil {
		return err
	}

	if !sm.CanActorTransition(from, to, actor) {
		return fmt.Errorf("invalid bead transition %s -> %s for actor %s", from, to, actor.Type)
	}
	if err := a.beads.Status(ctx, id, toStoreStatus(to)); err != nil {
		return err
	}
	a.recordStateSnapshot(id, to)
	return nil
}

func validateBlockTransition(sm *domain.BeadStateMachine, bead *domain.Bead, all []*domain.Bead, id string, from, to domain.BeadState) error {
	hasBlockers := sm.HasLiveBlockers(bead, all)
	if to == domain.BeadStateBlocked && !hasBlockers {
		return fmt.Errorf("cannot set blocked: bead %s has no live blockers", id)
	}
	if from == domain.BeadStateBlocked && to != domain.BeadStateBlocked && hasBlockers {
		return fmt.Errorf("cannot unblock: bead %s still has live blockers", id)
	}
	return nil
}

func storeBeadToDomain(b *store.Bead) *domain.Bead {
	deps := make([]domain.DepLink, len(b.Dependencies))
	for i, d := range b.Dependencies {
		deps[i] = domain.DepLink{
			IssueID:     d.IssueID,
			DependsOnID: d.DependsOnID,
			Type:        d.Type,
		}
	}
	return &domain.Bead{
		ID:           b.ID,
		BRN:          b.BRN,
		Title:        b.Title,
		Description:  b.Description,
		State:        domain.BeadState(b.Status),
		Assignee:     b.Assignee,
		Tags:         b.Tags,
		Dependencies: deps,
		CreatedAt:    b.CreatedAt,
		UpdatedAt:    b.UpdatedAt,
	}
}

func (a *app) systemActor() domain.Actor {
	return domain.Actor{Type: domain.ActorUser, Name: currentUser()}
}

// managerActor identifies a transition BARON's own automation made, not a
// direct human action — auto-merge policy evaluation (tryAutoMerge) and the
// reconciler both use this, never systemActor, even though both currently
// run synchronously inside a human-triggered `baron run`/TUI process: the
// decision itself ("policy says merge this" / "this bead looks stale, land
// it on retry") was the system's, not the human's, and the audit trail
// should say so.
func (a *app) managerActor() domain.Actor {
	return domain.Actor{Type: domain.ActorManager, Name: "baron"}
}

// storeActor converts a domain.Actor to the audit log's own Actor type
// (store deliberately doesn't import domain, so this is a small manual
// bridge; the two ActorType enums are kept string-identical on purpose).
func storeActor(a domain.Actor) store.Actor {
	return store.Actor{Type: store.ActorType(a.Type), Name: a.Name}
}
