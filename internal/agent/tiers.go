package agent

import (
	"context"
	"embed"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Tier is a model's capability class, in ascending order of how demanding a
// task it should be trusted with: free < fast < standard < expert < guru.
// A bead is assigned a Tier, not a raw model — which real model satisfies
// it on this machine, right now, is a resolver's job, not the human's.
type Tier string

const (
	// TierGuru is a provider's newest/cutting-edge line (claude's "fable"),
	// sitting above expert rather than below it — not yet as proven, but
	// not a downgrade either.
	TierGuru Tier = "guru"
	// TierExpert is a flagship/most-capable model (claude's "opus").
	TierExpert Tier = "expert"
	// TierStandard is a mid-tier workhorse (claude's "sonnet") — the
	// default for ordinary work.
	TierStandard Tier = "standard"
	// TierFast is a cheap/quick model (claude's "haiku") for simple or
	// high-volume work.
	TierFast Tier = "fast"
	// TierFree is any model with zero marginal cost.
	TierFree Tier = "free"
)

// IsTier reports whether t is one of the five known tier keywords — used to
// tell a bead's Assignee apart from a real agent CLI name, since assignment
// is tier-first (bd assign <bead> <tier>) and no real agent will ever be
// named exactly one of these.
func IsTier(t Tier) bool {
	switch t {
	case TierGuru, TierExpert, TierStandard, TierFast, TierFree:
		return true
	}
	return false
}

//go:embed tiers.toml
var tiersTOML embed.FS

// tierRule is one row of tiers.toml: a tier and the keyword pattern that
// earns it.
type tierRule struct {
	Tier    string `toml:"tier"`
	Pattern string `toml:"pattern"`
	re      *regexp.Regexp
}

type tiersDoc struct {
	Rules []tierRule `toml:"rules"`
}

// tierRules is compiled once from the embedded ruleset at package init —
// a malformed tiers.toml is a build-time bug, not a runtime condition to
// handle.
var tierRules = mustLoadTierRules()

func mustLoadTierRules() []tierRule {
	data, err := tiersTOML.ReadFile("tiers.toml")
	if err != nil {
		panic("agent: embedded tiers.toml: " + err.Error())
	}
	var doc tiersDoc
	if _, err := toml.Decode(string(data), &doc); err != nil {
		panic("agent: embedded tiers.toml: " + err.Error())
	}
	for i := range doc.Rules {
		doc.Rules[i].re = regexp.MustCompile("(?i)" + doc.Rules[i].Pattern)
	}
	return doc.Rules
}

// ClassifyModel assigns a Tier to a model from its id, family and display
// name, and — when known — its real per-1M-token USD cost. It returns the
// tier ("" when nothing could place it) and a short human-readable reason,
// useful for debugging a surprising classification.
//
// hasCost distinguishes "cost is known and happens to be zero" (opencode
// reports real cost.input/cost.output, including real zeros for its free
// models) from "cost is simply not known" (claude and agy expose no
// pricing data at all) — without that distinction an unpriced claude model
// would wrongly read as free.
//
// Order: a known-zero cost always wins (free), checked before any keyword;
// then the first matching rule from tiers.toml (see that file for why
// every pattern is a generic, provider-neutral size word — this is what
// makes "relayhaus/sonnet" classify as standard without any code anywhere
// needing to know "relayhaus" exists); then, only when cost is known but no
// keyword matched, a cost-tercile guess (tierFromCost); otherwise
// unclassified.
func ClassifyModel(id, family, name string, hasCost bool, costIn, costOut float64) (Tier, string) {
	if hasCost && costIn == 0 && costOut == 0 {
		return TierFree, "cost is reported as zero"
	}
	haystack := strings.ToLower(id + " " + family + " " + name)
	for _, r := range tierRules {
		if r.re.MatchString(haystack) {
			return Tier(r.Tier), "matched pattern " + r.Pattern
		}
	}
	if hasCost {
		return tierFromCost(costIn), "no keyword match, priced by cost.input"
	}
	return "", "no keyword match and no known cost"
}

// tierFromCost buckets a known per-1M-input-token USD cost into a tier for
// a model no naming keyword could place (brand names like glm, grok, kimi,
// qwen carry no size cue of their own). Thresholds come from the real
// price spread observed across opencode's catalog: <=$0.3 sits with the
// cheap/haiku-class models, $0.3-$1.0 with the sonnet-class workhorses,
// >$1.0 with the opus-class flagships. No tier above expert is reachable
// this way — guru is a naming signal (a provider's own "this is our
// newest line" claim), not a price point.
func tierFromCost(costIn float64) Tier {
	switch {
	case costIn <= 0.3:
		return TierFast
	case costIn <= 1.0:
		return TierStandard
	default:
		return TierExpert
	}
}

// openRouterModel is the subset of one entry from OpenRouter's public
// model list (GET /api/v1/models) ClassifyViaOpenRouter needs.
// https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties
type openRouterModel struct {
	ID      string `json:"id"`
	Pricing struct {
		Prompt string `json:"prompt"` // USD per single token, as a decimal string
	} `json:"pricing"`
}

type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

// openRouterFetch performs the live lookup; tests override it to avoid a
// real network call.
var openRouterFetch = func(ctx context.Context, client *http.Client) (*openRouterResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var out openRouterResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClassifyViaOpenRouter is the best-effort fallback for a model
// ClassifyModel could not place: no keyword matched and no local pricing is
// known (claude and agy report no cost data of their own — this is exactly
// the gap agy's Gemini Flash variants fall into, since agy gives no per-
// model price and "flash" alone is deliberately not a keyword rule). It
// looks the model's bare name (the part after the last "/") up in
// OpenRouter's public model list purely for pricing, and applies the same
// cost-tercile rule ClassifyModel uses.
//
// This is supplementary metadata only — OpenRouter is never the source of
// which models exist (each agent CLI's own live output is), only of a
// price for a model something else already reported existing. A network
// failure, timeout, or a model OpenRouter doesn't list all degrade to
// ok == false: an unresolved tier is the acceptable outcome here, not a
// guess dressed up as an answer.
func ClassifyViaOpenRouter(ctx context.Context, id string) (tier Tier, ok bool) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := openRouterFetch(ctx, client)
	if err != nil {
		return "", false
	}
	bare := bareModelName(id)
	for _, m := range resp.Data {
		if !strings.EqualFold(bareModelName(m.ID), bare) {
			continue
		}
		costIn, err := strconv.ParseFloat(m.Pricing.Prompt, 64)
		if err != nil {
			return "", false
		}
		// OpenRouter prices per single token; ClassifyModel's thresholds are
		// per 1M tokens, matching opencode's own units.
		return tierFromCost(costIn * 1_000_000), true
	}
	return "", false
}

// bareModelName strips any "provider/" prefix, e.g.
// "anthropic/claude-sonnet-4-6" -> "claude-sonnet-4-6".
func bareModelName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}
