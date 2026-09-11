package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/baron-cli/baron/internal/store"
)

// statusBucket is the coarse "do I need to care?" signal BARON's 11 bead
// statuses collapse into. Color carries this signal; statusGlyph (below)
// carries the finer per-status detail; the exact status name is the finest
// channel, shown as text in the detail pane.
type statusBucket int

const (
	bucketIdle statusBucket = iota
	bucketActive
	bucketNeedsYou
	bucketDone
	bucketDoneDim // terminal but not a success (closed, cancelled)
)

// statusBuckets maps each bead status to its coarse bucket.
var statusBuckets = map[string]statusBucket{
	"open":        bucketIdle,
	"blocked":     bucketIdle,
	"assigned":    bucketActive,
	"working":     bucketActive,
	"in_progress": bucketActive,
	"validating":  bucketActive,
	"retry":       bucketActive,
	// mergable sits in boardColumns' "Needs You" tab alongside human_queue
	// (gate passed, waiting on a human decision, nothing running) — its color
	// must match that, not bucketActive, or a bead needing your approval
	// reads amber ("something's in motion") instead of red ("needs you").
	"mergable":    bucketNeedsYou,
	"human_queue": bucketNeedsYou,
	"merged":      bucketDone,
	"closed":      bucketDoneDim,
	"cancelled":   bucketDoneDim,
}

// ansi resolves an ANSI 0-15 color code, terminal-theme-adaptive by
// construction: the code is fixed, but each terminal's own curated palette
// decides its actual RGB for both light and dark themes — no separate
// light/dark hex pairs needed.
func ansi(code string) color.Color { return lipgloss.Color(code) }

// The reduced 5-hue palette. Magenta/purple/pink/orange are deliberately
// absent: eleven distinct hues is why the old palette read as "confusing"
// — the eye can't build a stable color->meaning mapping.
const (
	ansiAccent = "6" // cyan: chrome accent, focus, selection — never a data value
	ansiGreen  = "2" // done-and-good
	ansiAmber  = "3" // in-flight / attention-soon
	ansiRed    = "1" // failed / needs-human / errors
	ansiDim    = "8" // inactive, metadata, unfocused chrome
)

// styles holds the rendered lipgloss styles for one theme. NO_COLOR/TERM=dumb
// disables color entirely by using a styles set with no foreground/background
// set ("NO_COLOR is honored").
type styles struct {
	light   bool
	noColor bool

	// reselectSeq is CardSelected's raw SGR start sequence (e.g. "\x1b[1;7m"
	// for bold+reverse), derived from the style itself rather than
	// hardcoded — see reapplySelection in view.go, which needs it to
	// restore the selection's reverse-video after an inner styled segment
	// (a colored type glyph, etc.) resets it mid-row.
	reselectSeq string

	// retained semantics
	Muted   lipgloss.Style
	Success lipgloss.Style
	Error   lipgloss.Style
	Warn    lipgloss.Style
	Info    lipgloss.Style
	Accent  lipgloss.Style

	// chrome
	App          lipgloss.Style
	HeaderBar    lipgloss.Style
	HeaderProj   lipgloss.Style
	HeaderStat   lipgloss.Style
	HeaderVal    lipgloss.Style
	ScreenTitle  lipgloss.Style
	CardL1       lipgloss.Style
	CardSelected lipgloss.Style
	CardSelBar   lipgloss.Style
	ListBox      lipgloss.Style
	ListCursor   lipgloss.Style
	TabActive    lipgloss.Style
	TabInactive  lipgloss.Style
	DetailLabel  lipgloss.Style
	DetailValue  lipgloss.Style
	DetailHead   lipgloss.Style
	SectionHead  lipgloss.Style
	OverlayBox   lipgloss.Style
	OverlayTitle lipgloss.Style
	EmptyState   lipgloss.Style
	FooterHint   lipgloss.Style
	FooterKey    lipgloss.Style
	FooterCmd    lipgloss.Style
	CmdPrompt    lipgloss.Style
	SearchPrompt lipgloss.Style
	LivePane     lipgloss.Style
	LiveTitle    lipgloss.Style
	ToastError   lipgloss.Style
	ToastNotice  lipgloss.Style
	ShellOutput  lipgloss.Style // inline output panel above the command bar
}

// color resolves an ANSI color code; noColor returns "" so no Foreground
// escape is ever emitted (TestModelNoColorProducesNoANSI).
func (s styles) color(code string) color.Color {
	if s.noColor {
		return lipgloss.NoColor{}
	}
	return ansi(code)
}

