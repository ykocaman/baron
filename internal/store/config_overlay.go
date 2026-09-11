// Package store implements BARON's persistent configuration and audit storage.
package store

import (
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

// overlay copies every field src's TOML source actually set (per md,
// BurntSushi/toml's per-key "was this present" metadata) onto base — walking
// configFields (config_field.go) instead of hand-listing each one, same as
// applyEnv. Aliased fields (name/project_name, profile/default_profile) are
// re-mirrored via syncAliases right after the copy, matching applyEnv's own
// handling of the same pair from an env var.
func overlay(base, src *Config, md toml.MetaData) {
	baseV := reflect.ValueOf(base).Elem()
	srcV := reflect.ValueOf(src).Elem()
	for _, cf := range configFields {
		if !md.IsDefined(cf.tomlPath...) {
			continue
		}
		srcFv, err := fieldAtPath(srcV, cf.tomlPath)
		if err != nil {
			continue // unreachable: every configFields path is valid on Config
		}
		baseFv, err := fieldAtPath(baseV, cf.tomlPath)
		if err != nil {
			continue
		}
		baseFv.Set(srcFv)
		syncAliases(base, strings.Join(cf.tomlPath, "."))
	}
	if md.IsDefined("agents") {
		base.Agents = src.Agents
	}
	for name, p := range src.Profiles {
		if base.Profiles == nil {
			base.Profiles = make(map[string]Profile)
		}
		base.Profiles[name] = p
	}
}
