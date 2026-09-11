package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	xansi "github.com/charmbracelet/x/ansi"

	"github.com/baron-cli/baron/internal/store"
)

// dateTimeLayout is the display format for a bead's created/updated
// timestamps, shared across the detail and split views.
const dateTimeLayout = "2006-01-02 15:04"

func (m Model) detailContent(w int) string {
	b := m.detail
	var out strings.Builder
	switch m.detailTab {
	case 0: // Overview: everything about the bead, comments last. The state
		// line above the tabs already carries status/assignee/priority/type
		// and the tab bar carries the dates — do not repeat them here.
		if len(b.Tags) > 0 {
			t := strings.Join(b.Tags, ", ")
			out.WriteString(m.styles.DetailLabel.Render("tags:"))
			out.WriteString(" ")
			out.WriteString(m.styles.DetailValue.Render(t))
			out.WriteString("\n")
		}
		for _, blk := range m.activeBlockers(b) {
			fmt.Fprintf(&out, "%s %s\n", m.styles.DetailLabel.Render("blocked by:"),
				m.styles.Error.Render(string(blk.BRN)+" "+blk.Title))
		}
		out.WriteString(m.statusBannerSection(b, w))
		if b.Description != "" {
			fmt.Fprintf(&out, "\n%s\n", m.sectionHead("▎ Description"))
			out.WriteString(m.wrap(w, b.Description))
			out.WriteString("\n")
		}
		if b.AcceptanceCriteria != "" {
			fmt.Fprintf(&out, "\n%s\n", m.sectionHead("▎ Acceptance"))
			out.WriteString(m.wrap(w, b.AcceptanceCriteria))
			out.WriteString("\n")
		}
		out.WriteString(m.commentsSection(w))
	// case 1 (Terminal) and case 2 (Diff) are never reached here:
	// viewSplitDetailPane short-circuits both to viewSessionPane before
	// this function is ever called for them — see its doc comment.
	case 3: // Audit: the bead's activity timeline, grouped by theme.
		m.renderAuditHistory(&out)
	}
	return out.String()
}

// statusBannerSection renders the one-line call-to-action banner for
// mergable/retry/human_queue beads. default is intentionally empty (every
// other status has nothing that needs its own banner here — the state line
// above the tabs already carries status/assignee/priority/type) but must
// stay an explicit case: .golangci.yml's exhaustive linter is configured
// with default-signifies-exhaustive, so dropping it would flag this switch
// as missing cases again.
func (m Model) statusBannerSection(b store.Bead, w int) string {
	var out strings.Builder
	switch b.Status {
	case store.BeadStatusMergable:
		target := m.mergeTargetForBead(b)
		fmt.Fprintf(&out, "\n%s\n", m.sectionHead("▎ Ready to Merge"))
		line := target.Branch
		if target.Base != "" {
			line += " → " + target.Base
		}
		out.WriteString(m.styles.DetailValue.Render(line) + "\n")
		out.WriteString(m.styles.Info.Render("m merge   c comment") + "\n")
	case store.BeadStatusRetry:
		fmt.Fprintf(&out, "\n%s\n", m.sectionHead("▎ Needs a Retry"))
		if reason := m.lastAuditReason(); reason != "" {
			out.WriteString(m.wrap(w, reason) + "\n")
		}
		out.WriteString(m.styles.Info.Render("r retry now   s cancel") + "\n")
	case store.BeadStatusHumanQueue:
		fmt.Fprintf(&out, "\n%s\n", m.sectionHead("▎ Needs You"))
		if reason := m.lastAuditReason(); reason != "" {
			out.WriteString(m.wrap(w, reason) + "\n")
		}
		out.WriteString(m.styles.Info.Render("r reassign and retry   s change status") + "\n")
	default:
		// Every other status (open/assigned/working/validating/blocked/
		// merged/closed/cancelled) has nothing that needs its own
		// call-to-action banner here.
	}
	return out.String()
}

