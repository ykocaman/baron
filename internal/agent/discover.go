package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/tool"
)

// Discovery is one machine scan: which agent CLIs exist, which of them are
// usable, and what they can be assigned.
type Discovery struct {
	// Checklist is every candidate BARON knows, including ones not
	// installed (StatusNotFound) — what `baron doctor` renders. Registry
	// omits those, since nothing can be dispatched to a missing binary.
	Checklist []Agent
	Registry  *Registry
	Catalog   *Catalog
}

// Refresh probes the machine for agent CLIs, builds the model catalog for
// the ones that answered, and writes both machine-wide caches. It is what
// `baron init` and `baron doctor` run, and what the TUI kicks off in the
// background when the catalog goes stale.
//
// configAgents are the project's [[agents]] overrides. They shape the
// returned registry and catalog — an agent registered only in config.toml
// is assignable like any other — but are not written to the shared cache,
// which stays a record of what was found on the machine, identical for
// every project on it.
func Refresh(ctx context.Context, runner tool.Runner, configAgents []ConfigAgent) (Discovery, error) {
	p := NewProbe(runner)
	checklist := p.ProbeAll(ctx)
	reg := NewRegistry()
	for _, a := range checklist {
		if a.Status != StatusNotFound {
			reg.Add(a)
		}
	}
	// A cache that can't be written is reported, but never cuts the scan
	// short: the caller asked what is on this machine, and that answer is
	// already in hand — every Discovery returned from here is complete.
	saveErr := SaveAgents(reg)
	reg.MergeConfig(configAgents)
	// A config-registered agent BARON has no candidate for was merged in as
	// active on the config's word alone. Probe it too, so the catalog never
	// offers a model nothing on this machine can run — unless the config
	// stated `active` explicitly, in which case the user's word wins.
	for _, ca := range configAgents {
		a, ok := reg.Get(ca.Name)
		if !ok || ca.Active != nil {
			continue
		}
		probed, err := p.ProbeSingle(ctx, ca.Name)
		if err != nil {
			a.Status = StatusPassive
		} else {
			a.Status, a.Version = probed.Status, probed.Version
		}
		reg.Add(a)
	}
	cat := BuildCatalog(ctx, runner, reg)
	d := Discovery{Checklist: checklist, Registry: reg, Catalog: cat}
	return d, errors.Join(saveErr, cat.Save())
}

// WithConfigAgents returns a copy of the catalog with the project's
// [[agents]] entries layered on — the same merge Refresh applies when it
// rebuilds a stale catalog, done in memory so a catalog served straight
// from cache (which never carries config-only agents, by design) still
// offers them. Nothing here is written back to the shared cache.
//
// An agent the catalog already knows keeps its entries unless the config
// deactivates it (active = false); an agent missing from the catalog is
// probed when the config didn't state `active` outright — the catalog must
// not offer a model nothing on this machine can run — and contributes its
// discoverable models: for an agent with no model selection, the bare
// agent name.
func (c *Catalog) WithConfigAgents(ctx context.Context, runner tool.Runner, configAgents []ConfigAgent) *Catalog {
	if len(configAgents) == 0 {
		return c
	}
	reg := loadMergedRegistry(configAgents)
	deactivated := deactivatedAgents(configAgents)
	known := knownAgents(c.Models)

	models, changed1 := dropDeactivatedModels(c.Models, deactivated)
	added, changed2 := newModelsForActiveAgents(ctx, runner, reg, configAgents, known, deactivated)
	models = append(models, added...)

	if !changed1 && !changed2 {
		return c
	}
	out := *c
	out.Models = models
	return &out
}

// loadMergedRegistry loads the machine-wide agent registry (falling back to
// an empty one when the cache read fails) with configAgents' own overrides
// already merged on top — see Registry.MergeConfig.
func loadMergedRegistry(configAgents []ConfigAgent) *Registry {
	reg, _, err := LoadAgents()
	if err != nil {
		reg = NewRegistry()
	}
	reg.MergeConfig(configAgents)
	return reg
}

// deactivatedAgents is the set of agent names configAgents explicitly turns
// off (active = false).
func deactivatedAgents(configAgents []ConfigAgent) map[string]bool {
	deactivated := make(map[string]bool, len(configAgents))
	for _, ca := range configAgents {
		if ca.Active != nil && !*ca.Active {
			deactivated[ca.Name] = true
		}
	}
	return deactivated
}

// knownAgents is the set of agent names already contributing at least one
// model to models.
func knownAgents(models []Model) map[string]bool {
	known := make(map[string]bool, len(models))
	for _, m := range models {
		known[m.Agent] = true
	}
	return known
}

// dropDeactivatedModels filters models down to those whose agent isn't in
// deactivated, reporting whether anything was actually dropped.
func dropDeactivatedModels(models []Model, deactivated map[string]bool) (kept []Model, changed bool) {
	kept = make([]Model, 0, len(models))
	for _, m := range models {
		if deactivated[m.Agent] {
			changed = true
			continue
		}
		kept = append(kept, m)
	}
	return kept, changed
}

