package tui

import "github.com/baron-cli/baron/internal/agent"

// modelCache is the assign picker's usage index: how many times each model
// was picked and when it was last picked, used to rank the picker so
// recently-used and popular models sit on top.
//
// It is the same file as the model catalog itself (~/.cache/baron/models.json,
// see agent.Catalog): the catalog is what the picker shows and the usage
// index is how it is ordered, so one machine-wide file holds both and a
// catalog refresh preserves the history. The file is a passive cache —
// corrupt or missing entries silently fall back to the unranked
// Deps.Models() order.
type modelCache = agent.Catalog

// loadModelCache reads the usage index (and the catalog it lives in).
func loadModelCache() *modelCache { return agent.LoadCatalog() }

// rank orders models for the picker: recently used (up to 5, most recent
// first), then most-used (up to 3, not already shown), then the rest in
// their original order. Models absent from models are dropped.
func rank(c *modelCache, models []string) (recent, popular, rest []string) {
	return c.Rank(models)
}
