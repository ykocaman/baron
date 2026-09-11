package domain

import "strings"

// mergeBaseMarker prefixes the pre-merge base SHA appended to a "merge"/
// "merge_auto" audit event's detail. Kept as a suffix on the existing free-
// text Detail field rather than a new structured AuditEvent field so old,
// already-logged merge events (with no SHA) keep parsing fine — MergeBaseSHA
// just reports "no snapshot available" for them instead of erroring.
const mergeBaseMarker = " [merge-base: "

// MergeBaseDetail appends beforeSHA's marker to detail, or returns detail
// unchanged if beforeSHA is empty (the caller's own HeadSHA lookup failed —
// the merge itself still succeeded, only the Diff tab's post-merge snapshot
// is unavailable).
//
// beforeSHA is the base branch's own tip *before* the merge ran — not the
// resulting merge commit. A plain `git show`/`git diff-tree` on a clean,
// non-conflicting `--no-ff` merge commit (every BARON merge — see
// tool.MergeBranch) shows an EMPTY diff by git's own default combined-diff
// semantics, so that commit's SHA alone is not enough. `beforeSHA` fixes
// this: since the base checkout's working tree sits at the merge commit
// right afterward with nothing uncommitted, diffing it against beforeSHA —
// `hunk diff beforeSHA` (working tree vs. a target ref), not `hunk show` —
// reproduces exactly what the branch introduced, and it's the one thing
// left to show once the bead's worktree is gone (readyForMerge's callers
// remove it right after) and the branch's own live diff against base has
// gone empty (base now contains it too, so `git diff base...branch` is
// nothing).
func MergeBaseDetail(detail, beforeSHA string) string {
	if beforeSHA == "" {
		return detail
	}
	return detail + mergeBaseMarker + beforeSHA + "]"
}

// MergeBaseSHA extracts the SHA MergeBaseDetail appended, if any.
func MergeBaseSHA(detail string) (string, bool) {
	i := strings.LastIndex(detail, mergeBaseMarker)
	if i < 0 || !strings.HasSuffix(detail, "]") {
		return "", false
	}
	sha := detail[i+len(mergeBaseMarker) : len(detail)-1]
	if sha == "" {
		return "", false
	}
	return sha, true
}
