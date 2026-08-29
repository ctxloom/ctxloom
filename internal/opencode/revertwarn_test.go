package opencode

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Every early-error path in Chat and launchInteractive used to discard the
// LIFO revert-step errors with `_ =`, so a failed restore (e.g. of
// opencode.json) left the user's project holding a ctxloom-written overlay
// (a plan run's read-only permission block, or a foreign model) with no
// warning at all — silently locking the project, the exact outcome
// snapshotOpencodeConfig exists to prevent. warnRevertFailure is what those
// call sites now route through: the ORIGINAL error is still what's returned
// (masking it would be worse), but the revert failure itself is now surfaced.
func TestWarnRevertFailure_EmitsDiagnosticNamingTheArtifact(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	warnRevertFailure("opencode.json", fmt.Errorf("permission denied"))
	assert.Contains(t, buf.String(), "opencode.json", "must name which artifact failed to revert")
	assert.Contains(t, buf.String(), "permission denied", "must carry the underlying error")
}

func TestWarnRevertFailure_SuccessfulRevertStaysQuiet(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	warnRevertFailure("opencode.json", nil)
	assert.Empty(t, buf.String(), "a successful revert must not warn")
}
