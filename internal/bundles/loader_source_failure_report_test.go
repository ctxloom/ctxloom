package bundles

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// failingReader is a source that cannot be read, deterministically — the shape
// a malformed bundle file produces. It returns the SAME error every time
// because that is the property under test: nothing about a file that will not
// parse changes between two reads in one process.
type failingReader struct{ err error }

func (r failingReader) Read(context.Context) ([]BundleRead, error) { return nil, r.err }

// TestIndex_UnreadableSourceReportsOncePerProcess pins the fix for a reporting
// defect that made three broken bundles read as a catastrophe.
//
// index() is memoized per LOADER, but a process builds MANY: Config.BundleLoader
// composes a fresh one per call site, and `ctxloom doctor` went through 22 of
// them. Every build re-read the sources and re-reported the same failures, so a
// run emitted 66 warning lines carrying 3 distinct faults. The volume read as a
// broken installation and buried the three file names that actually needed
// fixing.
//
// Reporting once is correct HERE specifically because the fault is
// deterministic and local: a bundle file that will not parse cannot start
// parsing between two loader builds. This is not a general licence to dedup —
// a fault that can legitimately change between reads must report every time.
func TestIndex_UnreadableSourceReportsOncePerProcess(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(strictness.Reset)
	// WarnOnce's dedup is process-wide and permanent by design, so without this
	// the assertion below is only meaningful on the first run in a process
	// (`go test -count=2` would see silence and read it as success).
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	// A sentinel unique to this test: the dedup key is the rendered line, so a
	// generic string could collide with another test's warning and make this
	// pass for the wrong reason.
	const detail = "malformed-bundle-sentinel-9f3a"

	mark := strictness.Checkpoint()
	const builds = 3
	for range builds {
		got, err := NewLoader(failingReader{err: errors.New(detail)}).List()
		require.NoError(t, err, "List keeps its signature; loudness rides the strictness choke")
		require.Empty(t, got, "the source genuinely could not be read, so nothing can be listed")
	}

	require.Equal(t, 1, strings.Count(sink.String(), detail),
		"a deterministic source failure must be reported ONCE per process, not once per loader "+
			"built: %d builds produced %d copies of the same line, which is what turned three "+
			"malformed bundles into a wall of identical warnings that hid which files to fix",
		builds, strings.Count(sink.String(), detail))

	require.NotEmpty(t, strictness.Since(mark),
		"deduping the LINE must not cost the FINDING: a source that could not be read still has "+
			"to record a fatal-class fault, or strict mode opens a session on content it never "+
			"managed to read")
}
