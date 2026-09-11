package tui

import (
	"testing"
	"time"

	"github.com/baron-cli/baron/internal/store"
)

func TestParseProfileGateChecks(t *testing.T) {
	detail := "gate passed: go —format: exit 0 ok, lint: exit 0 ok, tidy: exit 0 ok, test: exit 0 ok, build: exit 0 ok"
	rows := parseProfileGateChecks(detail)
	want := []gateCheckRow{
		{name: "format", passed: true, detail: "exit 0 ok"},
		{name: "lint", passed: true, detail: "exit 0 ok"},
		{name: "tidy", passed: true, detail: "exit 0 ok"},
		{name: "test", passed: true, detail: "exit 0 ok"},
		{name: "build", passed: true, detail: "exit 0 ok"},
	}
	if len(rows) != len(want) {
		t.Fatalf("parseProfileGateChecks() = %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, r := range rows {
		if r != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, r, want[i])
		}
	}
}

func TestParseProfileGateChecksMixedPassFail(t *testing.T) {
	detail := "gate failed: go —format: exit 0 ok, lint: exit 1 FAIL"
	rows := parseProfileGateChecks(detail)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	if !rows[0].passed {
		t.Errorf("format row passed = false, want true")
	}
	if rows[1].passed {
		t.Errorf("lint row passed = true, want false (FAIL)")
	}
}

func TestParseProfileGateChecksNoDashReturnsNil(t *testing.T) {
	if rows := parseProfileGateChecks("not a gate detail string"); rows != nil {
		t.Errorf("rows = %+v, want nil for a detail with no em-dash separator", rows)
	}
}

func TestLatestGateSummaryUsesNewestPerCheckKind(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []store.AuditEvent{
		// oldest-first, matching AuditStore.Query's own ordering
		{Time: base, Action: "gate", Detail: "gate failed: go —lint: exit 1 FAIL"},
		{Time: base.Add(time.Minute), Action: "secret_scan", Detail: "secret scan passed: no findings"},
		{Time: base.Add(2 * time.Minute), Action: "gate", Detail: "gate passed: go —lint: exit 0 ok"},
		{Time: base.Add(3 * time.Minute), Action: "signed_commit", Detail: "signed commit check passed: all commits signed"},
	}
	rows := latestGateSummary(events)

	byName := map[string]gateCheckRow{}
	for _, r := range rows {
		byName[r.name] = r
	}
	if lint, ok := byName["lint"]; !ok || !lint.passed {
		t.Errorf("lint row = %+v, want the newest (passing) gate event's result, not the older failing one", byName["lint"])
	}
	if secret, ok := byName["secret scan"]; !ok || !secret.passed {
		t.Errorf("secret scan row = %+v, want passed", byName["secret scan"])
	}
	if signed, ok := byName["signed commits"]; !ok || !signed.passed {
		t.Errorf("signed commits row = %+v, want passed", byName["signed commits"])
	}
}

func TestLatestGateSummaryEmptyWithNoEvents(t *testing.T) {
	if rows := latestGateSummary(nil); rows != nil {
		t.Errorf("rows = %+v, want nil for no audit events", rows)
	}
}

func TestLatestGateSummaryExcludesAsk(t *testing.T) {
	events := []store.AuditEvent{
		{Action: "ask", Detail: "agent asked a question — parked in the human queue (agent-exited)"},
	}
	if rows := latestGateSummary(events); rows != nil {
		t.Errorf("rows = %+v, want nil — 'ask' is an observation, not a repeatable pass/fail check", rows)
	}
}
