package domain

import "testing"

func TestEvaluateMergePolicyDisabled(t *testing.T) {
	got := EvaluateMergePolicy(MergePolicy{Enabled: false}, nil, 0, 0, nil)
	if got.Allowed {
		t.Error("Allowed = true, want false when merge.auto is disabled")
	}
}

// TestEvaluateMergePolicyEmptyTagsAllowsAnything: an empty require_tags is
// "no tag restriction," not "block everything" — this is what makes
// enabling auto-merge from `baron init` work immediately, with no bead
// tagging required first.
func TestEvaluateMergePolicyEmptyTagsAllowsAnything(t *testing.T) {
	policy := MergePolicy{Enabled: true, RequireTags: nil}
	got := EvaluateMergePolicy(policy, nil, 1, 1, nil)
	if !got.Allowed {
		t.Errorf("Allowed = false (%s), want true when require_tags is empty (untagged bead included)", got.Reason)
	}
}

func TestEvaluateMergePolicyMissingTag(t *testing.T) {
	policy := MergePolicy{Enabled: true, RequireTags: []string{"auto-merge"}}
	got := EvaluateMergePolicy(policy, []string{"bug"}, 1, 1, nil)
	if got.Allowed {
		t.Error("Allowed = true, want false when the bead lacks a required tag")
	}
}

func TestEvaluateMergePolicyMaxChangedFiles(t *testing.T) {
	policy := MergePolicy{Enabled: true, RequireTags: []string{"auto-merge"}, MaxChangedFiles: 3}
	got := EvaluateMergePolicy(policy, []string{"auto-merge"}, 4, 10, nil)
	if got.Allowed {
		t.Error("Allowed = true, want false when changed files exceed max_changed_files")
	}
}

func TestEvaluateMergePolicyMaxDiffLines(t *testing.T) {
	policy := MergePolicy{Enabled: true, RequireTags: []string{"auto-merge"}, MaxDiffLines: 50}
	got := EvaluateMergePolicy(policy, []string{"auto-merge"}, 2, 51, nil)
	if got.Allowed {
		t.Error("Allowed = true, want false when diff lines exceed max_diff_lines")
	}
}

func TestEvaluateMergePolicyForbidPaths(t *testing.T) {
	policy := MergePolicy{Enabled: true, RequireTags: []string{"auto-merge"}, ForbidPaths: []string{"*.secret"}}
	got := EvaluateMergePolicy(policy, []string{"auto-merge"}, 1, 1, []string{"config.secret"})
	if got.Allowed {
		t.Error("Allowed = true, want false when a changed path matches forbid_paths")
	}
}

func TestEvaluateMergePolicyAllowed(t *testing.T) {
	policy := MergePolicy{
		Enabled:         true,
		RequireTags:     []string{"auto-merge"},
		MaxChangedFiles: 5,
		MaxDiffLines:    100,
		ForbidPaths:     []string{"*.secret"},
	}
	got := EvaluateMergePolicy(policy, []string{"auto-merge", "docs"}, 2, 30, []string{"README.md", "docs/x.md"})
	if !got.Allowed {
		t.Errorf("Allowed = false (%s), want true", got.Reason)
	}
}
