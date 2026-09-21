package coord

import (
	"bytes"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// termSink is the terminal renderer the composition root hands production
// coordinators, so a test that captures stderr through clidiag.SetSink reads
// the same text a user would.
func termSink() report.Sink { return strictness.Sink("ctxloom") }

// termRep is termSink as the Reporter a free function takes.
func termRep() report.Reporter { return report.To(termSink()) }

// captureWarnings routes the diagnostic sink into a buffer for the test's
// duration.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}
