package cli

import (
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// DiagnosticSink is the one place a core finding becomes stderr text.
func DiagnosticSink(prog string) report.Sink {
	return report.Discard
}
