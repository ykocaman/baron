package cli

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

func TestBdCommandPrefixesAlwaysIncludesReadOnly(t *testing.T) {
	prefixes := bdCommandPrefixes(persona.Authority{})
	want := []string{"bd show", "bd comments"}
	if !slices.Equal(prefixes, want) {
		t.Errorf("bdCommandPrefixes(empty authority) = %v, want %v (read-only, no Actions granted)", prefixes, want)
	}
}

func TestBdCommandPrefixesReopenAndClose(t *testing.T) {
	prefixes := bdCommandPrefixes(persona.Authority{Actions: []string{"reopen", "close", "comment"}})
	for _, want := range []string{"bd show", "bd comments", "bd reopen", "bd close", "bd comment"} {
		if !slices.Contains(prefixes, want) {
			t.Errorf("bdCommandPrefixes = %v, want it to contain %q", prefixes, want)
		}
	}
	if slices.Contains(prefixes, "bd create") {
		t.Errorf("bdCommandPrefixes = %v, want no bd create — Actions never declared it", prefixes)
	}
}

func TestBdCommandPrefixesUnknownActionIgnored(t *testing.T) {
	prefixes := bdCommandPrefixes(persona.Authority{Actions: []string{"nonsense"}})
	want := []string{"bd show", "bd comments"}
	if !slices.Equal(prefixes, want) {
		t.Errorf("bdCommandPrefixes(unknown action) = %v, want %v (unrecognized actions grant nothing)", prefixes, want)
	}
}

// loadTestCatalog returns an empty-but-non-nil catalog: launchPersona's
// tier-resolution path (unused when Model.Agent is set directly, as every
// test above does) still needs a non-nil *agent.Catalog to range over.
// XDG_CACHE_HOME is redirected to a temp dir by newTestApp, so this never
// reads the developer's real machine-wide catalog.
func loadTestCatalog(t *testing.T) *agent.Catalog {
	t.Helper()
	return agent.LoadCatalog()
}

func TestAllBuiltinPersonasFireIndividually(t *testing.T) {
	runner := &tmuxPersonaRunner{tmuxOut: "tmux 3.4"}
	a := newTestApp(t, runner)
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) {
		return persona.LoadAll("", "")
	}

	// Seed test beads with branch names
	_, _ = a.beads.Create(context.Background(), store.CreateBeadParams{Title: "Fix bug", Description: "description", Acceptance: "bug", Priority: store.Priority("P1"), Tier: agent.TierStandard})
	_, _ = a.beads.Create(context.Background(), store.CreateBeadParams{Title: "Add feature", Description: "description", Acceptance: "feature", Priority: store.Priority("P2"), Tier: agent.TierStandard})

	personaIDs := []string{"clean-code", "closer", "reviewer", "qa-chromium", "red-team"}
	for _, id := range personaIDs {
		err := a.FireNow(context.Background(), id)
		if err != nil {
			t.Fatalf("FireNow(%q) failed: %v", id, err)
		}
		// Verify tmux new-window call was made for this persona
		windowName := "persona-" + id
		found := false
		for _, c := range runner.calls {
			if len(c) >= 4 && c[0] == "tmux" && c[1] == "new-window" && slices.Contains(c, windowName) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("FireNow(%q): tmux window %q not found in calls", id, windowName)
		}

		// Verify audit activity was recorded
		events, err := a.PersonaActivity(id)
		if err != nil {
			t.Fatalf("PersonaActivity(%q) err: %v", id, err)
		}
		if len(events) == 0 {
			t.Errorf("PersonaActivity(%q): no audit events recorded after FireNow", id)
		}
	}
}

func TestLiveRunAll5PersonasInTestDir(t *testing.T) {
	targetDir := "/Users/yusuf/Projects/test"
	runner := tool.NewRunner()
	var buf strings.Builder
	a := newApp(&buf, &buf, runner)
	a.dir = targetDir
	ctx := context.Background()
	personas := []string{"clean-code", "closer", "reviewer", "qa-chromium", "red-team"}
	for _, id := range personas {
		err := a.FireNow(ctx, id)
		if err != nil {
			t.Logf("FireNow(%s): %v", id, err)
		} else {
			t.Logf("Successfully fired %s", id)
		}
	}

	time.Sleep(4 * time.Second)

	for _, id := range personas {
		out, err := a.PersonaOutput(ctx, id)
		t.Logf("\n=== PERSONA OUTPUT [%s] (err=%v) ===\n%s\n", id, err, strings.TrimSpace(out))
	}
}
