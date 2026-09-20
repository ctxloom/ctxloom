package operations

import (
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// terminalReporter is the channel the application services report through
// today — the same terminal renderer their own clidiag.Warn calls reach —
// handed to the core functions they call that now take a Reporter. It goes
// when operations reports through an injected Reporter of its own.
func terminalReporter() report.Reporter { return report.To(strictness.Sink("ctxloom")) }
