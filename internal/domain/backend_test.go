package domain

import (
	"strings"
	"testing"
)

// TestPromptNoHunkSteering: Prompt used to append a fixed "leave inline
// Hunk review notes" instruction to every agent invocation regardless of
// whether the bead needed it — removed (see Prompt's doc comment); this
// guards against it coming back.
func TestPromptNoHunkSteering(t *testing.T) {
	p := Prompt("Title", "Desc", "Accept this")
	if strings.Contains(p, "hunk") || strings.Contains(p, "Hunk") {
		t.Errorf("Prompt() = %q, want no Hunk steering section", p)
	}
	if !strings.Contains(p, "Title") || !strings.Contains(p, "Accept this") {
		t.Errorf("Prompt() = %q, want title/acceptance present", p)
	}
}
