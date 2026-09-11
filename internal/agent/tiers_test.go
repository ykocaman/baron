package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestClassifyModelKeywords covers the keyword rules against real model
// names captured this session, including the case that drove the design:
// a resource like "relayhaus/sonnet" must classify from the word "sonnet"
// alone, with nothing in the classifier naming "relayhaus" specifically.
func TestClassifyModelKeywords(t *testing.T) {
	cases := []struct {
		id, family, name string
		want             Tier
	}{
		{id: "relayhaus/sonnet", want: TierStandard},
		{id: "relayhaus/opus", want: TierExpert},
		{id: "relayhaus/haiku", want: TierFast},
		{id: "claude/fable", name: "fable", want: TierGuru},
		{id: "claude/opus", name: "opus", want: TierExpert},
		{id: "claude/sonnet", name: "sonnet", want: TierStandard},
		{id: "claude/haiku", name: "haiku", want: TierFast},
		{id: "opencode-go/gemini-flash-lite", family: "gemini-flash-lite", want: TierFast},
		{id: "opencode-go/qwen3.7-max", family: "qwen3.7-max", want: TierExpert},
		{id: "opencode-go/qwen3.6-plus", family: "qwen3.6-plus", want: TierStandard},
	}
	for _, c := range cases {
		got, reason := ClassifyModel(c.id, c.family, c.name, false, 0, 0)
		if got != c.want {
			t.Errorf("ClassifyModel(%q,%q,%q) = %q (%s), want %q", c.id, c.family, c.name, got, reason, c.want)
		}
		if strings.Contains(strings.ToLower(reason), "relayhaus") {
			t.Errorf("ClassifyModel(%q): reason %q names a specific provider, must stay generic", c.id, reason)
		}
	}
}

// TestClassifyModelZeroCostWinsOverKeyword: a model reported as genuinely
// free must classify as free even when its own name would otherwise match
// a paid-tier keyword.
func TestClassifyModelZeroCostWinsOverKeyword(t *testing.T) {
	got, _ := ClassifyModel("relayhaus/opus", "", "", true, 0, 0)
	if got != TierFree {
		t.Errorf("zero-cost opus classified as %q, want free (cost overrides keyword)", got)
	}
}

// TestClassifyModelUnknownCostNeverReadsFree: without real pricing data
// (claude, agy), an unpriced model must never be silently treated as free
// just because its zero-value cost floats look the same as a real zero.
func TestClassifyModelUnknownCostNeverReadsFree(t *testing.T) {
	got, reason := ClassifyModel("agy/gpt-oss-120b-medium", "", "gpt-oss-120b-medium", false, 0, 0)
	if got == TierFree {
		t.Errorf("unpriced gpt-oss classified as free (reason %q) — hasCost=false must not read as a real zero", reason)
	}
	if got != "" {
		t.Errorf("unpriced gpt-oss with no keyword match = %q, want unclassified, not a guess", got)
	}
}

// TestClassifyModelBareFlashIsUnresolvedByKeyword documents the deliberate
// gap: real pricing shows Flash spans both fast and expert price points
// across generations, so a bare "flash" keyword must not resolve it —
// falling through to the cost-tercile fallback (or OpenRouter) instead.
func TestClassifyModelBareFlashIsUnresolvedByKeyword(t *testing.T) {
	got, _ := ClassifyModel("agy/gemini-3.7-flash-high", "", "gemini-3.7-flash-high", false, 0, 0)
	if got != "" {
		t.Errorf("bare-flash model with no known cost = %q, want unclassified (see tiers.toml's flash comment)", got)
	}

	cheap, _ := ClassifyModel("opencode-go/gemini-2.5-flash", "gemini-flash", "", true, 0.3, 0.3)
	if cheap != TierFast {
		t.Errorf("cheap flash ($0.3) via cost fallback = %q, want fast", cheap)
	}
	pricey, _ := ClassifyModel("opencode-go/gemini-3.6-flash", "gemini-flash", "", true, 1.5, 1.5)
	if pricey != TierExpert {
		t.Errorf("expensive flash ($1.5) via cost fallback = %q, want expert — same family, real price divergence", pricey)
	}
}