// commentsSection renders the Overview tab's Comments block: a header, then
// either an empty/loading placeholder or the visible comment thread
// (collapsed to the last 3 unless expanded), each comment word-wrapped.
func (m Model) commentsSection(w int) string {
	var out strings.Builder
	out.WriteString("\n" + m.sectionHead("▎ Comments") + "\n")
	if len(m.comments) == 0 {
		if m.commentsLoading {
			out.WriteString(m.styles.EmptyState.Render("loading comments…"))
		} else {
			out.WriteString(m.styles.EmptyState.Render("no comments"))
		}
		return out.String()
	}
	shown := m.comments
	if !m.commentsExpanded && len(shown) > 3 {
		shown = shown[len(shown)-3:]
	}
	for _, c := range shown {
		fmt.Fprintf(&out, "%s (%s UTC)\n", m.styles.CardL1.Render(c.Author), c.CreatedAt.Format(dateTimeLayout))
		for line := range strings.SplitSeq(strings.TrimRight(c.Text, "\n"), "\n") {
			out.WriteString("  " + m.wrap(w-2, line))
			out.WriteString("\n")
		}
	}
	if !m.commentsExpanded && len(m.comments) > 3 {
		fmt.Fprintf(&out, "%s\n", m.styles.Muted.Render(fmt.Sprintf("+%d more — press u to expand", len(m.comments)-3)))
	} else if m.commentsExpanded {
		fmt.Fprintf(&out, "%s\n", m.styles.Muted.Render("— press u to collapse"))
	}
	return out.String()
}

// auditGroup is one themed bucket of the bead's audit trail, rendered under
// its own header so gates, runs and housekeeping never blur together.
type auditGroup struct {
	title string
	match func(store.AuditEvent) bool
}

// auditLine is one deduplicated entry of an audit group: identical events
// from the same actor collapse into a single line with a repeat count.
type auditLine struct {
	action, detail, actor string
	last                  time.Time
	count                 int
}

// auditKey collapses events that differ only in noise. run events drop their
// trailing "(duration)" — every run of a bead launches the same model, so the
// nanosecond-precision durations would keep dozens of near-identical lines
// from merging into one "launched <model> ×N". Actor is part of the key so
// the same movement by two different personas stays two lines.
func auditKey(ev store.AuditEvent) string {
	d := ev.Detail
	if ev.Action == "run" {
		if i := strings.Index(d, " ("); i > 0 {
			d = d[:i]
		}
	}
	return ev.Action + "\x00" + d + "\x00" + auditActor(ev.Actor)
}

// auditActor renders an event's actor for the audit timeline: the bare name
// for human events, "name(type)" for automation (persona/manager/agent), and
// "" for legacy events recorded before actors existed.
func auditActor(a store.Actor) string {
	switch {
	case a.Name == "":
		return ""
	case a.Type == "" || a.Type == store.ActorUser:
		return a.Name
	default:
		return a.Name + "(" + string(a.Type) + ")"
	}
}

// gateCheckRow is one named check's most recent outcome — the answer to
// "did this bead, right now, actually pass X", which the chronological
// timeline below doesn't give directly once a bead has retried and the same
// check name appears several times with different outcomes interleaved.
type gateCheckRow struct {
	name   string
	passed bool
	detail string // shown only when failed
}

// parseProfileGateChecks parses one "gate" audit event's flattened detail
// string — gateAuditDetail's own format in internal/cli/run.go, "gate
// passed: <profile> —name: exit N ok, name2: exit N2 FAIL" — into its
// individual named sub-checks (format/lint/tidy/test/build/... as configured
// by the project's profile). This is a plain string because the audit log
// itself is one flat Detail field per event (internal/store/audit.go); it's
// reparsed here rather than adding a structured field so old, already-logged
// events keep working without a migration.
func parseProfileGateChecks(detail string) []gateCheckRow {
	_, rest, ok := strings.Cut(detail, "—")
	if !ok {
		return nil
	}
	var rows []gateCheckRow
	for part := range strings.SplitSeq(rest, ",") {
		part = strings.TrimSpace(part)
		name, status, ok := strings.Cut(part, ": exit ")
		if !ok {
			continue
		}
		rows = append(rows, gateCheckRow{name: name, passed: strings.HasSuffix(status, " ok"), detail: "exit " + status})
	}
	return rows
}

