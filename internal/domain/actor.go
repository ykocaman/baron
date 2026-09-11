package domain

// ActorType represents who performed an action.
type ActorType string

// ActorType constants define the valid actor types.
const (
	ActorUser ActorType = "user"
	// ActorManager identifies BARON's own reconciler/automation acting
	// without a human directly at the keyboard for this specific
	// transition (e.g. relaunching a stale "retry" bead, re-attempting an
	// auto-merge). Distinct from ActorUser so these transitions are
	// findable in the audit log instead of looking like something a human
	// typed — a background process silently indistinguishable from a human
	// is undiagnosable if it ever misbehaves. CanActorTransition grants it
	// the same transition rights as ActorUser (every structurally valid
	// edge); it is not a new permission tier, only a distinct audit label.
	ActorManager ActorType = "manager"
	ActorAgent   ActorType = "agent"
	// ActorPersona identifies a Crew Mode persona run (docs/PRD/crew-mode.md)
	// — a scheduled/triggered prompt with a human-granted authority to
	// manage bead state (reopen, comment, create), launched in the main
	// repo checkout rather than an isolated worktree. Distinct from
	// ActorAgent on purpose: a persona is explicitly trusted with mutation
	// the same way ActorManager is (CanActorTransition grants it the same
	// transition rights as ActorUser/ActorManager, see bead.go), but stays
	// separately labeled in the audit log for the same reason ActorManager
	// does — an automated actor indistinguishable from a human is
	// undiagnosable if it ever misbehaves. Never gets a merge bypass: see
	// CanActorTransition's explicit -> BeadStateMerged denial.
	ActorPersona ActorType = "persona"
)

// Actor represents a participant in the system.
type Actor struct {
	Type ActorType
	Name string // e.g., "alice", "claude-3.5", or "reconciler"
}
