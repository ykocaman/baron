package domain

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// MergePolicy is the opt-in conditional auto-merge configuration. Enabled
// defaults to false. RequireTags scopes auto-merge to beads carrying at
// least one of the listed bd tags; empty means no tag restriction at all —
// every bead that clears the other guardrails (MaxChangedFiles,
// MaxDiffLines, ForbidPaths) auto-merges, which is what enabling auto-merge
// from `baron init` gives you with no further setup.
type MergePolicy struct {
	Enabled         bool
	RequireTags     []string
	MaxChangedFiles int // 0 = no limit
	MaxDiffLines    int // 0 = no limit
	ForbidPaths     []string
}

// MergePolicyDecision is the result of evaluating a policy against one bead.
type MergePolicyDecision struct {
	Allowed bool
	Reason  string // why not, when Allowed is false
}

// EvaluateMergePolicy decides whether a bead's branch qualifies for
// automatic merge under policy, given the bead's own bd tags, changed-file
// count, diff line count, and changed paths. This covers only the policy's
// own fields; the fixed preconditions (gate passed, secret scan clean,
// commits signed, merge-tree preflight, ls-files -u, diff --check) are the
// caller's job — they need subprocess access this package doesn't have, and
// they apply regardless of policy content. ForbidPaths uses filepath.Match
// semantics (single path segment per "*", no recursive "**").
func EvaluateMergePolicy(policy MergePolicy, tags []string, changedFiles, diffLines int, changedPaths []string) MergePolicyDecision {
	if !policy.Enabled {
		return MergePolicyDecision{Reason: "merge.auto.enabled is false"}
	}
	if len(policy.RequireTags) > 0 && !hasAnyTag(tags, policy.RequireTags) {
		return MergePolicyDecision{Reason: "bead has none of the required tags: " + strings.Join(policy.RequireTags, ", ")}
	}
	if policy.MaxChangedFiles > 0 && changedFiles > policy.MaxChangedFiles {
		return MergePolicyDecision{Reason: fmt.Sprintf("changed files %d exceeds max_changed_files %d", changedFiles, policy.MaxChangedFiles)}
	}
	if policy.MaxDiffLines > 0 && diffLines > policy.MaxDiffLines {
		return MergePolicyDecision{Reason: fmt.Sprintf("diff lines %d exceeds max_diff_lines %d", diffLines, policy.MaxDiffLines)}
	}
	for _, p := range changedPaths {
		for _, pattern := range policy.ForbidPaths {
			if matched, _ := filepath.Match(pattern, p); matched {
				return MergePolicyDecision{Reason: fmt.Sprintf("changed path %q matches forbid_paths %q", p, pattern)}
			}
		}
	}
	return MergePolicyDecision{Allowed: true}
}

// hasAnyTag reports whether tags contains any of required.
func hasAnyTag(tags, required []string) bool {
	for _, t := range tags {
		if slices.Contains(required, t) {
			return true
		}
	}
	return false
}
