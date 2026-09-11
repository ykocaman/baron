package tui

import (
	"context"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// benchModel builds a Model with n live tmux-backed sessions, each carrying a
// realistic emulator (alt screen, a few thousand lines of history) — the shape
// the frame tick actually walks 24x/second in a long-running session.
func benchModel(n int) Model {
	m := New(context.Background(), testDeps())
	beads := make([]store.Bead, 0, n)
	for i := range n {
		beads = append(beads, store.Bead{BRN: domain.BRN(fmt.Sprintf("baron-%03d", i)), Title: "T", Status: store.BeadStatusOpen})
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 160, 48
	m.detailTab = 1
	m.detail = beads[0]
	m.liveBRN = string(beads[0].BRN)

	cols, rows := m.termDims()
	for i := range n {
		emu := vt.NewSafeEmulator(cols, rows)
		emu.SetScrollbackSize(5000)
		_, _ = emu.WriteString("\x1b[?1049h\x1b[2J\x1b[H")
		for j := range 3000 {
			_, _ = fmt.Fprintf(emu, "\x1b[32msome agent output line %d\x1b[0m with a bit of trailing text\r\n", j)
		}
		brn := string(beads[i].BRN)
		m.sessions[brn] = &agentTerminal{brn: brn, kind: kindAgent, emu: emu, tmuxWindow: brn}
	}
	return m
}

// BenchmarkEmuRender: the raw cost of one emulator full-screen render, the
// primitive both checkTmuxDone and viewEmbeddedTerminal call per session.
func BenchmarkEmuRender(b *testing.B) {
	m := benchModel(1)
	t := m.sessions["baron-000"]
	b.ResetTimer()
	for b.Loop() {
		_ = t.emu.Render()
	}
}

// BenchmarkCheckTmuxDoneRenderStrip: what the tick pays per session just to
// look for the done sentinel — a full render plus an ANSI strip of it.
func BenchmarkCheckTmuxDoneRenderStrip(b *testing.B) {
	m := benchModel(1)
	t := m.sessions["baron-000"]
	b.ResetTimer()
	for b.Loop() {
		_ = xansi.Strip(t.emu.Render())
	}
}

// BenchmarkTickUpdate1 / 10 / 30: the real per-frame Update cost as live
// sessions accumulate — 30 is what browsing 30 beads with the Terminal tab
// open leaves registered.
func BenchmarkTickUpdate1(b *testing.B)  { benchTick(b, 1) }
func BenchmarkTickUpdate10(b *testing.B) { benchTick(b, 10) }
func BenchmarkTickUpdate30(b *testing.B) { benchTick(b, 30) }

func benchTick(b *testing.B, n int) {
	m := benchModel(n)
	b.ResetTimer()
	for b.Loop() {
		next, _ := m.Update(tickMsg{})
		m = asModel(next)
	}
}

// BenchmarkViewFrame: one full TUI render with a live session on screen.
func BenchmarkViewFrame(b *testing.B) {
	m := benchModel(1)
	b.ResetTimer()
	for b.Loop() {
		_ = m.View()
	}
}

// BenchmarkKeypressRoundTrip: the latency a single keystroke actually sees —
// Update plus the View that follows it — which is what "typing lag" means.
func BenchmarkKeypressRoundTrip(b *testing.B) {
	m := benchModel(10)
	b.ResetTimer()
	for b.Loop() {
		next, _ := m.Update(tea.KeyPressMsg{Code: 'j'})
		m = asModel(next)
		_ = m.View()
	}
}
