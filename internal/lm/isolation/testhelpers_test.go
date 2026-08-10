package isolation

import (
	"bytes"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// captureWarnings redirects clidiag's stderr-flavored helpers (Warn/WarnOnce)
// to a buffer for the duration of the test and returns it, so a test can
// assert on a LOUD non-fatal finding's text without touching strictness.
//
// Formerly lived in curatedhome_test.go (deleted with the antigravity-only
// curated-HOME mechanism); several unrelated test files across this package
// still use it, so it moved here rather than being duplicated per-file.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}