// newModelsForActiveAgents contributes models for every registry agent not
// already known and not deactivated: an agent whose config entry left
// `active` unstated is probed live (p) to confirm it's really usable on this
// machine before its models are offered — the catalog must not offer a
// model nothing on this machine can run.
func newModelsForActiveAgents(ctx context.Context, runner tool.Runner, reg *Registry, configAgents []ConfigAgent, known, deactivated map[string]bool) (added []Model, changed bool) {
	p := NewProbe(runner)
	for _, a := range reg.Active() {
		if known[a.Name] || deactivated[a.Name] {
			continue
		}
		a = resolveAgentStatus(ctx, p, a, configAgents)
		if a.Status != StatusActive {
			continue
		}
		added = append(added, discoverModels(ctx, runner, a)...)
		changed = true
	}
	return added, changed
}

// resolveAgentStatus re-probes a's own liveness when its config entry left
// `active` unstated — an explicit active=true/false already decided the
// question and needs no live probe.
func resolveAgentStatus(ctx context.Context, p *Probe, a Agent, configAgents []ConfigAgent) Agent {
	ca, ok := configAgentByName(configAgents, a.Name)
	if !ok || ca.Active != nil {
		return a
	}
	probed, err := p.ProbeSingle(ctx, a.Name)
	if err != nil {
		a.Status = StatusPassive
	} else {
		a.Status, a.Version = probed.Status, probed.Version
	}
	return a
}

func configAgentByName(configAgents []ConfigAgent, name string) (ConfigAgent, bool) {
	for _, ca := range configAgents {
		if ca.Name == name {
			return ca, true
		}
	}
	return ConfigAgent{}, false
}

// BuildCatalog assembles the assignable-model list for a registry: every
// active agent contributes either its own models (claude's aliases,
// opencode's live catalog) or a single entry under its own name when it has
// no model selection of its own (gemini, agy, and any agent registered via
// [[agents]]).
//
// Contributing the bare agent name matters: without it an agent with no
// model list would be undiscoverable in the picker even though `work assign
// --agent <name>` accepted it happily.
func BuildCatalog(ctx context.Context, runner tool.Runner, reg *Registry) *Catalog {
	cat := LoadCatalog()
	cat.Models = nil
	cat.FetchedAt = time.Now()
	for _, a := range reg.Active() {
		cat.Models = append(cat.Models, discoverModels(ctx, runner, a)...)
	}
	sort.Slice(cat.Models, func(i, j int) bool { return cat.Models[i].ID < cat.Models[j].ID })
	return cat
}

// catalogWithFallback calls a live-catalog fetcher (opencodeCatalog,
// agyCatalog) and falls back to a single bare-name entry when the fetch
// fails or returns nothing. The catalog lookup is a live subprocess call;
// when it fails, offering the agent under its own name still assigns
// correctly (it falls back to its own configured default model) — dropping
// it entirely does not.
func catalogWithFallback(a Agent, fn func() ([]Model, error)) []Model {
	if models, err := fn(); err == nil && len(models) > 0 {
		return models
	}
	return []Model{{ID: a.Name, Agent: a.Name}}
}

// discoverModels returns the catalog entries one agent contributes.
func discoverModels(ctx context.Context, runner tool.Runner, a Agent) []Model {
	if a.Command == "opencode" {
		return catalogWithFallback(a, func() ([]Model, error) { return opencodeCatalog(ctx, runner, a) })
	}
	if a.Command == "agy" {
		return catalogWithFallback(a, func() ([]Model, error) { return agyCatalog(ctx, runner, a) })
	}
	c, ok := candidatesIndex[a.Name]
	if !ok || len(c.models) == 0 {
		return []Model{{ID: a.Name, Agent: a.Name}}
	}
	models := make([]Model, 0, len(c.models))
	for _, name := range c.models {
		tier, _ := ClassifyModel(a.Name+"/"+name, "", name, false, 0, 0)
		models = append(models, Model{
			ID:      a.Name + "/" + name,
			Agent:   a.Name,
			Name:    name,
			Efforts: c.efforts,
			Tier:    tier,
		})
	}
	return models
}

// agyCatalogTimeout bounds `agy models`. Unlike opencode's catalog lookup
// (served from a local file), agy's own "Fetching available models..."
// prefix shows it's a live network round trip — in an offline or
// network-restricted environment (e.g. a sandboxed test run) it could hang
// well past what a catalog refresh should ever cost. A bounded timeout
// degrades to the bare-agent-name fallback instead of stalling the whole
// refresh (and, once init runs automatically on TUI startup, the whole
// launch) on one CLI's network call.
const agyCatalogTimeout = 10 * time.Second

