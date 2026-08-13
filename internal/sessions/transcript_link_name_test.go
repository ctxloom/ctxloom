package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLinkEngineTranscript_NameTracksThePathsHelper pins WHY the
// per-vendor-log link is named what it is: linkEngineTranscript derives the
// link's path through paths.HarpEngineTranscriptLinkPath rather than
// hand-rolling the engine-transcript-<engine>-<sessionID>.jsonl pattern
// itself. A link built from an independent, hand-copied format string can
// drift from the one paths.go actually documents and every OTHER reader of
// that naming scheme (paths_test.go, a future lineage-listing reader) relies
// on; deriving through the shared helper means there is exactly one place
// that pattern is spelled out.
//
// Note this assertion cannot be driven red by a naming-scheme change alone
// (both sides would move together) -- what it pins is that linkEngineTranscript
// calls THROUGH paths.HarpEngineTranscriptLinkPath rather than re-deriving the
// same string independently, verified here by cross-checking the symlink
// actually created against what the helper, called directly, says the path
// should be.
func TestLinkEngineTranscript_NameTracksThePathsHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	testsupport.Isolate(t)
	const harp = "swift-amber-falcon"
	const engine = "claude-code"
	const sessionID = "sess-1"

	// The target must live OUTSIDE the harp dir, or the linker deliberately
	// declines to add a second name for an already-addressable file — and the
	// test would assert nothing while looking like it passed.
	target := filepath.Join(t.TempDir(), "engine-native.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("{}\n"), 0o644))

	linkEngineTranscript(harp, engine, sessionID, target)

	wantLink, err := paths.HarpEngineTranscriptLinkPath(harp, engine, sessionID)
	require.NoError(t, err)

	got, err := os.Readlink(wantLink)
	require.NoError(t, err,
		"no symlink at %s (paths.HarpEngineTranscriptLinkPath's own answer): the link was not created where the shared naming helper says it should live", wantLink)
	assert.Equal(t, target, got, "the link must point at the engine's live transcript")

	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	assert.Equal(t, harpDir, filepath.Dir(wantLink), "the link sits directly at the harp dir root")
	assert.Contains(t, filepath.Base(wantLink), engine, "the link name carries the engine")
	assert.Contains(t, filepath.Base(wantLink), sessionID, "the link name carries the session id")
}