// TestClassifyModelBareProIsUnresolvedByKeyword documents the same
// deliberate gap for "pro": Google's Gemini Pro line prices expert-tier
// ($1.25-2) but DeepSeek's "v4-pro" prices standard-tier ($0.66) — not a
// safe cross-provider keyword, so it defers to cost like bare "flash" does.
func TestClassifyModelBareProIsUnresolvedByKeyword(t *testing.T) {
	got, _ := ClassifyModel("agy/gemini-3.1-pro-high", "", "gemini-3.1-pro-high", false, 0, 0)
	if got != "" {
		t.Errorf("bare-pro model with no known cost = %q, want unclassified (see tiers.toml's -pro comment)", got)
	}

	geminiPro, _ := ClassifyModel("opencode-go/gemini-2.5-pro", "gemini-pro", "", true, 1.25, 1.25)
	if geminiPro != TierExpert {
		t.Errorf("gemini-pro ($1.25) via cost fallback = %q, want expert", geminiPro)
	}
	deepseekPro, _ := ClassifyModel("opencode-go/deepseek-v4-pro", "", "", true, 0.66, 0.66)
	if deepseekPro != TierStandard {
		t.Errorf("deepseek-v4-pro ($0.66) via cost fallback = %q, want standard — same word, different price class", deepseekPro)
	}
}

// TestTierFromCostBoundaries pins the tercile cutoffs so a future edit to
// them is a deliberate, visible change.
func TestTierFromCostBoundaries(t *testing.T) {
	cases := []struct {
		cost float64
		want Tier
	}{
		{0, TierFast},
		{0.3, TierFast},
		{0.31, TierStandard},
		{1.0, TierStandard},
		{1.01, TierExpert},
		{3.0, TierExpert},
	}
	for _, c := range cases {
		if got := tierFromCost(c.cost); got != c.want {
			t.Errorf("tierFromCost(%v) = %q, want %q", c.cost, got, c.want)
		}
	}
}

// TestClassifyModelCostTercileForUnkeyworded classifies brand names that
// carry no size cue of their own (glm, grok, kimi) using only their real
// opencode pricing — the gap the keyword rules explicitly don't cover.
func TestClassifyModelCostTercileForUnkeyworded(t *testing.T) {
	cases := []struct {
		id      string
		costIn  float64
		costOut float64
		want    Tier
	}{
		{id: "opencode-go/glm-5.1", costIn: 1.4, costOut: 1.4, want: TierExpert},
		{id: "opencode-go/grok-5", costIn: 2, costOut: 2, want: TierExpert},
		{id: "opencode-go/kimi-k3", costIn: 3, costOut: 3, want: TierExpert},
		{id: "opencode-go/kimi-k2", costIn: 0.95, costOut: 0.95, want: TierStandard},
		{id: "opencode-go/deepseek-v4-pro", costIn: 0.66, costOut: 0.66, want: TierStandard},
		{id: "opencode-go/gpt-luna", costIn: 0.1, costOut: 0.1, want: TierFast},
		{id: "opencode-go/deepseek-v4-flash", costIn: 0.22, costOut: 0.22, want: TierFast},
		{id: "opencode-go/deepseek-v4-flash-free", costIn: 0, costOut: 0, want: TierFree},
	}
	for _, c := range cases {
		got, reason := ClassifyModel(c.id, "", "", true, c.costIn, c.costOut)
		if got != c.want {
			t.Errorf("ClassifyModel(%q, cost=%v/%v) = %q (%s), want %q", c.id, c.costIn, c.costOut, got, reason, c.want)
		}
	}
}