// latestGateSummary builds the bead's current per-check pass/fail state: the
// profile gate's own newest sub-check breakdown, plus the newest secret_scan
// and signed_commit and review outcome, each contributing at most one row —
// a retried bead's older attempts naturally drop out since only the first
// (newest, events is newest-first here) occurrence of each source is used.
// "ask" is deliberately excluded: it's an observation ("the agent asked a
// question"), not a repeatable pass/fail check, and reads fine as prose in
// the timeline below.
func latestGateSummary(events []store.AuditEvent) []gateCheckRow {
	var rows []gateCheckRow
	haveGate, haveSecret, haveSigned, haveReview := false, false, false, false
	for _, ev := range slices.Backward(events) {
		switch {
		case ev.Action == "gate" && !haveGate:
			haveGate = true
			rows = append(rows, parseProfileGateChecks(ev.Detail)...)
		case ev.Action == "secret_scan" && !haveSecret:
			haveSecret = true
			rows = append(rows, gateCheckRow{name: "secret scan", passed: strings.Contains(ev.Detail, "passed"), detail: ev.Detail})
		case ev.Action == "signed_commit" && !haveSigned:
			haveSigned = true
			rows = append(rows, gateCheckRow{name: "signed commits", passed: strings.Contains(ev.Detail, "passed"), detail: ev.Detail})
		case ev.Action == "review" && !haveReview:
			haveReview = true
			rows = append(rows, gateCheckRow{name: "review", passed: strings.HasPrefix(ev.Detail, "review: pass"), detail: ev.Detail})
		}
		if haveGate && haveSecret && haveSigned && haveReview {
			break
		}
	}
	return rows
}

// renderGateSummary renders latestGateSummary as an aligned check/result
// table — the at-a-glance "which gates did this bead actually pass" the
// chronological "Gates & checks" group below still answers, just not
// quickly, since a retried bead interleaves several attempts' worth of
// pass/fail lines for the same check names.
func (m Model) renderGateSummary(out *strings.Builder) {
	rows := latestGateSummary(m.auditEvents)
	if len(rows) == 0 {
		return
	}
	nameWidth := 0
	for _, r := range rows {
		if len(r.name) > nameWidth {
			nameWidth = len(r.name)
		}
	}
	fmt.Fprintf(out, "\n%s\n", m.sectionHead("▎ Gate summary"))
	for _, r := range rows {
		mark, style := "✓ pass", m.styles.Success
		if !r.passed {
			mark, style = "✗ fail", m.styles.Error
		}
		line := fmt.Sprintf("  %-*s  %s", nameWidth, r.name, mark)
		if !r.passed && r.detail != "" {
			line += "  " + r.detail
		}
		out.WriteString(style.Render(line))
		out.WriteString("\n")
	}
}

// renderAuditHistory renders the Audit tab's activity timeline: first the
// gate summary table (current pass/fail per check), then the full
// chronological log grouped by theme, newest first, and deduplicated
// (repeated events show a ×N count instead of a wall of identical lines).
// Comment events are skipped: the Overview tab owns the full comment
// history (with timestamps), so repeating it here would just duplicate what
// the user already sees.
func (m Model) renderAuditHistory(out *strings.Builder) {
	m.renderGateSummary(out)
	groups := []auditGroup{
		{"Gates & checks", func(ev store.AuditEvent) bool {
			return ev.Action == "gate" || ev.Action == "ask" || ev.Action == "secret_scan" || ev.Action == "signed_commit"
		}},
		{"Runs", func(ev store.AuditEvent) bool {
			return ev.Action == "run" || ev.Action == "retry"
		}},
		{"Merge", func(ev store.AuditEvent) bool {
			return ev.Action == "mergable" || ev.Action == "merge" || ev.Action == "merge_auto"
		}},
		{"Lifecycle", func(ev store.AuditEvent) bool {
			switch ev.Action {
			case "assign", "status", "close", "dep_add", "init":
				return true
			}
			return false
		}},
		{"Persona runs", func(ev store.AuditEvent) bool {
			return ev.Action == "persona_run"
		}},
	}
	anyGroup := false
	for _, g := range groups {
		lines := buildAuditLines(m.auditEvents, g.match)
		if len(lines) == 0 {
			continue
		}
		anyGroup = true
		fmt.Fprintf(out, "\n%s\n", m.sectionHead("▎ "+g.title))
		for _, l := range lines {
			out.WriteString(m.renderAuditLine(l))
			out.WriteString("\n")
		}
	}
	if !anyGroup {
		fmt.Fprintf(out, "\n%s\n", m.styles.EmptyState.Render("no checks or runs recorded yet"))
	}
}

