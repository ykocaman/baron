package cli

import (
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// epicCloseRunner serves the pre-close fixture (child still open) on the
// first `bd list` call — checkEpicCloseAllowed's guard — and the post-close
// fixture (child closed) on later calls, which is what the auto-close check
// sees after `bd close` has run.
func epicCloseRunner(preClose, postClose string) *fakeRunner {
	var lists int
	return &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		if name == "bd" && len(args) > 0 && args[0] == "list" {
			lists++
			if lists == 1 {
				return tool.Result{Stdout: preClose}, nil
			}
			return tool.Result{Stdout: postClose}, nil
		}
		return tool.Result{}, nil
	}}
}

func TestWorkCloseAutoClosesEpic(t *testing.T) {
	const pre = `[{"id":"baron-e1a2b3","title":"epic","status":"open","issue_type":"epic"},` +
		`{"id":"baron-e1a2b3.1","title":"child one","status":"closed","issue_type":"task"},` +
		`{"id":"baron-e1a2b3.2","title":"child two","status":"open","issue_type":"task"}]`
	const post = `[{"id":"baron-e1a2b3","title":"epic","status":"open","issue_type":"epic"},` +
		`{"id":"baron-e1a2b3.1","title":"child one","status":"closed","issue_type":"task"},` +
		`{"id":"baron-e1a2b3.2","title":"child two","status":"closed","issue_type":"task"}]`
	fr := epicCloseRunner(pre, post)
	a := newTestApp(t, fr)
	if err := closeBead(t, a, "baron-e1a2b3.2"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !hasCall(fr, "close", "baron-e1a2b3.2") {
		t.Fatalf("calls = %v, want bd close of the child", fr.calls)
	}
	if !hasCall(fr, "update", "baron-e1a2b3", "--status", "closed") {
		t.Fatalf("calls = %v, want bd update of the epic to closed", fr.calls)
	}
	if !strings.Contains(stdout(t, a), "epic baron-e1a2b3 auto-closed (all children closed)") {
		t.Fatalf("stdout = %q, want epic auto-close line", stdout(t, a))
	}
	var epicAudited bool
	for _, e := range auditEvents(t, a) {
		if e.Action == "close" && e.Target == "baron-e1a2b3" && strings.Contains(e.Detail, "auto-closed") {
			epicAudited = true
		}
	}
	if !epicAudited {
		t.Fatalf("audit = %+v, want an auto-close event on the epic", auditEvents(t, a))
	}
}

func TestWorkCloseNoAutoCloseWithOpenSibling(t *testing.T) {
	const pre = `[{"id":"baron-e1a2b3","title":"epic","status":"open","issue_type":"epic"},` +
		`{"id":"baron-e1a2b3.1","title":"child one","status":"open","issue_type":"task"},` +
		`{"id":"baron-e1a2b3.2","title":"child two","status":"open","issue_type":"task"}]`
	const post = `[{"id":"baron-e1a2b3","title":"epic","status":"open","issue_type":"epic"},` +
		`{"id":"baron-e1a2b3.1","title":"child one","status":"open","issue_type":"task"},` +
		`{"id":"baron-e1a2b3.2","title":"child two","status":"closed","issue_type":"task"}]`
	fr := epicCloseRunner(pre, post)
	a := newTestApp(t, fr)
	if err := closeBead(t, a, "baron-e1a2b3.2"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !hasCall(fr, "close", "baron-e1a2b3.2") {
		t.Fatalf("calls = %v, want the child itself closed", fr.calls)
	}
	if hasCall(fr, "update", "baron-e1a2b3", "--status", "closed") {
		t.Fatalf("calls = %v, want no epic close while a sibling is open", fr.calls)
	}
	if strings.Contains(stdout(t, a), "auto-closed") {
		t.Fatalf("stdout = %q, want no auto-close line", stdout(t, a))
	}
}

func TestWorkCloseEpicAlreadyClosed(t *testing.T) {
	const pre = `[{"id":"baron-e1a2b3","title":"epic","status":"closed","issue_type":"epic"},` +
		`{"id":"baron-e1a2b3.2","title":"child two","status":"open","issue_type":"task"}]`
	const post = `[{"id":"baron-e1a2b3","title":"epic","status":"closed","issue_type":"epic"},` +
		`{"id":"baron-e1a2b3.2","title":"child two","status":"closed","issue_type":"task"}]`
	fr := epicCloseRunner(pre, post)
	a := newTestApp(t, fr)
	if err := closeBead(t, a, "baron-e1a2b3.2"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !hasCall(fr, "close", "baron-e1a2b3.2") {
		t.Fatalf("calls = %v, want the child itself closed", fr.calls)
	}
	if hasCall(fr, "update", "baron-e1a2b3", "--status", "closed") {
		t.Fatalf("calls = %v, want a closed epic left untouched", fr.calls)
	}
}

func TestWorkClosePlainBeadNoEpicLogic(t *testing.T) {
	fr := epicCloseRunner("[]", "[]")
	a := newTestApp(t, fr)
	if err := closeBead(t, a, "baron-a1b2c3"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !hasCall(fr, "close", "baron-a1b2c3") {
		t.Fatalf("calls = %v, want plain close", fr.calls)
	}
	for _, call := range fr.calls {
		if len(call) > 0 && call[0] == "update" {
			t.Fatalf("calls = %v, want no update call for a plain bead", fr.calls)
		}
	}
}

// TestWorkCloseMissingParentSkipsAutoClose: a child whose dotted-id parent
// isn't in the bead list (deleted, or never existed) can't auto-close
// anything — the close itself must still succeed either way.
func TestWorkCloseMissingParentSkipsAutoClose(t *testing.T) {
	const pre = `[{"id":"baron-e1a2b3.2","title":"child two","status":"open","issue_type":"task"}]`
	const post = `[{"id":"baron-e1a2b3.2","title":"child two","status":"closed","issue_type":"task"}]`
	fr := epicCloseRunner(pre, post)
	a := newTestApp(t, fr)
	if code := closeBeadCode(t, a, "baron-e1a2b3.2"); code != ExitOK {
		t.Fatalf("exit = %d, want %d (a missing parent must not fail the close)", code, ExitOK)
	}
	if !hasCall(fr, "close", "baron-e1a2b3.2") {
		t.Fatalf("calls = %v, want the child closed despite the missing parent", fr.calls)
	}
	if hasCall(fr, "update", "baron-e1a2b3", "--status", "closed") {
		t.Fatalf("calls = %v, want no epic update when the parent can't be found", fr.calls)
	}
}
