package cli

import (
	"fmt"
	"strings"

	"github.com/baron-cli/baron/internal/domain"
)

func gateFailureSummary(report domain.GateReport) string {
	var b strings.Builder
	b.WriteString("The previous gate run failed:\n")
	for _, r := range report.Results {
		if r.Success {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): exit %d\n", r.Name, r.Command, r.ExitCode)
	}
	b.WriteString("Fix the failing checks so every gate step exits 0. Do not change the acceptance criteria.")
	return b.String()
}

// gateAuditDetail summarizes a gate report for the audit log as a
// pipeline listing: one entry per step with its exit code.
func gateAuditDetail(report domain.GateReport) string {
	var b strings.Builder
	if report.Success {
		fmt.Fprintf(&b, "gate passed: %s —", report.Profile)
	} else {
		fmt.Fprintf(&b, "gate failed: %s —", report.Profile)
	}
	for i, r := range report.Results {
		if i > 0 {
			b.WriteString(", ")
		}
		state := "ok"
		if !r.Success {
			state = "FAIL"
		}
		fmt.Fprintf(&b, "%s: exit %d %s", r.Name, r.ExitCode, state)
	}
	return b.String()
}

// toGateReportJSON converts a domain.GateReport to its JSON shape.
func toGateReportJSON(brn domain.BRN, report domain.GateReport) gateReportJSON {
	steps := make([]gateStepResultJSON, 0, len(report.Results))
	for _, r := range report.Results {
		steps = append(steps, gateStepResultJSON{
			Name:     r.Name,
			Command:  r.Command,
			Success:  r.Success,
			ExitCode: r.ExitCode,
			Output:   r.Output,
			Duration: r.Duration.String(),
		})
	}
	return gateReportJSON{BRN: brn, Profile: report.Profile, Success: report.Success, Steps: steps}
}

// printGateReport renders a gate report as text: one line per step with
// its exit code, and an aggregate verdict. Step output is deliberately not
// shown — the gate already ran in the worktree; the screen only needs the
// name and exit code.
func (a *app) printGateReport(report domain.GateReport) {
	for _, r := range report.Results {
		status := "pass"
		if !r.Success {
			status = "FAIL"
		}
		a.outf("  %-12s %-4s exit %d (%s)\n", r.Name, status, r.ExitCode, r.Duration)
	}
	if report.Success {
		a.outf("gate passed (%s)\n", report.Duration)
	} else {
		a.outf("gate failed (%s)\n", report.Duration)
	}
}
