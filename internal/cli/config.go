package cli

import "path/filepath"

// configPath returns the project config file path.
func (a *app) configPath() string {
	return filepath.Join(a.dir, ".baron", "config.toml")
}
