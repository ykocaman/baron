package tui

import (
	"strings"
	"testing"
	"unicode"
)

// uppercaseExceptions are keys the no-uppercase invariant doesn't apply to:
// G (the universal vim jump-to-bottom idiom, always paired with g/top,
// unlike the case-pairs this keymap explicitly eliminated where uppercase
// meant something unrelated, like old r/R); P (Prompt Mode's own toggle,
// deliberately shift-cased so it reads as a heavier, deliberate mode
// switch distinct from lowercase p's fast bead-anchored dispatch — the one
// uppercase/lowercase pair in this keymap where the two are genuinely
// related actions at two different weights, not an old-r/R-style
// unrelated-meaning collision).
var uppercaseExceptions = map[string]bool{"G": true, "P": true}

// TestNoUppercaseExceptG: no uppercase letter key may exist except the
// documented exceptions. A shift-modified letter that
// means something unrelated to its lowercase counterpart is exactly the
// confusion this keymap redesign eliminated (old r/R, s/S, m/M).
func TestNoUppercaseExceptG(t *testing.T) {
	for _, b := range bindings {
		for part := range strings.SplitSeq(b.key, "/") {
			if uppercaseExceptions[part] {
				continue
			}
			for _, r := range part {
				if unicode.IsUpper(r) {
					t.Errorf("binding %+v: key part %q has an uppercase letter other than G", b, part)
				}
			}
		}
	}
}

// TestNoConflictingVerbsPerScope: within a single scope, a key must resolve
// to exactly one verb. (Different scopes may reuse a key for different
// things — that's the whole point of scoping — but two entries for the same
// key+scope with different verbs would mean the registry itself disagrees
// with what the handler does.)
func TestNoConflictingVerbsPerScope(t *testing.T) {
	type sk struct {
		scope bindingScope
		key   string
	}
	seen := map[sk]string{}
	for _, b := range bindings {
		k := sk{b.scope, b.key}
		if prevVerb, ok := seen[k]; ok && prevVerb != b.verb {
			t.Errorf("key %q in scope %q resolves to both %q and %q", b.key, b.scope, prevVerb, b.verb)
		}
		seen[k] = b.verb
	}
}

// TestBindingsHaveHelp: every binding must carry a non-empty help
// description — the generated help screen has nothing else to show.
func TestBindingsHaveHelp(t *testing.T) {
	for _, b := range bindings {
		if b.help == "" {
			t.Errorf("binding %+v has no help text", b)
		}
	}
}

func TestNoHL_OverloadPerFocus(t *testing.T) {
	for _, b := range bindings {
		if b.scope == scopePrompt && (b.key == "h/l" || b.key == "h" || b.key == "l") {
			t.Fatalf("binding %+v: Prompt agent tabs must use ctrl+h/ctrl+l, not %q (h/l overload per focus)", b, b.key)
		}
	}
	foundCtrl := false
	for _, b := range bindings {
		if b.scope == scopePrompt && (b.key == "ctrl+h/ctrl+l" || b.key == "ctrl+h" || b.key == "ctrl+l") {
			foundCtrl = true
		}
	}
	if !foundCtrl {
		t.Fatalf("no ctrl+h/ctrl+l binding in Prompt scope — Prompt agent tabs must be ctrl+h/l")
	}
}

func TestGlobalPlusMinus_AutoToggleRegistered(t *testing.T) {
	hasPlus, hasMinus := false, false
	for _, b := range bindings {
		if b.scope == scopeGlobal && (b.key == "+" || b.key == "+/-") {
			hasPlus = true
		}
		if b.scope == scopeGlobal && (b.key == "-" || b.key == "+/-") {
			hasMinus = true
		}
		if b.scope == scopeGlobal && b.key == "+/-" {
			hasPlus, hasMinus = true, true
		}
	}
	if !hasPlus || !hasMinus {
		t.Fatalf("global +/- auto-toggle not registered (hasPlus=%v hasMinus=%v) — need scopeGlobal bindings for +/- auto-toggle", hasPlus, hasMinus)
	}
}
