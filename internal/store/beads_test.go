package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/tool"
)

// loadFixture reads a golden JSON fixture from test/fixtures/bd-json.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "test", "fixtures", "bd-json", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return data
}

// runnerStub returns a Runner that always produces the given stdout.
func runnerStub(stdout string) Runner {
	return RunnerFunc(func(context.Context, string, []string, tool.Options) (tool.Result, error) {
		return tool.Result{Stdout: stdout}, nil
	})
}

func TestBeadParse(t *testing.T) {
	ts := func(h, minute int) time.Time { return time.Date(2026, 8, 9, h, minute, 0, 0, time.UTC) }

	tests := []struct {
		name     string
		fixture  string
		envelope bool
		want     []Bead
	}{
		{
			name:     "non-envelope",
			fixture:  "list-non-envelope.json",
			envelope: false,
			want: []Bead{
				{
					ID:          "baron-a1b2c3",
					BRN:         domain.BRN("baron-a1b2c3"),
					Title:       "Implement auth",
					Description: "Add JWT auth to API",
					Status:      BeadStatusOpen,
					Tags:        []string{"backend", "security"},
					CreatedAt:   ts(10, 0),
					UpdatedAt:   ts(10, 0),
				},
				{
					ID:        "baron-d4e5f6",
					BRN:       domain.BRN("baron-d4e5f6"),
					Title:     "Fix login bug",
					Status:    BeadStatusAssigned,
					Assignee:  "claude-3.5",
					Tags:      []string{"bugfix"},
					CreatedAt: ts(11, 0),
					UpdatedAt: ts(12, 0),
				},
			},
		},
		{
			name:     "envelope",
			fixture:  "list-envelope.json",
			envelope: true,
			want: []Bead{
				{
					ID:          "baron-a1b2c3",
					BRN:         domain.BRN("baron-a1b2c3"),
					Title:       "Implement auth",
					Description: "Add JWT auth to API",
					Status:      BeadStatus("açık"),
					Tags:        []string{"backend", "security"},
					CreatedAt:   ts(10, 0),
					UpdatedAt:   ts(10, 0),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := loadFixture(t, tt.fixture)
			got, err := parseBeads(data, "")
			if err != nil {
				t.Fatalf("parseBeads: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("beads mismatch:\n got %+v\nwant %+v", got, tt.want)
			}
			if gotEnv := isEnvelope(data); gotEnv != tt.envelope {
				t.Fatalf("isEnvelope = %v, want %v", gotEnv, tt.envelope)
			}
		})
	}
}

func TestBeadParsePriorityAndAcceptance(t *testing.T) {
	data := []byte(`[
		{"id":"baron-a1b2c3","title":"Task","status":"open","acceptance_criteria":"tests pass","priority":1,"created_at":"2026-08-09T10:00:00Z","updated_at":"2026-08-09T10:00:00Z"},
		{"id":"baron-d4e5f6","title":"Low","status":"open","priority":3,"created_at":"2026-08-09T10:00:00Z","updated_at":"2026-08-09T10:00:00Z"},
		{"id":"baron-f6a7b8","title":"Crit","status":"open","priority":0,"created_at":"2026-08-09T10:00:00Z","updated_at":"2026-08-09T10:00:00Z"}
	]`)
	beads, err := parseBeads(data, "")
	if err != nil {
		t.Fatalf("parseBeads: %v", err)
	}
	want := []struct {
		id       string
		accept   string
		priority Priority
	}{
		// A straight 1:1 relabel now (bd's n -> P(n+1)), not a bucket: P0 and
		// P1 (bd's own numbering) used to both read "high" here, which made
		// a critical (0) bead visually indistinguishable from merely urgent
		// (1) — see Priority's doc comment.
		{"baron-a1b2c3", "tests pass", PriorityP2}, // bd priority 1
		{"baron-d4e5f6", "", PriorityP4},           // bd priority 3
		{"baron-f6a7b8", "", PriorityP1},           // bd priority 0 (critical) — no longer collapsed into P2
	}
	if len(beads) != len(want) {
		t.Fatalf("got %d beads, want %d", len(beads), len(want))
	}
	for i, w := range want {
		if beads[i].ID != w.id || beads[i].AcceptanceCriteria != w.accept || beads[i].Priority != w.priority {
			t.Fatalf("bead %d = %+v, want id %q acceptance %q priority %q", i, beads[i], w.id, w.accept, w.priority)
		}
	}
}

func TestBeadCreatePassesAcceptanceAndPriority(t *testing.T) {
	var got []string
	r := RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
		got = args
		return tool.Result{Stdout: "baron-a1b2c3\n"}, nil
	})
	s := NewBeadStore(r, t.TempDir())
	bead, err := s.Create(context.Background(), CreateBeadParams{Title: "Task", Description: "desc", Acceptance: "tests pass", Priority: PriorityP1, Tier: agent.TierStandard})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := []string{"create", "Task", "--json", "--description", "desc", "--acceptance", "tests pass", "--priority", "0", "--metadata", `{"tier":"standard"}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	if bead.ID != "baron-a1b2c3" {
		t.Fatalf("bead.ID = %q, want baron-a1b2c3", bead.ID)
	}
}

func TestBeadCreateDefaultsToMediumPriority(t *testing.T) {
	var got []string
	r := RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
		got = args
		return tool.Result{Stdout: "baron-a1b2c3\n"}, nil
	})
	s := NewBeadStore(r, t.TempDir())
	if _, err := s.Create(context.Background(), CreateBeadParams{Title: "Task", Tier: agent.TierFast}); err != nil {
		t.Fatalf("create: %v", err)
	}
	want := []string{"create", "Task", "--json", "--metadata", `{"tier":"fast"}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBeadCreatePassesParentAndType(t *testing.T) {
	var got []string
	r := RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
		got = args
		return tool.Result{Stdout: "baron-a1b2c3\n"}, nil
	})
	s := NewBeadStore(r, t.TempDir())
	s.SetBRNPrefix("baron")
	bead, err := s.Create(context.Background(), CreateBeadParams{Title: "Child", Parent: "baron-obq", IssueType: "epic", Tier: agent.TierExpert})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := []string{"create", "Child", "--json", "--parent", "obq", "--type", "epic", "--metadata", `{"tier":"expert"}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	if bead.ID != "baron-a1b2c3" {
		t.Fatalf("bead.ID = %q, want baron-a1b2c3", bead.ID)
	}
}

// TestBeadCreateRequiresTier: tier is mandatory, not merely defaulted —
// omitting it must fail before bd is even invoked.
func TestBeadCreateRequiresTier(t *testing.T) {
	called := false
	r := RunnerFunc(func(_ context.Context, _ string, _ []string, _ tool.Options) (tool.Result, error) {
		called = true
		return tool.Result{}, nil
	})
	s := NewBeadStore(r, t.TempDir())
	if _, err := s.Create(context.Background(), CreateBeadParams{Title: "Task"}); err == nil {
		t.Fatal("Create with empty tier: want error, got nil")
	}
	if called {
		t.Error("bd was invoked despite the missing-tier error")
	}
}

// TestBeadTierReadsMetadata: Tier reads the "tier" metadata key directly,
// the same way Model/Effort read theirs — independent of Assignee, so it
// survives resolution (Assignee/Model get overwritten with the real
// agent+model; Tier does not).
func TestBeadTierReadsMetadata(t *testing.T) {
	b := Bead{Assignee: "claude", Metadata: map[string]string{"tier": "expert", "model": "opus"}}
	if got := b.Tier(); got != agent.TierExpert {
		t.Errorf("Tier() = %q, want expert", got)
	}
}

// TestBeadTierDefaultsFastWhenUnsetOrInvalid: a bead with no tier metadata
// at all (created by bypassing baron, e.g. a raw `bd create`) or an
// unrecognized value (a stale/typo'd one) defaults to agent.TierFast rather
// than reading as empty — a real default the reconciler's tier-resolution
// pass now acts on too (reconcileTierAssignments), not just a display
// fallback. See Tier's doc comment for why this replaced the old
// "unset reads as empty, left untouched forever" behavior.
func TestBeadTierDefaultsFastWhenUnsetOrInvalid(t *testing.T) {
	unset := Bead{}
	if got := unset.Tier(); got != agent.TierFast {
		t.Errorf("Tier() = %q, want %q for a bead with no tier metadata", got, agent.TierFast)
	}
	invalid := Bead{Metadata: map[string]string{"tier": "nonsense"}}
	if got := invalid.Tier(); got != agent.TierFast {
		t.Errorf("Tier() = %q, want %q for a bead with an unrecognized tier value", got, agent.TierFast)
	}
}

func TestBeadStatusNormalization(t *testing.T) {
	tests := []struct {
		fromBD    BeadStatus
		toBD      string
		canonical BeadStatus
	}{
		{"open", "open", BeadStatusOpen},
		{"in_progress", "in_progress", BeadStatusWorking},
		{"blocked", "blocked", BeadStatusBlocked},
		{"closed", "closed", BeadStatusClosed},
		{"deferred", "closed", BeadStatusCancelled},
		{"pinned", "open", BeadStatusOpen},
		{"hooked", "in_progress", BeadStatusWorking},
		{"unknown_state", "unknown_state", BeadStatus("unknown_state")},
	}
	for _, tt := range tests {
		if got := normalizeStatus(tt.fromBD); got != tt.canonical {
			t.Errorf("normalizeStatus(%q) = %q, want %q", tt.fromBD, got, tt.canonical)
		}
		if got := bdStatus(tt.canonical); got != tt.toBD {
			t.Errorf("bdStatus(%q) = %q, want %q", tt.canonical, got, tt.toBD)
		}
	}
}

func TestBeadStatusWriteUsesBDName(t *testing.T) {
	var got []string
	r := RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
		got = args
		return tool.Result{}, nil
	})
	s := NewBeadStore(r, t.TempDir())
	if err := s.Status(context.Background(), "baron-a1b2c3", BeadStatusBlocked); err != nil {
		t.Fatalf("status: %v", err)
	}
	want := []string{"update", "baron-a1b2c3", "--status", "blocked"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBeadStatusWriteCustomLiteral(t *testing.T) {
	var calls [][]string
	r := RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
		calls = append(calls, args)
		if len(args) == 3 && args[0] == "config" && args[1] == "get" {
			return tool.Result{Stdout: "status.custom (not set)"}, nil
		}
		return tool.Result{}, nil
	})
	s := NewBeadStore(r, t.TempDir())
	if err := s.Status(context.Background(), "baron-a1b2c3", BeadStatusHumanQueue); err != nil {
		t.Fatalf("status: %v", err)
	}
	want := [][]string{
		{"config", "get", "status.custom"},
		{"config", "set", "status.custom", "validating,retry,mergable,human_queue,merged,cancelled"},
		{"update", "baron-a1b2c3", "--status", "human_queue"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestBeadStatusCustomRegisteredOnce(t *testing.T) {
	var configCalls int
	r := RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
		if len(args) > 0 && args[0] == "config" {
			configCalls++
			if args[1] == "get" {
				return tool.Result{Stdout: "status.custom (not set)"}, nil
			}
		}
		return tool.Result{}, nil
	})
	s := NewBeadStore(r, t.TempDir())
	for _, st := range []BeadStatus{BeadStatusHumanQueue, BeadStatusMergable} {
		if err := s.Status(context.Background(), "baron-a1b2c3", st); err != nil {
			t.Fatalf("status %s: %v", st, err)
		}
	}
	if configCalls != 2 {
		t.Fatalf("config calls = %d, want 2 (one get, one set)", configCalls)
	}
}

// TestBeadStatusCustomUpgradesStaleConfig: a project whose status.custom was
// registered by an older BARON build (missing "merged", added later) must
// get re-registered with the full current list — otherwise writing
// "merged" would silently fall back to bd's own status vocabulary and
// collapse to "closed" instead of round-tripping verbatim.
func TestBeadStatusCustomUpgradesStaleConfig(t *testing.T) {
	var calls [][]string
	r := RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
		calls = append(calls, args)
		if len(args) == 3 && args[0] == "config" && args[1] == "get" {
			return tool.Result{Stdout: "validating,retry,mergable,human_queue"}, nil
		}
		return tool.Result{}, nil
	})
	s := NewBeadStore(r, t.TempDir())
	if err := s.Status(context.Background(), "baron-a1b2c3", BeadStatusMerged); err != nil {
		t.Fatalf("status: %v", err)
	}
	want := [][]string{
		{"config", "get", "status.custom"},
		{"config", "set", "status.custom", "validating,retry,mergable,human_queue,merged,cancelled"},
		{"update", "baron-a1b2c3", "--status", "merged"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

// TestBeadStatusCancelledRoundTrips: cancelled must be a registered custom
// status so Status() writes it literally, not via bdStatus's "closed"
// collapse. Before this was registered, a cancelled bead wrote as plain
// "closed" and read back as BeadStatusClosed (normalizeStatus has no
// reverse entry for bare "closed", only "deferred" maps back to cancelled)
// — silently indistinguishable from a bead closed normally, and reachable
// again via closed's own reopen edge, defeating the state machine's
// "cancelled has no exit" rule (allowedTransitions[BeadStateCancelled] is
// empty).
func TestBeadStatusCancelledRoundTrips(t *testing.T) {
	var calls [][]string
	r := RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
		calls = append(calls, args)
		if len(args) == 3 && args[0] == "config" && args[1] == "get" {
			return tool.Result{Stdout: "status.custom (not set)"}, nil
		}
		return tool.Result{}, nil
	})
	s := NewBeadStore(r, t.TempDir())
	if err := s.Status(context.Background(), "baron-a1b2c3", BeadStatusCancelled); err != nil {
		t.Fatalf("status: %v", err)
	}
	want := [][]string{
		{"config", "get", "status.custom"},
		{"config", "set", "status.custom", "validating,retry,mergable,human_queue,merged,cancelled"},
		{"update", "baron-a1b2c3", "--status", "cancelled"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v (a literal --status cancelled write, not the bdStatus \"closed\" collapse)", calls, want)
	}
	if got := normalizeStatus(BeadStatusCancelled); got != BeadStatusCancelled {
		t.Errorf("normalizeStatus(cancelled) = %q, want it to pass through unchanged", got)
	}
}

func TestBeadPriorityBDValue(t *testing.T) {
	tests := []struct {
		prio Priority
		want string
	}{
		{PriorityP1, "0"},
		{PriorityP2, "1"},
		{PriorityP3, "2"},
		{PriorityP4, "3"},
		{PriorityP5, "4"},
		{Priority("garbage"), "2"}, // unrecognized -> bd's own default
	}
	for _, tt := range tests {
		if got := tt.prio.bdValue(); got != tt.want {
			t.Fatalf("bdValue(%q) = %q, want %q", tt.prio, got, tt.want)
		}
	}
}

func TestBeadParseVersion(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    string
		wantErr bool
	}{
		{"homebrew", "bd version 1.0.5 (Homebrew)", "1.0.5", false},
		{"plain", "1.1.2", "1.1.2", false},
		{"v prefix", "bd version v2.0.1", "2.0.1", false},
		{"trailing newline", "bd version 1.1.2\n", "1.1.2", false},
		{"no version", "bd: unknown command", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVersion(tt.out)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseVersion(%q) err = %v, wantErr %v", tt.out, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("parseVersion(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

func TestBeadCheckDrift(t *testing.T) {
	tests := []struct {
		name      string
		installed string
		pinned    string
		wantErr   bool
	}{
		{"exact match", "1.1.2", "1.1.2", false},
		{"patch ahead ok", "1.1.3", "1.1.2", false},
		{"minor behind", "1.0.5", "1.1.2", true},
		{"major behind", "0.9.0", "1.1.2", true},
		{"minor ahead", "1.2.0", "1.1.2", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewBeadStore(runnerStub("bd version "+tt.installed+" (Homebrew)"), t.TempDir())
			err := s.CheckDrift(context.Background(), tt.pinned)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckDrift(%s vs %s) err = %v, wantErr %v", tt.installed, tt.pinned, err, tt.wantErr)
			}
		})
	}
}

func TestBeadBRNMapping(t *testing.T) {
	tests := []struct {
		id     string
		prefix string
		want   domain.BRN
	}{
		{"baron-a1b2c3", "", domain.BRN("baron-a1b2c3")},
		{"baron-d4e5f6", "", domain.BRN("baron-d4e5f6")},
		{"baron-a1b2c3", "BRN", domain.BRN("BRN-baron-a1b2c3")},
		{"BRN-baron-a1b2c3", "BRN", domain.BRN("BRN-baron-a1b2c3")}, // already prefixed
	}
	for _, tt := range tests {
		name := tt.id + "_prefix_" + tt.prefix
		t.Run(name, func(t *testing.T) {
			if got := brnFromID(tt.id, tt.prefix); got != tt.want {
				t.Fatalf("brnFromID(%q, %q) = %q, want %q", tt.id, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestParseComments(t *testing.T) {
	ts := func(h, minute, sec int) time.Time { return time.Date(2026, 8, 10, h, minute, sec, 0, time.UTC) }

	commentJSON := `[
		{"id":"uuid-1","issue_id":"test-9ze","author":"ykocaman","text":"first comment","created_at":"2026-08-10T09:52:55Z"},
		{"id":"uuid-2","issue_id":"test-9ze","author":"ykocaman","text":"second comment","created_at":"2026-08-10T09:55:00Z"}
	]`

	tests := []struct {
		name string
		data string
		want []Comment
	}{
		{
			name: "bare array",
			data: commentJSON,
			want: []Comment{
				{ID: "uuid-1", IssueID: "test-9ze", Author: "ykocaman", Text: "first comment", CreatedAt: ts(9, 52, 55)},
				{ID: "uuid-2", IssueID: "test-9ze", Author: "ykocaman", Text: "second comment", CreatedAt: ts(9, 55, 0)},
			},
		},
		{
			name: "envelope",
			data: `{"version":2,"items":` + commentJSON + `}`,
			want: []Comment{
				{ID: "uuid-1", IssueID: "test-9ze", Author: "ykocaman", Text: "first comment", CreatedAt: ts(9, 52, 55)},
				{ID: "uuid-2", IssueID: "test-9ze", Author: "ykocaman", Text: "second comment", CreatedAt: ts(9, 55, 0)},
			},
		},
		{
			name: "empty array",
			data: `[]`,
			want: []Comment{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseComments([]byte(tt.data))
			if err != nil {
				t.Fatalf("parseComments: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("comments mismatch:\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestBeadCommentsArgs(t *testing.T) {
	ts := func(h, minute, sec int) time.Time { return time.Date(2026, 8, 10, h, minute, sec, 0, time.UTC) }
	wantComment := Comment{ID: "uuid-1", IssueID: "test-9ze", Author: "ykocaman", Text: "first comment", CreatedAt: ts(9, 52, 55)}
	stdout := `[{"id":"uuid-1","issue_id":"test-9ze","author":"ykocaman","text":"first comment","created_at":"2026-08-10T09:52:55Z"}]`

	tests := []struct {
		name   string
		prefix string
		id     string
		want   []string
	}{
		{"bare id", "", "test-9ze", []string{"comments", "test-9ze", "--json"}},
		{"full BRN", "BRN", "BRN-test-9ze", []string{"comments", "test-9ze", "--json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			r := RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
				got = args
				return tool.Result{Stdout: stdout}, nil
			})
			s := NewBeadStore(r, t.TempDir())
			s.SetBRNPrefix(tt.prefix)
			comments, err := s.Comments(context.Background(), tt.id)
			if err != nil {
				t.Fatalf("comments: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("args = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(comments, []Comment{wantComment}) {
				t.Fatalf("comments = %+v, want %+v", comments, []Comment{wantComment})
			}
		})
	}
}

func TestBeadDomainState(t *testing.T) {
	tests := []struct {
		name   string
		status BeadStatus
		assign string
		want   domain.BeadState
	}{
		{"open assigned", BeadStatusOpen, "ykocaman", domain.BeadStateAssigned},
		{"open unassigned", BeadStatusOpen, "", domain.BeadStateOpen},
		{"working", BeadStatusWorking, "", domain.BeadStateWorking},
		{"blocked", BeadStatusBlocked, "", domain.BeadStateBlocked},
		{"closed", BeadStatusClosed, "", domain.BeadStateClosed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := Bead{Status: tt.status, Assignee: tt.assign}
			if got := b.DomainState(); got != tt.want {
				t.Fatalf("DomainState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBeadList(t *testing.T) {
	// The first call observes envelope output; the second must then request
	// BD_JSON_ENVELOPE=1.
	var args []string
	var envs []map[string]string
	calls := 0
	r := RunnerFunc(func(ctx context.Context, name string, a []string, opts tool.Options) (tool.Result, error) {
		calls++
		args = append(args, strings.Join(a, " "))
		envs = append(envs, opts.Env)
		if calls == 1 {
			return tool.Result{Stdout: string(loadFixture(t, "list-envelope.json"))}, nil
		}
		return tool.Result{Stdout: string(loadFixture(t, "list-non-envelope.json"))}, nil
	})
	s := NewBeadStore(r, t.TempDir())

	first, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first list = %d beads, want 1", len(first))
	}
	second, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second list = %d beads, want 2", len(second))
	}
	if args[0] != "list --all --json" {
		t.Fatalf("first args = %q, want %q", args[0], "list --all --json")
	}
	if envs[0] != nil {
		t.Fatalf("first call env = %v, want nil", envs[0])
	}
	if envs[1]["BD_JSON_ENVELOPE"] != "1" {
		t.Fatalf("second call env = %v, want BD_JSON_ENVELOPE=1", envs[1])
	}
}

func TestSetModelMetadata(t *testing.T) {
	tests := []struct {
		name   string
		model  *string
		effort *string
		want   []string // args after "update <id>", nil means no bd call at all
	}{
		// Writing the model always clears the legacy "llm" key too, so a
		// bead assigned before the rename can't keep a stale second answer.
		{"both set", strPtr("opus"), strPtr("high"), []string{"--set-metadata", "model=opus", "--unset-metadata", "llm", "--set-metadata", "effort=high"}},
		{"only model", strPtr("opencode-go/deepseek"), nil, []string{"--set-metadata", "model=opencode-go/deepseek", "--unset-metadata", "llm"}},
		{"only effort", nil, strPtr("max"), []string{"--set-metadata", "effort=max"}},
		{"clear both", strPtr(""), strPtr(""), []string{"--unset-metadata", "model", "--unset-metadata", "llm", "--unset-metadata", "effort"}},
		{"neither set: no bd call", nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			called := false
			r := RunnerFunc(func(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
				called = true
				if name != "bd" || len(args) < 2 || args[0] != "update" || args[1] != "baron-a" {
					t.Fatalf("args = %v, want [update baron-a ...]", args)
				}
				got = args[2:]
				return tool.Result{}, nil
			})
			s := NewBeadStore(r, t.TempDir())
			if err := s.SetModelMetadata(context.Background(), "baron-a", tt.model, tt.effort); err != nil {
				t.Fatalf("SetModelMetadata: %v", err)
			}
			if tt.want == nil {
				if called {
					t.Errorf("bd called with %v, want no call", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("args = %v, want %v", got, tt.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func TestBeadModelAndEffortFromMetadata(t *testing.T) {
	b := Bead{Metadata: map[string]string{"model": "opus", "effort": "high"}}
	if got := b.Model(); got != "opus" {
		t.Errorf("Model() = %q, want opus", got)
	}
	if got := b.Effort(); got != "high" {
		t.Errorf("Effort() = %q, want high", got)
	}
	empty := Bead{}
	if got := empty.Model(); got != "" {
		t.Errorf("Model() on nil metadata = %q, want empty", got)
	}
}

// TestBeadModelFallsBackToLegacyLLMKey: beads assigned before the metadata
// key was renamed from "llm" to "model" must keep running the model they
// were assigned, rather than silently reverting to the agent's default.
func TestBeadModelFallsBackToLegacyLLMKey(t *testing.T) {
	b := Bead{Metadata: map[string]string{"llm": "haiku"}}
	if got := b.Model(); got != "haiku" {
		t.Errorf("Model() = %q, want haiku from the legacy llm key", got)
	}
	// The new key wins when both are present.
	both := Bead{Metadata: map[string]string{"llm": "haiku", "model": "opus"}}
	if got := both.Model(); got != "opus" {
		t.Errorf("Model() = %q, want the new model key to win", got)
	}
}
