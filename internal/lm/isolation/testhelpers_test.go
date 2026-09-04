package isolation

import (
	"bytes"
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// testStamp is a whole, CLEAN version stamp — the shape version.ValidStamp
// accepts. It is fixed rather than read from the tree so a test's expectations
// cannot move with the build that runs them.
const testStamp = "v0.7.0-abc1234-20260904T031516"

// testStampDirty is the SAME commit built from a tracked-dirty tree.
const testStampDirty = testStamp + "-dirty"

// TestMain gives this package's tests the version stamp that production
// guarantees them. binaryVersion keys both the agent image tag and the
// ctxloom.provenance label, and internal/cli's root gate refuses to RUN an
// unstamped binary — so an unset stamp is a state production cannot reach.
// Leaving it unset here would silently empty the provenance and disable the
// staleness gate underneath every test that exercises it, which is a false
// green rather than a missing one.
func TestMain(m *testing.M) {
	SetBinaryVersion(testStamp)
	os.Exit(m.Run())
}

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
