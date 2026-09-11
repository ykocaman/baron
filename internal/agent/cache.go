package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// CacheDir is where BARON keeps its machine-wide agent and model caches:
// $XDG_CACHE_HOME/baron when that is set, ~/.cache/baron otherwise.
//
// This is deliberately outside any project. Which agent CLIs are installed
// and which models they expose are facts about the machine, identical in
// every checkout — caching them per project meant every repo re-probed the
// same binaries and carried a stale copy of the same answer in its .baron/
// directory. "" means the home directory couldn't be resolved, which makes
// every cache operation a silent no-op (probing still works, it just isn't
// remembered).
func CacheDir() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "baron")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "baron")
}

// AgentsPath is the machine-wide agent probe cache (~/.cache/baron/agents.json).
func AgentsPath() string {
	dir := CacheDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "agents.json")
}

// ModelsPath is the machine-wide model catalog (~/.cache/baron/models.json).
func ModelsPath() string {
	dir := CacheDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "models.json")
}

// agentsFile is the on-disk shape of agents.json. ProbedAt drives the
// staleness check that lets a long-lived TUI refresh in the background.
type agentsFile struct {
	ProbedAt time.Time `json:"probed_at"`
	Agents   []Agent   `json:"agents"`
}

// LoadAgents reads the cached agent probe into a registry. A missing or
// unreadable cache yields an empty registry and a nil error — nothing has
// probed yet is a normal state, not a failure; only a corrupt file is an
// error worth reporting.
func LoadAgents() (*Registry, time.Time, error) {
	r := NewRegistry()
	path := AgentsPath()
	if path == "" {
		return r, time.Time{}, nil
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var f agentsFile
		if err := json.Unmarshal(data, &f); err != nil {
			return r, time.Time{}, fmt.Errorf("decode %s: %w", path, err)
		}
		for _, a := range f.Agents {
			r.Add(a)
		}
		return r, f.ProbedAt, nil
	}
	return r, time.Time{}, nil
}

// SaveAgents writes the registry to the machine-wide cache.
func SaveAgents(r *Registry) error {
	path := AgentsPath()
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(agentsFile{ProbedAt: time.Now(), Agents: r.List()}, "", "  ")
	if err != nil {
		return err
	}
	return writeCacheFile(path, data)
}

// writeCacheFile writes data to path, creating the cache directory. The
// write goes to a temporary file in the same directory and is then renamed
// over the target, so a BARON process reading the cache while another one
// refreshes it never sees a half-written file.
func writeCacheFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create cache temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}

// sortAgents orders agents by name, the order every listing renders in.
func sortAgents(agents []Agent) {
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
}
