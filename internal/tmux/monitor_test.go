package tmux

import (
	"context"
	"reflect"
	"testing"
)

func TestClient_CapturePane_argvAndStdout(t *testing.T) {
	s := newStub(ok("line1\nline2\n"))
	c := New(s)

	got, err := c.CapturePane(context.Background(), "BRN-001", 10)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if got != "line1\nline2\n" {
		t.Fatalf("CapturePane stdout: got %q", got)
	}
	wantCalls(t, s.calls, [][]string{
		{"capture-pane", "-t", "baron:BRN-001", "-p", "-S", "-10"},
	})
}

func TestClient_CapturePane_propagatesError(t *testing.T) {
	s := newStub(fail("server error"))
	c := New(s)

	_, err := c.CapturePane(context.Background(), "BRN-001", 10)
	wantErrContains(t, err, "tmux capture-pane: server error")
}

func TestClient_LastLines(t *testing.T) {
	tests := []struct {
		name  string
		stdin string
		want  []string
	}{
		{
			name:  "trailing empty lines trimmed",
			stdin: "a\nb\n\n\n",
			want:  []string{"a", "b"},
		},
		{
			name:  "single line",
			stdin: "only\n",
			want:  []string{"only"},
		},
		{
			name:  "empty output",
			stdin: "",
			want:  []string{},
		},
		{
			name:  "only newlines",
			stdin: "\n\n",
			want:  []string{},
		},
		{
			name:  "leading blanks preserved",
			stdin: "\n\na\n",
			want:  []string{"", "", "a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStub(ok(tt.stdin))
			c := New(s)

			got, err := c.LastLines(context.Background(), "BRN-001", 10)
			if err != nil {
				t.Fatalf("LastLines: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LastLines:\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

func TestClient_LastLines_propagatesCaptureError(t *testing.T) {
	s := newStub(fail("server error"))
	c := New(s)

	_, err := c.LastLines(context.Background(), "BRN-001", 10)
	wantErrContains(t, err, "tmux capture-pane: server error")
}

func TestAvailable_true(t *testing.T) {
	s := newStub(ok("tmux 3.4"))

	if !Available(context.Background(), s) {
		t.Fatal("Available: expected true for tmux 3.4")
	}
	wantCalls(t, s.calls, [][]string{
		{"-V"},
	})
}

func TestAvailable_falseOnError(t *testing.T) {
	s := newStub(fail("executable file not found in $PATH"))

	if Available(context.Background(), s) {
		t.Fatal("Available: expected false on error")
	}
}

func TestAvailable_falseOnGarbage(t *testing.T) {
	s := newStub(ok(""))

	if Available(context.Background(), s) {
		t.Fatal("Available: expected false on empty output")
	}
}
