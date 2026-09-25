package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Every failed item in the pull summary carries its own fix line, directly
// under it — the summary is where the user reads which item broke.
func TestRenderPullSummary_EveryFailedItemCarriesItsFix(t *testing.T) {
	failed := []operations.SyncItem{
		{Reference: "acme/a", Status: "failed", Error: "boom"},
		{Reference: "acme/b", Status: "failed", Error: "bang"},
	}
	var out bytes.Buffer
	renderPullSummary(&out, &operations.SyncDependenciesResult{Total: 2, Errors: 2, Failed: failed})
	for _, item := range failed {
		require.NotEmpty(t, item.Remedy(), "a failed item always has a fix")
		assert.Contains(t, out.String(), "    - "+item.Reference+": "+item.Error+clifmt.FixLine("      ", item.Remedy())+"\n")
	}
}

// The pull's error names the fix only when every failed item shares it.
func TestPullResultErr_RemedyOnlyWhenShared(t *testing.T) {
	shared := []operations.SyncItem{
		{Reference: "a", Status: "failed", Error: "x"},
		{Reference: "b", Status: "failed", Error: "y"},
	}
	err := pullResultErr(&operations.SyncDependenciesResult{Errors: 2, Failed: shared})
	fix, ok := clifmt.RemedyOf(err)
	assert.True(t, ok)
	assert.Equal(t, shared[0].Remedy(), fix)

	mixed := []operations.SyncItem{shared[0], {Reference: "c", Error: "z"}}
	require.NotEqual(t, mixed[0].Remedy(), mixed[1].Remedy())
	_, ok = clifmt.RemedyOf(pullResultErr(&operations.SyncDependenciesResult{Errors: 2, Failed: mixed}))
	assert.False(t, ok, "one remedy field cannot stand for several")
}

// init's pull warning names the fix its error carries (6d), and only falls
// back to re-running the pull when the error names none.
func TestWarnDependencyPullFailed_NamesTheErrorsFix(t *testing.T) {
	// Text mode: a command run earlier in this package may have left the
	// process-wide structured channel on (root's PersistentPreRun sets it).
	clidiag.SetStructured(false)
	var buf strings.Builder
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)

	const fix = "fix the reference's spelling"
	warnDependencyPullFailed(report.Error{Msg: "deps pull: 1 failed (x)", Fix: fix})
	assert.Contains(t, buf.String(), clifmt.FixLine("  ", fix))
	assert.NotContains(t, buf.String(), remedyRetryPull)

	buf.Reset()
	warnDependencyPullFailed(errors.New("offline"))
	assert.Contains(t, buf.String(), clifmt.FixLine("  ", remedyRetryPull))
	assert.Contains(t, buf.String(), "Everything init wrote is kept", "the preamble stands")
}