// buildAuditLines scans events newest-first, keeping one auditLine per
// distinct auditKey among those match selects — repeated events (same
// auditKey) collapse into a single line with an incrementing count instead
// of one line per occurrence.
func buildAuditLines(events []store.AuditEvent, match func(store.AuditEvent) bool) []auditLine {
	var lines []auditLine
	byKey := map[string]int{}
	for _, ev := range slices.Backward(events) {
		if !match(ev) {
			continue
		}
		key := auditKey(ev)
		if idx, ok := byKey[key]; ok {
			lines[idx].count++
			continue
		}
		byKey[key] = len(lines)
		lines = append(lines, auditLine{action: ev.Action, detail: ev.Detail, actor: auditActor(ev.Actor), last: ev.Time, count: 1})
	}
	return lines
}

// renderAuditLine formats one deduplicated audit-timeline entry — action,
// detail, actor (when known), and either a last-seen timestamp or a ×N
// repeat count with a "last <timestamp>" tail — then applies gate-specific
// pass/fail coloring.
func (m Model) renderAuditLine(l auditLine) string {
	line := fmt.Sprintf("  %s %s",
		m.styles.DetailLabel.Render(l.action),
		m.styles.DetailValue.Render(l.detail))
	if l.actor != "" {
		line += fmt.Sprintf("  %s", m.styles.Muted.Render(l.actor))
	}
	if l.count > 1 {
		line += fmt.Sprintf("  %s  %s",
			m.styles.Muted.Render(fmt.Sprintf("×%d", l.count)),
			m.styles.Muted.Render("last "+l.last.Format(dateTimeLayout)))
	} else {
		line += fmt.Sprintf("  %s",
			m.styles.Muted.Render(l.last.Format(dateTimeLayout)))
	}
	switch {
	case l.action == "gate" && strings.Contains(l.detail, "failed"):
		line = m.styles.Error.Render(line)
	case l.action == "gate":
		line = m.styles.Success.Render(line)
	}
	return line
}

// wrap wraps multiline detail fields to w cells (no-op for unknown or
// non-positive widths). lipgloss wraps on word boundaries, so prose never
// gets cut mid-word by a pane-width truncate.
func (m Model) wrap(w int, s string) string {
	if w <= 0 {
		return s
	}
	// Word-wrap with a hard break for words longer than the line:
	// lipgloss.MaxWidth only breaks on whitespace, so a long unbroken
	// word (URL, path, camelCase) would overflow the pane.
	var out strings.Builder
	for para := range strings.SplitSeq(s, "\n") {
		out.WriteString(wrapParagraph(para, w))
	}
	return strings.TrimRight(out.String(), "\n")
}

func wrapParagraph(para string, w int) string {
	if para == "" {
		return "\n"
	}
	var out strings.Builder
	cur, curW := "", 0
	flush := func() {
		if curW > 0 {
			out.WriteString(cur + "\n")
		}
		cur, curW = "", 0
	}
	for word := range strings.FieldsSeq(para) {
		ww := xansi.StringWidth(word)
		if ww > w {
			cur, curW = hardBreakWord(&out, word, w, cur, curW)
			continue
		}
		if curW > 0 && curW+1+ww > w {
			flush()
		}
		if curW > 0 {
			cur += " "
			curW++
		}
		cur += word
		curW += ww
	}
	flush()
	return out.String()
}

// hardBreakWord writes word into out as one or more w-wide chunks (rune
// boundaries only) — lipgloss's own word-wrap only breaks on whitespace, so
// an unbroken word longer than w (a URL, a path, a long identifier) would
// otherwise overflow the pane. Any content already pending in cur/curW is
// flushed first; the returned pending state is always ("", 0) afterward.
func hardBreakWord(out *strings.Builder, word string, w int, cur string, curW int) (string, int) {
	flush := func() {
		if curW > 0 {
			out.WriteString(cur + "\n")
		}
		cur, curW = "", 0
	}
	flush()
	for _, r := range word {
		rw := xansi.StringWidth(string(r))
		if curW+rw > w {
			flush()
		}
		cur += string(r)
		curW += rw
	}
	flush()
	return cur, curW
}

// viewHumanQueueV2 renders the human queue list (reachable via H), windowed
// to the pane height.
// viewMergePreflight renders the real preflight result
// (config check + merge-tree cleanliness), or a loading/error state.
