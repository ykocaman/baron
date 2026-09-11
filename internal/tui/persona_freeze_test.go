package tui

// Freeze-capture evidence for the persona → bead movement E2E: the
// dashboard's Audit tab before and after a persona reopens a merged bead.
// Artifacts land in /tmp/baron-persona-e2e (txt + PNG via the external
// `freeze` CLI); freeze failures are logged, not fatal.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/store"
)

func TestCapturePersonaReopenE2EWithFreeze(t *testing.T) {
	outDir := "/tmp/baron-persona-e2e"
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", outDir, err)
	}

	capture := func(name string, status store.BeadStatus, events []store.AuditEvent) {
		m := New(context.Background(), testDeps())
		bead := store.Bead{
			BRN:                "baron-f1",
			ID:                 "f1",
			IssueType:          "bug",
			Title:              "Fix login race on session refresh",
			Description:        "Concurrent refresh corrupts the stored session token.",
			AcceptanceCriteria: "Login succeeds under parallel refresh load.",
			Priority:           store.PriorityP1,
			Status:             status,
		}
		next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{bead}})
		m = asModel(next)
		m.width, m.height = 105, 32
		m.auditEvents = events
		m.detailTab = 3 // Audit tab — shows exactly who moved the bead

		content := stripANSI(m.View().Content)
		lines := strings.Split(content, "\n")
		var cleaned []string
		for _, l := range lines {
			cleaned = append(cleaned, strings.TrimRight(l, " "))
		}
		txtPath := filepath.Join(outDir, name+".txt")
		if err := os.WriteFile(txtPath, []byte(strings.Join(cleaned, "\n")), 0o600); err != nil {
			t.Logf("write %s: %v", txtPath, err)
		}
		pngPath := filepath.Join(outDir, name+".png")
		fcmd := exec.CommandContext(context.Background(), "freeze", txtPath, "--language", "text", "--window", "-o", pngPath)
		if fOut, fErr := fcmd.CombinedOutput(); fErr != nil {
			t.Logf("freeze %s err: %v (%s)", name, fErr, fOut)
		} else {
			t.Logf("Saved freeze screenshot: %s", pngPath)
		}
	}

	base := time.Now()
	pre := []store.AuditEvent{
		{Time: base.Add(-4 * time.Minute), Actor: store.Actor{Type: store.ActorUser, Name: "yusuf"}, Action: "status", Target: "baron-f1", Detail: "open -> mergable"},
		{Time: base.Add(-3 * time.Minute), Actor: store.Actor{Type: store.ActorManager, Name: "baron"}, Action: "status", Target: "baron-f1", Detail: "mergable -> merged"},
		{Time: base.Add(-2 * time.Minute), Actor: store.Actor{Type: store.ActorPersona, Name: "qa"}, Action: "persona_run", Target: "baron-f1", Detail: "reacted to mergable→merged"},
	}
	post := append(slices.Clone(pre), store.AuditEvent{
		Time: base.Add(-1 * time.Minute), Actor: store.Actor{Type: store.ActorPersona, Name: "qa"}, Action: "status", Target: "baron-f1", Detail: "merged -> open",
	})

	capture("persona_e2e_pre_reopen", store.BeadStatusMerged, pre)
	capture("persona_e2e_post_reopen", store.BeadStatusOpen, post)
}
