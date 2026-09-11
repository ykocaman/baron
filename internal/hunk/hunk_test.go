package hunk

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// stub replays one canned result and records the argv it was called with.
type stub struct {
	res  tool.Result
	err  error
	args []string
}

func (s *stub) Run(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
	s.args = args
	return s.res, s.err
}

// realListJSON is verbatim output from hunk 0.17.6 (`hunk session comment
// list --repo <path> --json`) against a session with one agent comment, so the
// parser is pinned to the actual shape rather than an assumed one.
const realListJSON = `{
  "comments": [
    {
      "commentId": "mcp:ed54a7be-f160-4e6a-8607-d38a803bb34c",
      "filePath": "f.txt",
      "hunkIndex": 0,
      "side": "new",
      "line": 2,
      "summary": "why did this change?",
      "createdAt": "2026-08-15T17:45:20.652Z"
    }
  ]
}`

func TestCommentsParsesRealCLIOutput(t *testing.T) {
	s := &stub{res: tool.Result{Stdout: realListJSON}}
	got, err := Comments(context.Background(), s, "/repo")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	c := got[0]
	if c.ID != "mcp:ed54a7be-f160-4e6a-8607-d38a803bb34c" {
		t.Errorf("ID = %q", c.ID)
	}
	if c.File != "f.txt" || c.Line != 2 || c.Side != "new" {
		t.Errorf("anchor = %s:%d (%s), want f.txt:2 (new)", c.File, c.Line, c.Side)
	}
	if c.Summary != "why did this change?" {
		t.Errorf("Summary = %q", c.Summary)
	}
	if c.CreatedAt.IsZero() {
		t.Error("CreatedAt did not parse")
	}
	want := []string{"session", "comment", "list", "--repo", "/repo", "--json"}
	if !slices.Equal(s.args, want) {
		t.Errorf("argv = %v, want %v", s.args, want)
	}
}

// TestCommentsNoSessionIsEmptyNotError: the Diff tab being closed is the
// normal state, not a failure — surfacing it as an error would put a red
// notice on screen every poll.
func TestCommentsNoSessionIsEmptyNotError(t *testing.T) {
	for _, res := range []tool.Result{
		{Stderr: "hunk: No active session matches repoRoot /repo."},
		{Stdout: "hunk: No active session matches repoRoot /repo."},
	} {
		s := &stub{res: res, err: errors.New("exit status 1")}
		got, err := Comments(context.Background(), s, "/repo")
		if err != nil {
			t.Errorf("Comments with %q: %v, want no error", res.Stdout+res.Stderr, err)
		}
		if len(got) != 0 {
			t.Errorf("got %d comments, want none", len(got))
		}
	}
}

// A zero-exit "no session" (some versions) must be handled on the success path
// too, or the JSON parser sees prose and reports a parse failure.
func TestCommentsNoSessionZeroExit(t *testing.T) {
	s := &stub{res: tool.Result{Stdout: "hunk: No active session matches repoRoot /repo."}}
	got, err := Comments(context.Background(), s, "/repo")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d comments, want none", len(got))
	}
}

func TestCommentsEmptyList(t *testing.T) {
	s := &stub{res: tool.Result{Stdout: `{"comments":[]}`}}
	got, err := Comments(context.Background(), s, "/repo")
	if err != nil || len(got) != 0 {
		t.Fatalf("Comments = %v, %v; want empty and no error", got, err)
	}
}

func TestCommentsRealFailureSurfaces(t *testing.T) {
	s := &stub{res: tool.Result{Stderr: "daemon not reachable"}, err: errors.New("exit status 2")}
	if _, err := Comments(context.Background(), s, "/repo"); err == nil {
		t.Error("a genuine failure must not be swallowed as an empty list")
	}
}

func TestCommentsMalformedJSONSurfaces(t *testing.T) {
	s := &stub{res: tool.Result{Stdout: "{not json"}}
	if _, err := Comments(context.Background(), s, "/repo"); err == nil {
		t.Error("malformed output must be reported, not silently read as no comments")
	}
}

func TestLocation(t *testing.T) {
	cases := []struct {
		c    Comment
		want string
	}{
		{Comment{File: "a/b.go", Line: 12}, "a/b.go:12"},
		{Comment{File: "a/b.go"}, "a/b.go"},
		{Comment{File: "a/b.go", Line: -1}, "a/b.go"},
	}
	for _, tc := range cases {
		if got := tc.c.Location(); got != tc.want {
			t.Errorf("Location() = %q, want %q", got, tc.want)
		}
	}
}

func TestAvailable(t *testing.T) {
	if !Available(context.Background(), &stub{}) {
		t.Error("Available must be true when hunk --version succeeds")
	}
	if Available(context.Background(), &stub{err: errors.New("not found")}) {
		t.Error("Available must be false when hunk is missing")
	}
}
