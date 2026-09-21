package cli

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// runNoStartupFindings backs --no-startup-findings: the opt-out for delivering
// this launch's startup findings into the started agent's context. Default ON
// is the honest choice — an agent should know the ground it stands on — and
// the opt-out exists for scripted/one-shot runs where the extra context is
// measured waste.
var runNoStartupFindings bool

// startupFindingsFragmentName is the fragment identity the delivered report
// rides under (assembleDedupedContext keys on name AND content).
const startupFindingsFragmentName = "ctxloom-startup-findings"

// startupFindings is the launch's startup-findings lead: one more context
// block after the assembled context — the same seam every other context
// source rides, so the SessionStart hook, the context cache file and each
// engine's own delivery differences are already solved. Composed after the
// launch resolved, because the isolation axis resolves there and a
// degraded-to-host finding is the case this exists for. Nothing when there
// is nothing to say, or when --no-startup-findings opted out.
func (st *runState) startupFindings() []composite.Fragment {
	if runNoStartupFindings {
		return nil
	}
	report := operations.StartupFindings(App(), st.cfg, doctorHome(), strictness.Since(strictness.Mark{}))
	if len(report.Checks) == 0 {
		return nil
	}
	var b strings.Builder
	// renderDoctorReport's only error source is the writer, and a
	// strings.Builder never fails.
	_ = renderDoctorReport(&b, report)
	return []composite.Fragment{{Name: startupFindingsFragmentName, Body: b.String()}}
}