// newStyles builds the style set for theme ("dark" or "light"); noColor
// strips all color, keeping only structural styling (bold/borders/reverse).
// The 5-hue palette is ANSI-adaptive, so theme mostly only affects whether
// default terminal foreground is used as-is (both themes resolve the same
// ANSI codes through the terminal's own palette).
func newStyles(theme string, noColor bool) styles {
	light := theme == "light"
	s := styles{light: light, noColor: noColor}

	// lipgloss v2's Style.Render has no terminal-detecting default renderer
	// (v1's did, degrading to Ascii off a TTY) — Bold/Italic/Reverse must be
	// gated by hand here or NO_COLOR/tests still get SGR bytes for them.
	bold := func() lipgloss.Style {
		st := lipgloss.NewStyle()
		if !noColor {
			st = st.Bold(true)
		}
		return st
	}
	italic := func() lipgloss.Style {
		st := lipgloss.NewStyle()
		if !noColor {
			st = st.Italic(true)
		}
		return st
	}

	s.App = lipgloss.NewStyle()
	s.HeaderBar = bold()
	s.HeaderProj = bold().Foreground(s.color(ansiAccent))
	s.HeaderStat = lipgloss.NewStyle().Foreground(s.color(ansiDim))
	s.HeaderVal = bold()
	s.ScreenTitle = bold()
	s.CardL1 = bold()
	// Selection is the only background fill in the body:
	// reverse-video instead of a hardcoded highlight hex, so it reads
	// correctly against any terminal background.
	s.CardSelected = bold()
	if !noColor {
		s.CardSelected = s.CardSelected.Reverse(true)
	}
	s.reselectSeq = styleStartSeq(s.CardSelected)
	s.CardSelBar = lipgloss.NewStyle().Foreground(s.color(ansiAccent))
	s.ListBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if !noColor {
		s.ListBox = s.ListBox.BorderForeground(s.color(ansiDim))
	}
	s.ListCursor = bold().Foreground(s.color(ansiAccent))
	s.TabActive = bold().Foreground(s.color(ansiAccent))
	s.TabInactive = lipgloss.NewStyle().Foreground(s.color(ansiDim))
	s.DetailLabel = lipgloss.NewStyle().Foreground(s.color(ansiDim))
	s.DetailValue = lipgloss.NewStyle()
	s.DetailHead = bold()
	// Section headers use bold cyan text (no background bar) so right-pane
	// regions stay distinct without the loud full-width block.
	s.SectionHead = bold().Foreground(s.color(ansiAccent))
	// Width is set per-render by Model.overlayBox() to fit the terminal.
	s.OverlayBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
	s.OverlayTitle = bold().Foreground(s.color(ansiAccent))
	if !noColor {
		s.OverlayBox = s.OverlayBox.BorderForeground(s.color(ansiAccent))
	}
	s.EmptyState = italic().Foreground(s.color(ansiDim))
	s.FooterHint = lipgloss.NewStyle().Foreground(s.color(ansiDim))
	s.FooterKey = bold()
	s.FooterCmd = bold().Foreground(s.color(ansiAccent))
	s.CmdPrompt = bold().Foreground(s.color(ansiAccent))
	s.SearchPrompt = bold().Foreground(s.color(ansiAccent))
	s.LivePane = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if !noColor {
		s.LivePane = s.LivePane.BorderForeground(s.color(ansiDim))
	}
	s.LiveTitle = bold()

	// Toasts: a floating, bordered box (see viewToast/viewWithToast) so a
	// transient error/status notice reads as a distinct notification
	// instead of a plain color-only line that blends into everything else
	// on screen and vanishes outright under NO_COLOR.
	s.ToastError = bold().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	s.ToastNotice = bold().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if !noColor {
		s.ToastError = s.ToastError.Foreground(s.color(ansiRed)).BorderForeground(s.color(ansiRed))
		s.ToastNotice = s.ToastNotice.Foreground(s.color(ansiAccent)).BorderForeground(s.color(ansiAccent))
	}

	s.ShellOutput = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if !noColor {
		s.ShellOutput = s.ShellOutput.BorderForeground(s.color(ansiDim))
	}

	s.Muted = lipgloss.NewStyle().Foreground(s.color(ansiDim))
	s.Success = lipgloss.NewStyle().Foreground(s.color(ansiGreen))
	s.Error = lipgloss.NewStyle().Foreground(s.color(ansiRed))
	s.Warn = lipgloss.NewStyle().Foreground(s.color(ansiAmber))
	s.Info = lipgloss.NewStyle().Foreground(s.color(ansiAccent)) // no separate "info" hue in the 5-hue palette
	s.Accent = lipgloss.NewStyle().Foreground(s.color(ansiAccent))
	return s
}

// styleStartSeq returns the raw SGR "start" escape sequence s.Render emits
// before its content (e.g. "\x1b[1;7m" for bold+reverse) — "" when the style
// carries no styling (NO_COLOR, or a style with nothing set). lipgloss v2
// resets with a bare "\x1b[m", not "\x1b[0m", so this walks bytes rather
// than assuming any particular reset spelling.
func styleStartSeq(s lipgloss.Style) string {
	rendered := s.Render("\x00")
	if before, _, ok := strings.Cut(rendered, "\x00"); ok {
		return before
	}
	return ""
}

