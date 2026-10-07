package operations

import (
	"context"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// StartupLeadName is the fragment identity a launch's startup findings ride
// under (assembleDedupedContext keys on name AND content).
const StartupLeadName = "ctxloom-startup-findings"

// WithStartupFindings is l led by its startup findings: StartupFindings over
// the launch's own generation (deps.Snapshot), the home deps.Host names,
// recorded, and the withheld tally of l's own package, rendered as `ctxloom
// doctor` renders them (WriteDoctorReport) and carried again as one block
// after the assembled context (launch.WithLead) — the same seam every other
// context source rides, so each engine's own delivery differences are already
// solved, for a launch the CLI drives and for a delegated child's runner
// alike. It is composed after the launch resolved, because the isolation axis
// resolves there and a degraded-to-host finding is a case it exists for.
//
// recorded is what the launch's own resolution recorded and proceeded past:
// the caller's strictness window. Composing reads findings and never fails on
// one; the error is the carrier's. A launch with nothing to say is returned
// as it is.
func WithStartupFindings(ctx context.Context, app *App, deps launch.Deps, l launch.Launch, recorded report.Findings) (launch.Launch, error) {
	pkg, err := launch.Open(ctx, deps, l)
	if err != nil {
		return launch.Launch{}, err
	}
	return launch.WithLead(ctx, deps, l, startupLead(app, deps.Snapshot.Config, deps.Host.Home, recorded, pkg.Attestation().Withheld)...)
}

// startupLead is StartupFindings rendered as the one lead block, or nothing
// when there is nothing to say.
func startupLead(app *App, cfg *config.Config, home string, recorded report.Findings, withheld []bundles.Withhold) []composite.Fragment {
	findings := StartupFindings(app, cfg, home, recorded, withheld)
	if len(findings.Checks) == 0 {
		return nil
	}
	var b strings.Builder
	// WriteDoctorReport's only error source is the writer, and a
	// strings.Builder never fails.
	_ = WriteDoctorReport(&b, findings)
	return []composite.Fragment{{Name: StartupLeadName, Body: b.String()}}
}
