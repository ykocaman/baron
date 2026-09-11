package cli

import (
	"context"
	"fmt"

	"github.com/baron-cli/baron/internal/domain"
)

// depResult is the JSON shape of `baron work dep add`.
type depResult struct {
	BRN       domain.BRN `json:"brn"`
	DependsOn domain.BRN `json:"depends_on"`
	Type      string     `json:"type"`
}

// validDepTypes are the dependency types bd accepts.
var validDepTypes = []string{"blocks", "related", "parent-child", "discovered-from"}

// AddDep links target as a dependency of brn, cobra-free. depType must be
// one of validDepTypes — callers that accept it from a human (CLI flag, a
// future TUI field) validate against that list themselves, since the
// message differs by context (a usage error for the CLI, a form validation
// message for a TUI).
func (a *app) AddDep(ctx context.Context, brn, target domain.BRN, depType string) (depResult, error) {
	if err := a.beads.DepAdd(ctx, a.idOf(brn), a.idOf(target), depType); err != nil {
		return depResult{}, err
	}
	a.auditLog("dep_add", string(brn), fmt.Sprintf("depends on %s (%s)", target, depType))
	return depResult{BRN: brn, DependsOn: target, Type: depType}, nil
}
