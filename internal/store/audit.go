package store

import (
	"fmt"
	"time"
)

// ActorType represents who performed an action.
type ActorType string

// ActorType constants define the valid actor types.
const (
	ActorUser ActorType = "user"
	// ActorManager tags an audit event as reconciler/automation-initiated
	// rather than something a human typed — see domain.ActorManager.
	ActorManager ActorType = "manager"
	ActorAgent   ActorType = "agent"
	// ActorPersona mirrors domain.ActorPersona — a Crew Mode persona run
	// (docs/PRD/crew-mode.md), audited separately from both a human
	// (ActorUser) and a task-coding agent (ActorAgent).
	ActorPersona ActorType = "persona"
)

// AuditEvent records an action in the system.
type AuditEvent struct {
	Time   time.Time `json:"time"`
	Actor  Actor     `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"` // bead BRN or run ID
	Detail string    `json:"detail,omitempty"`
}

// Actor represents who performed the action.
type Actor struct {
	Type ActorType `json:"type"`
	Name string    `json:"name"`
}

// AuditStore manages the append-only audit log.
type AuditStore struct {
	path string
}

// NewAuditStore creates a store at the given path.
func NewAuditStore(path string) *AuditStore {
	return &AuditStore{path: path}
}

// Append adds an event to the log. Creates file if needed.
func (s *AuditStore) Append(event AuditEvent) error {
	if err := appendJSONL(s.path, event); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

// Query returns events matching the target.
func (s *AuditStore) Query(target string) ([]AuditEvent, error) {
	events, err := s.All()
	if err != nil {
		return nil, err
	}
	matches := make([]AuditEvent, 0, len(events))
	for _, e := range events {
		if e.Target == target {
			matches = append(matches, e)
		}
	}
	return matches, nil
}

// All returns all events.
func (s *AuditStore) All() ([]AuditEvent, error) {
	events, err := readJSONL[AuditEvent](s.path)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	return events, nil
}

// Path returns the log file path.
func (s *AuditStore) Path() string {
	return s.path
}
