package coord

import (
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// termSink is the terminal renderer the composition root hands production
// coordinators, so a test that captures stderr through clidiag.SetSink reads
// the same text a user would.
func termSink() report.Sink { return strictness.Sink("ctxloom") }

// termRep is termSink as the Reporter a free function takes.
func termRep() report.Reporter { return report.To(termSink()) }
