package domain

import "testing"

func TestMergeBaseDetailRoundTrip(t *testing.T) {
	detail := MergeBaseDetail("merged task/0is into main", "34a2684aa2d75a627e3d121114e25044c3789913")
	sha, ok := MergeBaseSHA(detail)
	if !ok {
		t.Fatalf("MergeBaseSHA(%q) ok = false, want true", detail)
	}
	if sha != "34a2684aa2d75a627e3d121114e25044c3789913" {
		t.Errorf("sha = %q, want the embedded SHA", sha)
	}
}

func TestMergeBaseDetailEmptySHALeavesDetailPlain(t *testing.T) {
	detail := MergeBaseDetail("merged task/0is into main", "")
	if detail != "merged task/0is into main" {
		t.Errorf("detail = %q, want unchanged (no marker) when sha is empty", detail)
	}
	if _, ok := MergeBaseSHA(detail); ok {
		t.Errorf("MergeBaseSHA(%q) ok = true, want false (no marker present)", detail)
	}
}

func TestMergeBaseSHAOnOldUnmarkedDetail(t *testing.T) {
	// A merge event logged before this feature existed: no marker at all.
	if _, ok := MergeBaseSHA("merged task/0is into main"); ok {
		t.Errorf("MergeBaseSHA on an old, unmarked detail: ok = true, want false")
	}
}
