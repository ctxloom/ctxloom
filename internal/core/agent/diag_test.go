package agent

import (
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// termRep is the terminal renderer the composition hands production
// callers, so a test that captures stderr through clidiag.SetSink reads the
// same text a user would.
func termRep() report.Reporter { return report.To(strictness.Sink("ctxloom")) }
