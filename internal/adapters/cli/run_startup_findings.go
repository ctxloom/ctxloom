package cli

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
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
	report := operations.StartupFindings(App(), st.cfg, doctorHome(), strictness.Since(strictness.Mark{}))
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