// TestParseAgyModelsClassification runs the real `agy models` output
// captured live this session through the whole discovery+classification
// path: opus/sonnet resolve from Anthropic's own naming (a tiers.toml
// keyword match), while the Gemini Pro/Flash variants and gpt-oss — agy
// exposes no pricing of its own, and neither "pro" nor "flash" is a safe
// enough cross-provider keyword to call without it (see tiers.toml) — fall
// through to ClassifyViaOpenRouter, stubbed here with fixture pricing so
// the test stays hermetic (no real network call).
func TestParseAgyModelsClassification(t *testing.T) {
	orig := openRouterFetch
	defer func() { openRouterFetch = orig }()
	openRouterFetch = func(context.Context, *http.Client) (*openRouterResponse, error) {
		priced := func(id, prompt string) openRouterModel {
			return openRouterModel{ID: id, Pricing: struct {
				Prompt string `json:"prompt"`
			}{Prompt: prompt}}
		}
		return &openRouterResponse{Data: []openRouterModel{
			priced("gemini-3.7-flash-high", "0.0000001"), // $0.1/1M -> fast
			priced("gemini-3.1-pro-high", "0.0000005"),   // $0.5/1M -> standard
			priced("gemini-3.1-pro-low", "0.0000005"),    // $0.5/1M -> standard
			priced("gpt-oss-120b-medium", "0.0000006"),   // $0.6/1M -> standard
		}}, nil
	}

	const agyModelsFixture = "Fetching available models...\n" +
		"gemini-3.7-flash-high\tGemini 3.7 Flash (High)\n" +
		"gemini-3.7-flash-medium\tGemini 3.7 Flash (Medium)\n" +
		"gemini-3.7-flash-low\tGemini 3.7 Flash (Low)\n" +
		"gemini-3.6-flash-high\tGemini 3.6 Flash (High)\n" +
		"gemini-3.6-flash-medium\tGemini 3.6 Flash (Medium)\n" +
		"gemini-3.6-flash-low\tGemini 3.6 Flash (Low)\n" +
		"gemini-3.5-flash-high\tGemini 3.5 Flash (High)\n" +
		"gemini-3.5-flash-medium\tGemini 3.5 Flash (Medium)\n" +
		"gemini-3.5-flash-low\tGemini 3.5 Flash (Low)\n" +
		"gemini-3.1-pro-high\tGemini 3.1 Pro (High)\n" +
		"gemini-3.1-pro-low\tGemini 3.1 Pro (Low)\n" +
		"claude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n" +
		"claude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\n" +
		"gpt-oss-120b-medium\tGPT-OSS 120B (Medium)\n"

	models := parseAgyModels(context.Background(), agyModelsFixture, "agy")
	if len(models) != 14 {
		t.Fatalf("parsed %d models, want 14 (the status line must not count)", len(models))
	}
	byID := make(map[string]Model, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	want := map[string]Tier{
		"agy/claude-opus-4-6-thinking": TierExpert,
		"agy/claude-sonnet-4-6":        TierStandard,
		"agy/gemini-3.1-pro-high":      TierStandard,
		"agy/gemini-3.1-pro-low":       TierStandard,
		"agy/gemini-3.7-flash-high":    TierFast,
		"agy/gpt-oss-120b-medium":      TierStandard,
	}
	for id, tier := range want {
		m, ok := byID[id]
		if !ok {
			t.Fatalf("missing model %q in parsed output: %+v", id, byID)
		}
		if m.Tier != tier {
			t.Errorf("%s: tier = %q, want %q", id, m.Tier, tier)
		}
		if m.Agent != "agy" {
			t.Errorf("%s: agent = %q, want agy", id, m.Agent)
		}
	}
}

// TestClassifyViaOpenRouterAppliesCostTercile drives the OpenRouter
// fallback with a fake fetcher (no real network call) and confirms it
// converts OpenRouter's per-token USD pricing to the same per-1M-token
// tercile ClassifyModel itself uses.
func TestClassifyViaOpenRouterAppliesCostTercile(t *testing.T) {
	orig := openRouterFetch
	defer func() { openRouterFetch = orig }()
	openRouterFetch = func(context.Context, *http.Client) (*openRouterResponse, error) {
		return &openRouterResponse{Data: []openRouterModel{
			{ID: "anthropic/claude-sonnet-4-6", Pricing: struct {
				Prompt string `json:"prompt"`
			}{Prompt: "0.000003"}}, // $3 per 1M tokens -> expert by the tercile
		}}, nil
	}

	tier, ok := ClassifyViaOpenRouter(context.Background(), "agy/claude-sonnet-4-6")
	if !ok {
		t.Fatal("ClassifyViaOpenRouter: ok = false, want true (bare-name match)")
	}
	if tier != TierExpert {
		t.Errorf("tier = %q, want expert for $3/1M tokens", tier)
	}
}

// TestClassifyViaOpenRouterDegradesOnFailure: a network error or a model
// OpenRouter has never heard of must come back as an unresolved tier, not
// an error the caller has to handle specially — an unclassified model is
// the acceptable outcome, never a guess.
func TestClassifyViaOpenRouterDegradesOnFailure(t *testing.T) {
	orig := openRouterFetch
	defer func() { openRouterFetch = orig }()

	openRouterFetch = func(context.Context, *http.Client) (*openRouterResponse, error) {
		return nil, errors.New("network unreachable")
	}
	if _, ok := ClassifyViaOpenRouter(context.Background(), "agy/claude-sonnet-4-6"); ok {
		t.Error("network failure: ok = true, want false")
	}

	openRouterFetch = func(context.Context, *http.Client) (*openRouterResponse, error) {
		return &openRouterResponse{Data: nil}, nil
	}
	if _, ok := ClassifyViaOpenRouter(context.Background(), "agy/some-brand-new-model"); ok {
		t.Error("unlisted model: ok = true, want false")
	}
}
