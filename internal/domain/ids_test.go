package domain

import "testing"

func TestBRNParts(t *testing.T) {
	b := BRN("baron-a1b2c3")
	if got := b.Repo(); got != "baron" {
		t.Errorf("Repo() = %q, want %q", got, "baron")
	}
	if got := b.Seq(); got != "a1b2c3" {
		t.Errorf("Seq() = %q, want %q", got, "a1b2c3")
	}
	if got := b.String(); got != "baron-a1b2c3" {
		t.Errorf("String() = %q, want %q", got, "baron-a1b2c3")
	}
}

func TestBRNPartsMultiHyphenRepo(t *testing.T) {
	b := BRN("my-project-00ff42")
	if got := b.Repo(); got != "my-project" {
		t.Errorf("Repo() = %q, want %q", got, "my-project")
	}
	if got := b.Seq(); got != "00ff42" {
		t.Errorf("Seq() = %q, want %q", got, "00ff42")
	}
}
