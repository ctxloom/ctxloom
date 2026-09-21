package coordgrpc

import (
	"bytes"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// captureWarnings routes the diagnostic sink into a buffer for the test's
// duration.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}
