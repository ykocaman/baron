package agent

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"time"
)

// Model is one assignable choice: a model, and the agent CLI that runs it.
//
// Carrying Agent on the record is what makes assignment model-first. The
// picker shows IDs, the user picks one, and the agent, the value to pass on
// the agent's --model flag, and the valid effort levels all come from the
// same record — no string surgery on the ID, which never worked anyway
// (opencode's own catalog IDs are "provider/model" pairs like
// "opencode-go/deepseek-v4-flash", whose prefix is a provider namespace,
// not the agent that runs it).
type Model struct {
	// ID is the catalog key the user picks and `work assign --model` takes:
	// "claude/opus", "opencode-go/deepseek-v4-flash", or a bare agent name
	// for an agent with no model selection of its own ("gemini").
	ID string `json:"id"`
	// Agent is the name of the agent CLI that runs this model.
	Agent string `json:"agent"`
	// Name is the value passed on the agent's ModelFlag. "" means the agent
	// has no model selection and runs whatever it is configured to.
	Name string `json:"name,omitempty"`
	// Efforts are the reasoning-effort levels this model accepts, in the
	// order the picker should offer them. Empty means the assign flow skips
	// the effort step entirely.
	Efforts []string `json:"efforts,omitempty"`
	// Tier is this model's capability class (see ClassifyModel), used to
	// resolve a bead's required Tier to an actual model. "" means nothing
	// could classify it yet — still assignable directly by ID, just absent
	// from tier-based resolution.
	Tier Tier `json:"tier,omitempty"`
}

// catalogTTL is how long a fetched model catalog stays fresh. The list
// changes only when a CLI is installed, removed, or upgraded, so a day is
// generous; past it the TUI serves the cached list instantly and refreshes
// in the background, so the picker is never slow and never more than one
// open behind.
const catalogTTL = 24 * time.Hour

// Catalog is the machine-wide model catalog plus the usage index that ranks
// it, persisted together in ~/.cache/baron/models.json.
//
// The usage half (Counts/Last) is the user's own picking history, which is
// why a refresh must merge rather than overwrite: Save keeps whatever
// history is already on disk unless this Catalog carries its own.
type Catalog struct {
	FetchedAt time.Time `json:"fetched_at"`
	Models    []Model   `json:"models"`
	// Counts is how many times each model ID was assigned, Last when it was
	// assigned most recently. Both are keyed by Model.ID.
	Counts map[string]int       `json:"counts,omitempty"`
	Last   map[string]time.Time `json:"last,omitempty"`
}

// LoadCatalog reads the cached catalog. A missing or corrupt file yields an
// empty catalog rather than an error: this is a cache, and every consumer
// has a working answer for "nothing cached yet".
func LoadCatalog() *Catalog {
	c := &Catalog{Counts: map[string]int{}, Last: map[string]time.Time{}}
	path := ModelsPath()
	if path == "" {
		return c
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, c)
	if c.Counts == nil {
		c.Counts = map[string]int{}
	}
	if c.Last == nil {
		c.Last = map[string]time.Time{}
	}
	return c
}

// Save persists the catalog, preserving any usage history already on disk
// for IDs this Catalog has none for. A save failure is returned but is
// never fatal to a caller: the catalog is a cache.
func (c *Catalog) Save() error {
	path := ModelsPath()
	if path == "" {
		return nil
	}
	c.mergeStoredUsage()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeCacheFile(path, data)
}

// mergeStoredUsage folds the on-disk usage history into c, so a catalog
// refresh (which builds a fresh Catalog from a probe, with no history) does
// not wipe the user's picking history and reset the picker's ranking.
func (c *Catalog) mergeStoredUsage() {
	stored := LoadCatalog()
	if c.Counts == nil {
		c.Counts = map[string]int{}
	}
	if c.Last == nil {
		c.Last = map[string]time.Time{}
	}
	for id, n := range stored.Counts {
		if _, ok := c.Counts[id]; !ok {
			c.Counts[id] = n
		}
	}
	for id, t := range stored.Last {
		if _, ok := c.Last[id]; !ok {
			c.Last[id] = t
		}
	}
}

// Record notes that id was just assigned, for the picker's ranking.
func (c *Catalog) Record(id string) *Catalog {
	if c.Counts == nil {
		c.Counts = map[string]int{}
	}
	if c.Last == nil {
		c.Last = map[string]time.Time{}
	}
	c.Counts[id]++
	c.Last[id] = time.Now()
	return c
}

// Stale reports whether the catalog is missing or older than catalogTTL —
// the cue to refresh it in the background.
func (c *Catalog) Stale() bool {
	return len(c.Models) == 0 || time.Since(c.FetchedAt) > catalogTTL
}

// IDs returns every catalog ID, sorted — the assign picker's raw item list.
func (c *Catalog) IDs() []string {
	ids := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids
}

// Find returns the model with the given ID.
func (c *Catalog) Find(id string) (Model, bool) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// ForAgent returns the models a given agent runs.
func (c *Catalog) ForAgent(name string) []Model {
	out := []Model{}
	for _, m := range c.Models {
		if m.Agent == name {
			out = append(out, m)
		}
	}
	return out
}

// ForTier returns the models classified into a given tier — the
// tier-resolver's candidate pool.
func (c *Catalog) ForTier(tier Tier) []Model {
	out := []Model{}
	for _, m := range c.Models {
		if m.Tier == tier {
			out = append(out, m)
		}
	}
	return out
}

// Rank orders ids for the picker: recently used (up to 5, most recent
// first), then most-used (up to 3, not already shown), then the rest in
// their original order. IDs absent from ids are dropped.
func (c *Catalog) Rank(ids []string) (recent, popular, rest []string) {
	var used []string
	for _, s := range ids {
		if !c.Last[s].IsZero() {
			used = append(used, s)
		}
	}
	slices.SortStableFunc(used, func(a, b string) int { return compareTimes(c.Last[a], c.Last[b]) })
	recent = used[:min(5, len(used))]
	shown := make(map[string]bool, len(recent))
	for _, s := range recent {
		shown[s] = true
	}

	var counted []string
	for _, s := range ids {
		if !shown[s] && c.Counts[s] > 0 {
			counted = append(counted, s)
		}
	}
	slices.SortStableFunc(counted, func(a, b string) int { return compareCounts(c.Counts[a], c.Counts[b]) })
	popular = counted[:min(3, len(counted))]
	for _, s := range popular {
		shown[s] = true
	}

	for _, s := range ids {
		if !shown[s] {
			rest = append(rest, s)
		}
	}
	return recent, popular, rest
}

func compareTimes(a, b time.Time) int {
	if a.Equal(b) {
		return 0
	}
	if a.After(b) {
		return -1
	}
	return 1
}

func compareCounts(a, b int) int {
	if a == b {
		return 0
	}
	if a > b {
		return -1
	}
	return 1
}
