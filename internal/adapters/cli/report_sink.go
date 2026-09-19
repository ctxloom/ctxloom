package cli

import (
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// DiagnosticSink is the one place a core finding becomes stderr text. The
// core returns or reports report.Findings and never touches the process's
// diagnostic channel; the composition root builds this sink once, with the
// binary's own name, and hands it down. An advisory renders as the family's
// "<prog>: warning:" line (or the structured envelope when --format asked
// for one); a fail-loudly finding renders the same way and is additionally
// ledgered for the startup gate; a Quiet one is ledgered only.
func DiagnosticSink(prog string) report.Sink {
	return report.SinkFunc(func(f report.Finding) {
		if !f.Quiet {
			if f.Once {
				clidiag.WarnOnce(prog, "%s", f.Text)
			} else {
				clidiag.Warn(prog, "%s", f.Text)
			}
		}
		strictness.Ledger(f)
	})
}
