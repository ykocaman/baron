package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// opencodeVerboseFixture is a trimmed real sample of `opencode models
// --verbose` output: one model with reasoning variants, one without, and a
// leading/trailing model to prove the parser doesn't grab a neighbour's
// block.
const opencodeVerboseFixture = `opencode/big-pickle
{
  "id": "big-pickle",
  "providerID": "opencode",
  "variants": {}
}
opencode-go/deepseek-v4-flash
{
  "id": "deepseek-v4-flash",
  "providerID": "opencode-go",
  "family": "deepseek-flash",
  "variants": {
    "low": {
      "reasoningEffort": "low"
    },
    "high": {
      "reasoningEffort": "high"
    },
    "max": {
      "reasoningEffort": "max"
    }
  }
}
opencode-go/glm-5.1
{
  "id": "glm-5.1",
  "providerID": "opencode-go",
  "variants": {
    "minimal": {},
    "low": {},
    "medium": {},
    "high": {}
  }
}
`

// runnerFunc adapts a function to tool.Runner.
type runnerFunc func(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error)

func (f runnerFunc) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	return f(ctx, name, args, opts)
}

// TestParseOpencodeModels: one pass over `opencode models --verbose` yields
// every model with its own variant keys. Reading the whole catalog once is
// what lets the assign picker offer effort levels without a second
// subprocess per selection.
func TestParseOpencodeModels(t *testing.T) {
	entries, err := parseOpencodeModels(opencodeVerboseFixture)
	if err != nil {
		t.Fatalf("parseOpencodeModels: %v", err)
	}
	got := map[string][]string{}
	for _, e := range entries {
		got[e.id] = e.variants
	}
	if len(got) != 3 {
		t.Fatalf("parsed %d models, want 3: %+v", len(got), got)
	}
	if v := got["opencode/big-pickle"]; len(v) != 0 {
		t.Errorf("big-pickle variants = %v, want none (\"variants\": {})", v)
	}
	if v := got["opencode-go/deepseek-v4-flash"]; !slices.Equal(v, []string{"high", "low", "max"}) {
		t.Errorf("deepseek variants = %v, want [high low max] sorted", v)
	}
	if v := got["opencode-go/glm-5.1"]; !slices.Equal(v, []string{"high", "low", "medium", "minimal"}) {
		t.Errorf("glm variants = %v, want [high low medium minimal] sorted", v)
	}
}

// TestOpencodeCatalogCarriesAgentProvenance: an opencode model ID names a
// provider ("opencode-go/..."), not the CLI that runs it, so the catalog
// entry has to record the agent explicitly — a prefix split can't recover
// it.
func TestOpencodeCatalogCarriesAgentProvenance(t *testing.T) {
	calls := 0
	runner := runnerFunc(func(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
		calls++
		if name != "opencode" || !slices.Equal(args, []string{"models", "--verbose"}) {
			t.Fatalf("run %s %v, want opencode models --verbose", name, args)
		}
		return tool.Result{Stdout: opencodeVerboseFixture}, nil
	})
	models, err := opencodeCatalog(context.Background(), runner, Agent{Name: "opencode", Command: "opencode"})
	if err != nil {
		t.Fatalf("opencodeCatalog: %v", err)
	}
	if calls != 1 {
		t.Errorf("opencode spawned %d times, want exactly 1 for the whole catalog", calls)
	}
	for _, m := range models {
		if m.Agent != "opencode" {
			t.Errorf("model %+v: agent = %q, want opencode", m, m.Agent)
		}
		if m.Name != m.ID {
			t.Errorf("model %+v: opencode takes its own catalog ID on --model verbatim", m)
		}
	}
}

// TestBuildCatalogFallsBackWhenOpencodeFails: a failed catalog lookup must
// still leave opencode assignable under its own name (it then runs its
// configured default model), rather than dropping it from the picker.
func TestBuildCatalogFallsBackWhenOpencodeFails(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	runner := runnerFunc(func(context.Context, string, []string, tool.Options) (tool.Result, error) {
		return tool.Result{}, errors.New("opencode exploded")
	})
	reg := NewRegistry()
	reg.Add(Agent{Name: "opencode", Command: "opencode", Status: StatusActive})
	cat := BuildCatalog(context.Background(), runner, reg)
	if len(cat.Models) != 1 || cat.Models[0].ID != "opencode" {
		t.Fatalf("catalog = %+v, want a single bare opencode entry", cat.Models)
	}
}

// TestBuildCatalogClaudeModels: an agent with a fixed model list
// contributes one entry per model, each carrying the CLI's effort levels,
// so the assign form needs no second lookup.
func TestBuildCatalogClaudeModels(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	runner := runnerFunc(func(context.Context, string, []string, tool.Options) (tool.Result, error) {
		return tool.Result{}, errors.New("no subprocess expected")
	})
	reg := NewRegistry()
	reg.Add(Agent{Name: "claude", Command: "claude", Status: StatusActive})
	cat := BuildCatalog(context.Background(), runner, reg)
	if len(cat.Models) != len(claudeModels) {
		t.Fatalf("catalog = %+v, want one entry per claude model", cat.Models)
	}
	m, ok := cat.Find("claude/opus")
	if !ok {
		t.Fatalf("catalog = %+v, want claude/opus", cat.Models)
	}
	if m.Agent != "claude" || m.Name != "opus" {
		t.Errorf("claude/opus = %+v, want agent claude and flag value opus", m)
	}
	if !slices.Equal(m.Efforts, claudeEfforts) {
		t.Errorf("claude/opus efforts = %v, want %v", m.Efforts, claudeEfforts)
	}
}

// TestBuildCatalogSkipsInactiveAgents: only agents that answered a version
// probe can run anything, so only they contribute models.
func TestBuildCatalogSkipsInactiveAgents(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	runner := runnerFunc(func(context.Context, string, []string, tool.Options) (tool.Result, error) {
		return tool.Result{}, errors.New("no subprocess expected")
	})
	reg := NewRegistry()
	reg.Add(Agent{Name: "gemini", Command: "gemini", Status: StatusNotFound})
	if cat := BuildCatalog(context.Background(), runner, reg); len(cat.Models) != 0 {
		t.Errorf("catalog = %+v, want nothing from an uninstalled agent", cat.Models)
	}
}
