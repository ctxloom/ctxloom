package cli

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// runNoStartupFindings backs --no-startup-findings: the opt-out for delivering
// this launch's startup findings into the started agent's context. Default ON
// is the honest choice — an agent should know the ground it stands on — and
// the opt-out exists for scripted/one-shot runs where the extra context is
// measured waste.
var runNoStartupFindings bool

// startupFindingsMarker is the DOCTOR-CHECK row under which the findings THIS
// launch recorded and proceeded past are delivered. It has no counterpart in
// `ctxloom doctor` because doctor is a separate process and cannot see what a
// run recorded; the run is the only place these rows can be produced.
const startupFindingsMarker = "DOCTOR-CHECK-STARTUP-FINDINGS-x4"

// startupFindingsFragmentName is the fragment identity the delivered report
// rides under (assembleDedupedContext keys on name AND content).
const startupFindingsFragmentName = "ctxloom-startup-findings"

// startupFindingsReport is what the started agent is told about the ground it
// stands on, scoped to THIS launch: the findings the launch itself recorded
// (config warnings, a degraded isolation axis, a sync or coordinator fault —
// everything a --degraded run proceeded past; in strict mode the gates already
// aborted on any of these, so the list is empty), plus doctor's own checks of
// the state this run reads — the marker and config validity, the companion
// decisions, and the local-only paths a fresh clone has no way to know it
// lacks. The rows ARE doctor's rows: same markers, same wording.
//
// Only what is NOT the intended state is a finding. An ok row is omitted; an
// info row is context, not a verdict, and is omitted too. The companions row
// is the exception that proves the rule: doctor reports it ok even when a
// companion was withheld, so it is selected on the decision itself.
func startupFindingsReport(cfg *config.Config, recorded []strictness.Finding) doctorReport {
	var checks []doctorCheck
	for _, f := range recorded {
		detail := "[" + string(f.Class) + "] " + f.Message
		if f.FixIt != "" {
			detail += " (fix: " + f.FixIt + ")"
		}
		checks = append(checks, doctorCheck{Marker: startupFindingsMarker, Status: doctorWarn, Detail: detail})
	}
	for _, c := range []doctorCheck{
		doctorCheckSetupMarker(cfg, nil),
		doctorCheckLocalTierState(cfg),
	} {
		if c.Status == doctorWarn {
			checks = append(checks, c)
		}
	}
	if !config.CompanionsDisabled() && readCompanionDecisions(cfg).withheld() {
		checks = append(checks, doctorCheckSetupCompanions(cfg, nil))
	}
	return doctorReport{Checks: checks}
}

// attachStartupFindings delivers the launch's startup findings into the
// RunStart the engine receives, as one more fragment after the assembled
// context — the same seam every other context source rides, so the SessionStart
// hook, the context cache file and each engine's own delivery differences are
// already solved. It runs after the workspace gate, because the isolation axis
// resolves there and a degraded-to-host finding is the case this exists for.
// Nothing is attached when there is nothing to say, or when
// --no-startup-findings opted out.
func (st *runState) attachStartupFindings() {
	if runNoStartupFindings {
		return
	}
	report := startupFindingsReport(st.cfg, strictness.Since(strictness.Mark{}))
	if len(report.Checks) == 0 {
		return
	}
	var b strings.Builder
	// renderDoctorReport's only error source is the writer, and a
	// strings.Builder never fails.
	_ = renderDoctorReport(&b, report)
	st.req.Fragments = append(st.req.Fragments, &pb.Fragment{
		Name:    startupFindingsFragmentName,
		Content: b.String(),
	})
}
