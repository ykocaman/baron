package persona

import (
	"reflect"
	"strings"
	"testing"
)

func TestMarkdownRoundTrip(t *testing.T) {
	p := Persona{
		ID:          "closer",
		Name:        "Closer",
		Description: "Re-checks a bead's merged change.",
		Prompt:      "- First bullet.\n- Second bullet, with a trailing thought.",
		Model:       Model{Tier: "standard"},
		Trigger:     Trigger{On: []TransitionRule{{From: "*", To: "merged"}}},
		Authority:   Authority{},
		Enabled:     false,
		Source:      SourceBuiltin,
		Skills:      []string{"pdf", "webapp-testing"},
	}
	data, err := MarshalMD(p)
	if err != nil {
		t.Fatalf("MarshalMD() error: %v", err)
	}
	got, err := UnmarshalMD(data)
	if err != nil {
		t.Fatalf("UnmarshalMD() error: %v\ndata:\n%s", err, data)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("round trip mismatch:\ngot:  %+v\nwant: %+v\ndata:\n%s", got, p, data)
	}
}

func TestMarkdownPromptIsVerbatim(t *testing.T) {
	p := Persona{ID: "x", Name: "X", Prompt: "- line one\n- line two\n\n  indented under a bullet\n- line three"}
	data, err := MarshalMD(p)
	if err != nil {
		t.Fatalf("MarshalMD() error: %v", err)
	}
	got, err := UnmarshalMD(data)
	if err != nil {
		t.Fatalf("UnmarshalMD() error: %v", err)
	}
	if got.Prompt != p.Prompt {
		t.Errorf("Prompt = %q, want %q (verbatim round trip)", got.Prompt, p.Prompt)
	}
}

func TestMarkdownMissingDelimiters(t *testing.T) {
	if _, err := UnmarshalMD([]byte("no frontmatter here at all")); err == nil {
		t.Error("UnmarshalMD() error = nil, want an error for missing opening delimiter")
	}
	if _, err := UnmarshalMD([]byte("---\nid: x\nname: X\n")); err == nil {
		t.Error("UnmarshalMD() error = nil, want an error for missing closing delimiter")
	}
}

func TestMarkdownHumanReadableShape(t *testing.T) {
	p := Persona{ID: "qa-chromium", Name: "QA (Chromium)", Prompt: "- Check it.", Model: Model{Tier: "fast"}}
	data, err := MarshalMD(p)
	if err != nil {
		t.Fatalf("MarshalMD() error: %v", err)
	}
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		t.Errorf("file doesn't start with the frontmatter delimiter:\n%s", s)
	}
	if strings.Contains(s, `"prompt"`) || strings.Contains(s, `\n`) {
		t.Errorf("file looks JSON-escaped, not plain markdown:\n%s", s)
	}
	if !strings.Contains(s, "- Check it.") {
		t.Errorf("prompt body not found verbatim in the file:\n%s", s)
	}
}