// agyCatalog reads agy's own model list in a single subprocess call.
//
// `agy models` prints a "Fetching available models..." status line
// followed by one "<id>\t<display name>" line per model, e.g.
// "claude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)" — no JSON, and no
// pricing data, unlike opencode's catalog.
func agyCatalog(ctx context.Context, runner tool.Runner, a Agent) ([]Model, error) {
	res, err := runner.Run(ctx, a.Command, []string{"models"}, tool.Options{Timeout: agyCatalogTimeout})
	if err != nil {
		return nil, err
	}
	return parseAgyModels(ctx, res.Stdout, a.Name), nil
}

// parseAgyModels parses agy models' tab-separated "id\tname" lines. agy
// reports no pricing of its own, so a model ClassifyModel can't place by
// keyword (e.g. a Gemini Flash variant — see ClassifyViaOpenRouter's own
// doc comment) falls back to OpenRouter's public pricing before giving up.
func parseAgyModels(ctx context.Context, out, agentName string) []Model {
	var models []Model
	for line := range strings.SplitSeq(out, "\n") {
		id, _, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		tier, _ := ClassifyModel(agentName+"/"+id, "", id, false, 0, 0)
		if tier == "" {
			if t, ok := ClassifyViaOpenRouter(ctx, id); ok {
				tier = t
			}
		}
		models = append(models, Model{ID: agentName + "/" + id, Agent: agentName, Name: id, Tier: tier})
	}
	return models
}

// opencodeCatalog reads opencode's own model catalog in a single
// subprocess call.
//
// `opencode models --verbose` prints one line naming each model
// ("opencode/big-pickle") followed by that model's full JSON metadata,
// including a "variants" object whose keys are exactly its valid --variant
// values (a model with no reasoning support has "variants": {}). Reading
// the whole thing once is what makes the assign form instant: it used to
// take one `opencode models` call to open the picker and a second
// `opencode models --verbose` call per selected model just to learn its
// effort levels.
func opencodeCatalog(ctx context.Context, runner tool.Runner, a Agent) ([]Model, error) {
	res, err := runner.Run(ctx, a.Command, []string{"models", "--verbose"}, tool.Options{})
	if err != nil {
		return nil, err
	}
	entries, err := parseOpencodeModels(res.Stdout)
	if err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(entries))
	for _, e := range entries {
		tier, _ := ClassifyModel(e.id, e.family, e.id, true, e.costIn, e.costOut)
		models = append(models, Model{
			// opencode's own IDs are already "provider/model" and are
			// passed to --model verbatim, so the catalog ID and the flag
			// value are the same string here.
			ID:      e.id,
			Agent:   a.Name,
			Name:    e.id,
			Efforts: e.variants,
			Tier:    tier,
		})
	}
	return models, nil
}

// opencodeEntry is one model from `opencode models --verbose`.
type opencodeEntry struct {
	id       string
	family   string
	costIn   float64
	costOut  float64
	variants []string
}

// parseOpencodeModels reads the "id\n{json}\n" pairs `opencode models
// --verbose` emits — not a single JSON document, but one repeated per
// model. A block that doesn't parse is skipped rather than failing the
// whole catalog: one malformed entry must not cost the user every other
// model.
func parseOpencodeModels(out string) ([]opencodeEntry, error) {
	lines := strings.Split(out, "\n")
	var entries []opencodeEntry
	for i := 0; i < len(lines); i++ {
		id := strings.TrimSpace(lines[i])
		if id == "" || strings.HasPrefix(id, "{") || strings.HasPrefix(id, "}") {
			continue
		}
		block, next := jsonBlockAt(lines, i+1)
		if block == "" {
			continue
		}
		i = next
		var parsed struct {
			Family string `json:"family"`
			Cost   struct {
				Input  float64 `json:"input"`
				Output float64 `json:"output"`
			} `json:"cost"`
			Variants map[string]json.RawMessage `json:"variants"`
		}
		if err := json.Unmarshal([]byte(block), &parsed); err != nil {
			continue
		}
		keys := make([]string, 0, len(parsed.Variants))
		for k := range parsed.Variants {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		entries = append(entries, opencodeEntry{
			id:       id,
			family:   parsed.Family,
			costIn:   parsed.Cost.Input,
			costOut:  parsed.Cost.Output,
			variants: keys,
		})
	}
	if len(entries) == 0 && strings.TrimSpace(out) != "" {
		return nil, fmt.Errorf("opencode models: no model entries in output")
	}
	return entries, nil
}

// jsonBlockAt returns the brace-balanced JSON object starting at lines[from]
// and the index of its last line. ("", from) means there is no object there.
func jsonBlockAt(lines []string, from int) (string, int) {
	if from >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[from]), "{") {
		return "", from
	}
	var b strings.Builder
	depth := 0
	for j := from; j < len(lines); j++ {
		b.WriteString(lines[j])
		b.WriteByte('\n')
		for _, r := range lines[j] {
			switch r {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth == 0 {
			return b.String(), j
		}
	}
	return "", from
}
