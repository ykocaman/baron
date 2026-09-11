package cli

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/agent"
)

// testClaude and testOpencode are the two agents the run tests dispatch
// to, with the same flag wiring the real probe records for them.
func testClaude() agent.Agent {
	return agent.Agent{
		Name: "claude", Command: "claude", Status: agent.StatusActive, Backend: "subprocess",
		Args: []string{"-p", "{{prompt}}"}, ModelFlag: "--model", EffortFlag: "--effort",
	}
}

func testOpencode() agent.Agent {
	return agent.Agent{
		Name: "opencode", Command: "opencode", Status: agent.StatusActive, Backend: "subprocess",
		Args: []string{"run", "{{prompt}}"}, ModelFlag: "--model", EffortFlag: "--variant",
	}
}

// writeAgentRegistry seeds the machine-wide agent cache so `baron run` can
// resolve an assigned agent without a live doctor probe. newTestApp points
// XDG_CACHE_HOME at a temp dir, so this never touches the real cache.
func writeAgentRegistry(t *testing.T, agents ...agent.Agent) {
	t.Helper()
	reg := agent.NewRegistry()
	for _, a := range agents {
		reg.Add(a)
	}
	if err := agent.SaveAgents(reg); err != nil {
		t.Fatalf("write agent registry: %v", err)
	}
}

// writeCatalog seeds the machine-wide model catalog as freshly fetched, so
// consumers read it instead of re-probing.
func writeCatalog(t *testing.T, models ...agent.Model) {
	t.Helper()
	cat := agent.LoadCatalog()
	cat.Models = models
	cat.FetchedAt = time.Now()
	if err := cat.Save(); err != nil {
		t.Fatalf("write model catalog: %v", err)
	}
}

// withAssigneeOverride patches every bead's "assignee" field in a bd-list
// JSON fixture — see runGateRunner.assignOverride.
func withAssigneeOverride(beadJSON, assignee string) string {
	var beads []map[string]any
	if err := json.Unmarshal([]byte(beadJSON), &beads); err != nil {
		return beadJSON
	}
	for i := range beads {
		beads[i]["assignee"] = assignee
	}
	out, err := json.Marshal(beads)
	if err != nil {
		return beadJSON
	}
	return string(out)
}

func TestRunUnknownBead(t *testing.T) {
	a := newTestApp(t, &runGateRunner{})
	_, err := a.findBead(context.Background(), "baron-nope", "baron-nope")
	if err == nil {
		t.Fatal("run --gate for unknown bead: want error, got nil")
	}
}

func TestRunGateAuditsResult(t *testing.T) {
	a := newTestApp(t, &runGateRunner{})
	if _, err := runGateOnly(t, a); err != nil {
		t.Fatalf("run --gate error: %v", err)
	}
	events := auditEvents(t, a)
	if len(events) != 1 || events[0].Action != "gate" {
		t.Errorf("audit events = %+v, want one gate event", events)
	}
}

func TestMatchOpencodeFailure(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "rate limit",
			lines: []string{
				`timestamp=2026-08-12T18:00:00Z level=ERROR message="stream error" error.error=Rate limit exceeded, retry in 30s`,
			},
			want: "Rate limit exceeded, retry in 30s",
		},
		{
			name: "generic server error carries the ref, not the useless fixed client message",
			lines: []string{
				`timestamp=2026-07-29T21:06:39.858Z level=ERROR run=c05704ce message=failed ref=err_02b54219 error="UnknownError: UnknownError" cause="UnknownError: UnknownError\n    at <anonymous> (...)"`,
			},
			want: "server error, err_02b54219 — see ~/.local/share/opencode/log for detail",
		},
		{
			name:  "no known shape",
			lines: []string{`timestamp=2026-08-12T18:00:00Z level=INFO message=init`},
			want:  "",
		},
		{
			name: "newest-first: a later (first) line wins over an older match",
			lines: []string{
				`timestamp=2026-08-12T18:00:05Z level=ERROR ref=err_newest error="UnknownError: UnknownError"`,
				`timestamp=2026-08-12T18:00:00Z level=ERROR ref=err_older error="UnknownError: UnknownError"`,
			},
			want: "server error, err_newest — see ~/.local/share/opencode/log for detail",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchOpencodeFailure(tt.lines); got != tt.want {
				t.Errorf("matchOpencodeFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}
