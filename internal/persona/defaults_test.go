package persona

import "testing"

// TestMergeCloserShape locks in the closer persona: a synchronous
// gate (Trigger empty, Authority empty — the same shape as reviewer)
// invoked directly by internal/cli/merge_closer.go, never fired by Reconcile
// and never given bd write authority of its own.
func TestMergeCloserShape(t *testing.T) {
	all, err := embeddedPersonas()
	if err != nil {
		t.Fatalf("embeddedPersonas() error: %v", err)
	}
	var p Persona
	found := false
	for _, d := range all {
		if d.ID == MergeCloserID {
			p = d
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("embeddedPersonas() has no %q persona", MergeCloserID)
	}
	if err := p.Validate(); err != nil {
		t.Errorf("closer.Validate() = %v, want nil", err)
	}
	if p.Enabled {
		t.Error("closer.Enabled = true, want false (opt-in like every other builtin)")
	}
	if p.Source != SourceBuiltin {
		t.Errorf("closer.Source = %v, want SourceBuiltin", p.Source)
	}
	if len(p.Trigger.On) != 0 || p.Trigger.Schedule != "" {
		t.Errorf("closer.Trigger = %+v, want empty (manual/gate, never fired by Reconcile)", p.Trigger)
	}
	if p.Authority.BDWrite || len(p.Authority.Actions) != 0 {
		t.Errorf("closer.Authority = %+v, want empty — mergeCloseGate's own trusted code performs the close/reopen, not the persona's agent process", p.Authority)
	}
}

// TestBuiltinPersonaPromptsAreEnglishAndBulleted is a light sanity check,
// not a translation validator: every built-in Prompt must start with a
// bullet ("- ") and must not contain the handful of Turkish-specific
// letters that showed up in this package's prompts before this pass
// (ç/ğ/ı/ö/ş/ü) — a cheap tripwire against a prompt silently drifting back
// to Turkish prose on a future edit.
func TestBuiltinPersonaPromptsAreEnglishAndBulleted(t *testing.T) {
	all, err := embeddedPersonas()
	if err != nil {
		t.Fatalf("embeddedPersonas() error: %v", err)
	}
	const turkishLetters = "çğışöüÇĞİÖŞÜ"
	for _, p := range all {
		if len(p.Prompt) == 0 || p.Prompt[0] != '-' {
			t.Errorf("%s: Prompt doesn't start with a bullet (\"- \"): %q", p.ID, p.Prompt)
		}
		for _, r := range p.Prompt {
			for _, tr := range turkishLetters {
				if r == tr {
					t.Errorf("%s: Prompt contains %q, a Turkish-specific letter — prompts must be English", p.ID, r)
				}
			}
		}
	}
}