// statusStyle returns the style for a bead status string, colored by its
// coarse bucket rather than a unique per-status hue —
// statusGlyph carries the fine-grained distinction instead. Two per-status
// overrides exist because the bucket alone would collide: open must not
// read as the same gray as a closed bead (open = waiting, visible work),
// and blocked is the one state that demands red even though it buckets as
// idle.
func (s styles) statusStyle(status string) lipgloss.Style {
	if s.noColor {
		return lipgloss.NewStyle()
	}
	switch status {
	case "open":
		return lipgloss.NewStyle().Foreground(s.color(ansiAccent))
	case "blocked":
		return lipgloss.NewStyle().Foreground(s.color(ansiRed))
	}
	switch statusBuckets[status] {
	case bucketActive:
		return lipgloss.NewStyle().Foreground(s.color(ansiAmber))
	case bucketNeedsYou:
		return lipgloss.NewStyle().Foreground(s.color(ansiRed))
	case bucketDone:
		return lipgloss.NewStyle().Foreground(s.color(ansiGreen))
	default: // bucketIdle, bucketDoneDim
		return lipgloss.NewStyle().Foreground(s.color(ansiDim))
	}
}

// priorityStyle colors a bead priority by criticality so it reads at a
// glance instead of blending into the muted detail text. Only 4 tones exist
// for 5 levels (see the palette's own 5-tone rule, TUI.md §3) — P4/P5 share
// dim — but the label itself is always the literal "P4"/"P5" text, so
// nothing is lost: color reinforces urgency here, it doesn't carry the only
// signal the way a bare glyph would.
func (s styles) priorityStyle(p store.Priority) lipgloss.Style {
	if s.noColor {
		return lipgloss.NewStyle()
	}
	switch p {
	case store.PriorityP1:
		return lipgloss.NewStyle().Foreground(s.color(ansiRed)).Bold(true)
	case store.PriorityP2:
		return lipgloss.NewStyle().Foreground(s.color(ansiAmber))
	case store.PriorityP3:
		return lipgloss.NewStyle()
	default: // P4, P5, and anything unrecognized
		return lipgloss.NewStyle().Foreground(s.color(ansiDim))
	}
}

// typeInfo is the single definition of a bead issue type's tree glyph and
// color; every renderer pulls from typeInfos so icons and hues can never
// drift apart. An empty color means the default foreground.
type typeInfo struct {
	glyph string
	color string
}

// typeInfos is the one place a bead issue type's icon and color are declared.
// epic's glyph used to be ⬢ (hexagon) — renders as a tofu box in a real
// terminal font (see statusGlyph's doc comment for how this was found and
// confirmed); ▲ is from the same confirmed-safe set. feature/bug were ✦/✖
// (also present on an older confirmed-safe list) until a fresh freeze render
// showed both as tofu too — that list was never re-verified after whatever
// font/freeze-version it was built against changed; ⚡/■ replaced them after
// checking a new candidate batch through the same freeze pipeline. Verify
// empirically before trusting either list again — see the visual-debugging
// memory.
var typeInfos = map[string]typeInfo{
	"epic":     {glyph: "▲", color: ansiAmber},
	"feature":  {glyph: "⚡", color: ansiGreen},
	"task":     {glyph: "◈"},
	"bug":      {glyph: "■", color: ansiRed},
	"chore":    {glyph: "◇", color: ansiDim},
	"decision": {glyph: "◆", color: ansiAccent},
}

// typeGlyph returns the tree glyph for a bead issue type (default "·").
// All glyphs are single-width so columns stay aligned.
func typeGlyph(t string) string {
	if ti, ok := typeInfos[t]; ok {
		return ti.glyph
	}
	return "·"
}

// typeStyle colors the type's glyph and name. task keeps the default
// foreground: it is the common case, and contrast makes the rarer kinds
// stand out.
func (s styles) typeStyle(t string) lipgloss.Style {
	if s.noColor {
		return lipgloss.NewStyle()
	}
	color := typeInfos[t].color
	if color == "" {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(s.color(color))
}

// statusGlyph returns a color-independent symbol for a status, so meaning
// survives NO_COLOR (symbols/icons beyond color).
// All glyphs are single-width.
//
// Picked from a set actually confirmed to render (not just "exists in
// Unicode") — assigned/validating/retry/blocked used to be ◔/◍/↻/⊘, which
// render as a tofu box in a real terminal font (found by piping a captured
// render through freeze and looking at the image; none of these show up as
// broken in the raw ANSI text a string-matching test reads). Circular-arrow
// glyphs in particular (↻ and every ↺/⟲/⟳ candidate tried in their place)
// are missing across the fonts tested — avoid that family entirely for any
// future glyph here.
func statusGlyph(status string) string {
	switch status {
	case "open":
		return "○"
	case "assigned":
		return "⊙"
	case "working", "in_progress":
		return "●"
	case "validating":
		return "◎"
	case "retry":
		return "▶"
	case "blocked":
		return "⊗"
	case "mergable":
		return "↑"
	case "human_queue":
		return "!"
	case "merged":
		return "✓"
	case "closed":
		return "✕"
	case "cancelled":
		return "✗"
	default:
		return "●"
	}
}
