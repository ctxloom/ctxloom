package codex

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// TestConvert_CancelledContextIsObservedBeforeTheFileIsRead pins a fix at
// the outermost seam. Convert opened the file and read every byte of it into
// memory before anything looked at ctx: the first check lived inside
// vendorreader.ConvertJSONLLines' per-line loop, which cannot run until the whole
// read AND the whole scanSessionInfo pass are done. On a multi-megabyte
// rollout that is the entire cost of the import, paid after the caller has
// already given up.
//
// A nonexistent src makes the ordering observable with no timing games: if
// cancellation is checked first, ctx's error is what comes back; if the read
// happens first, the open failure is. Both are errors, so only the identity
// distinguishes them — which is the point.
func TestConvert_CancelledContextIsObservedBeforeTheFileIsRead(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Adapter{}.Convert(ctx, rec, filepath.Join("testdata", "does-not-exist.jsonl"))
	require.ErrorIs(t, err, context.Canceled,
		"a cancelled import must not open or read the vendor file at all")
}

// TestConvertLines_CancelledContextIsObservedBeforeTheScanPass pins the inner
// half. With no lines to dispatch, ConvertJSONLLines' per-line ctx check never
// executes, so a cancelled context produced a nil error — a cancelled import
// reported as a successful empty one, which is this project's signature shape.
// It also proves the check sits ahead of scanSessionInfo rather than inside
// the streaming loop: nothing else in this call path could catch it.
func TestConvertLines_CancelledContextIsObservedBeforeTheScanPass(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, convertLines(ctx, rec, nil), context.Canceled,
		"a cancelled import with nothing to dispatch must still report the cancellation")
}
