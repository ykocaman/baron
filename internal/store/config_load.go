// Package store implements BARON's persistent configuration and audit storage.
package store

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// UserConfigDir returns ~/.config/baron — the machine-wide user config
// directory config.toml's own user layer resolves relative to.
func UserConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "baron"), nil
}

// ProjectProfilesDir returns projectDir's .baron/profiles drop-in directory.
func ProjectProfilesDir(projectDir string) string {
	return filepath.Join(projectDir, ".baron", ProfilesDirName)
}

// projectConfigPath returns projectDir's .baron/config.toml path.
func projectConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".baron", ConfigFileName)
}

// LoadConfig loads the effective configuration for a project directory with
// precedence env > project (.baron/config.toml, then its profiles/ drop-ins)
// > user (~/.config/baron/config.toml, then its profiles/ drop-ins) >
// defaults.
func LoadConfig(projectDir string) (*Config, error) {
	userDir, err := UserConfigDir()
	if err != nil {
		return nil, err
	}
	return loadConfig(projectDir, filepath.Join(userDir, ConfigFileName))
}

// loadConfig is LoadConfig with an injectable user config path for testing.
// Precedence, lowest to highest: defaults, the user's profiles/ drop-ins,
// the user's config.toml (its own [profiles.*] sections win over same-named
// drop-ins — editing config.toml directly is the more deliberate act), the
// project's profiles/ drop-ins, the project's config.toml (same rule).
func loadConfig(projectDir, userPath string) (*Config, error) {
	cfg := DefaultConfig()

	if err := mergeProfilesDir(cfg, filepath.Join(filepath.Dir(userPath), ProfilesDirName)); err != nil {
		return nil, err
	}
	if userCfg, md, err := decodeFile(userPath); err != nil {
		return nil, err
	} else if userCfg != nil {
		overlay(cfg, userCfg, md)
	}

	if err := mergeProfilesDir(cfg, ProjectProfilesDir(projectDir)); err != nil {
		return nil, err
	}
	projPath := projectConfigPath(projectDir)
	if projCfg, md, err := decodeFile(projPath); err != nil {
		return nil, err
	} else if projCfg != nil {
		overlay(cfg, projCfg, md)
	}

	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// maxRetryBudget is the hard ceiling validation.md §1.6 sets on
// gate.retry_budget: the gate's policy — a bead gets 3 retries after its
// initial attempt, never a 4th — must not be something a config file can
// quietly raise. See docs/PRD/harness-hardening.md §1.2.
const maxRetryBudget = 3

// Validate checks invariants config loading itself can't enforce just by
// parsing (a value being present and well-typed isn't the same as it being
// within policy). Called automatically at the end of loadConfig/LoadConfig
// — every config this package hands out has already passed this, so
// callers never need to call it themselves.
func (c *Config) Validate() error {
	if c.Gate.RetryBudget > maxRetryBudget {
		return fmt.Errorf("gate.retry_budget = %d exceeds the maximum of %d (validation.md §1.6)", c.Gate.RetryBudget, maxRetryBudget)
	}
	if c.Gate.RetryBudget < 0 {
		return fmt.Errorf("gate.retry_budget = %d must not be negative", c.Gate.RetryBudget)
	}
	return nil
}

// decodeFile decodes a TOML file into a fresh Config. It returns a nil Config
// when the file does not exist. Unknown keys are warned about, not rejected.
func decodeFile(path string) (*Config, toml.MetaData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, toml.MetaData{}, nil
		}
		return nil, toml.MetaData{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, toml.MetaData{}, fmt.Errorf("decode %s: %w", path, err)
	}
	for _, k := range md.Undecoded() {
		slog.Warn("unknown config key", "file", path, "key", strings.Join(k, "."))
	}
	return &cfg, md, nil
}

// mergeProfilesDir merges every *.toml file directly inside dir into
// cfg.Profiles, keyed by filename with the extension stripped (e.g.
// rust.toml becomes profile "rust"). A missing dir is not an error — most
// projects have none. Each file's content is a single Profile body (the
// same shape a [profiles.<name>] section decodes to), not a full Config.
func mergeProfilesDir(cfg *Config, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read profiles dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var p Profile
		if _, err := toml.Decode(string(data), &p); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		if cfg.Profiles == nil {
			cfg.Profiles = make(map[string]Profile)
		}
		cfg.Profiles[strings.TrimSuffix(e.Name(), ".toml")] = p
	}
	return nil
}

// WriteProfileFile TOML-encodes p and writes it to dir/name.toml, creating
// dir if needed. It refuses to overwrite an existing file — export is meant
// to hand the user an editable starting point once, never to clobber
// changes they've since made to it.
func WriteProfileFile(dir, name string, p Profile) error {
	path := filepath.Join(dir, name+".toml")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil // lost a race with another export; the file exists, which is the goal
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := toml.NewEncoder(f).Encode(p); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return nil
}

// overlay copies fields from src into base only for keys the source file
// actually defined (via md.IsDefined), so explicit false/0 values win over
// defaults. name/profile are shorthand aliases of project_name/default_profile:
// whichever key a file defines, both Go fields are synced to that value.
