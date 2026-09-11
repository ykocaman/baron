package tmux

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// scriptStep is one scripted response for stubRunner.
type scriptStep struct {
	res tool.Result
	err error
}

// stubRunner records every argv it receives and replays scripted results in
// order; the final step repeats for calls beyond the script, and an empty
// script always succeeds with empty output.
type stubRunner struct {
	mu    sync.Mutex
	calls [][]string
	steps []scriptStep
}

func (s *stubRunner) Run(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
	s.mu.Lock()
	i := len(s.calls)
	s.calls = append(s.calls, append([]string(nil), args...))
	s.mu.Unlock()
	if len(s.steps) == 0 {
		return tool.Result{}, nil
	}
	if i >= len(s.steps) {
		i = len(s.steps) - 1
	}
	step := s.steps[i]
	return step.res, step.err
}

func newStub(steps ...scriptStep) *stubRunner {
	return &stubRunner{steps: steps}
}

// ok is a scriptStep that succeeds with the given stdout.
func ok(stdout string) scriptStep {
	return scriptStep{res: tool.Result{Stdout: stdout}}
}

// fail is a scriptStep that fails with tmux's "can't find" stderr message,
// the normal signal for a missing session/window.
func fail(stderr string) scriptStep {
	return scriptStep{res: tool.Result{Stderr: stderr}, err: errors.New("exit status 1")}
}

// wantCalls asserts the exact argv sequence the runner received.
func wantCalls(t *testing.T, got, want [][]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runner argv calls:\n got: %v\nwant: %v", got, want)
	}
}

func wantErrContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}
