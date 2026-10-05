package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestCompactSource_UnopenableSessionIndex_ReportsTheRealReason: source
// resolution moved out of the compactor to the caller (slice 14a), so the
// "session index unavailable" reason — the one that actually happened — is
// reported HERE, not swapped for a misleading "backend does not support
// session history". The canonical index IS the only source, so its failure to
// open is the whole reason there is nothing to read.
func TestCompactSource_UnopenableSessionIndex_ReportsTheRealReason(t *testing.T) {
	home := testsupport.Isolate(t)

	// Make sessions.Open fail: it MkdirAll's the index's parent, so a plain
	// file where that directory belongs is enough.
	sessionsPath := filepath.Join(home, ".ctxloom", "sessions")
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionsPath), 0o755))
	require.NoError(t, os.WriteFile(sessionsPath, []byte("not a directory"), 0o644))

	_, err := DistillSource(home)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session index",
		"the failure that actually happened must be the one reported")
	assert.NotContains(t, err.Error(), "does not support session history",
		"reporting an unsupported backend sends the user after the wrong remedy")
}

// TestCompactable_CanonicalCaptureBeatsAVendorPath pins harmful-sprout: a
// container-runtime harp records the engine's transcript path but never gets
// a session_id bound host-side. Its canonical capture is on disk, and that is
// what a compact reads.
func TestCompactable_CanonicalCaptureBeatsAVendorPath(t *testing.T) {
	testsupport.Isolate(t)
	err := compactable(&sessions.Entry{
		HarpName:                "vexed-scary-gab",
		TranscriptPath:          "/nonexistent/vendor/transcript.jsonl",
		CanonicalTranscriptPath: "/nonexistent/canonical/transcript.jsonl",
	})
	require.NoError(t, err, "a canonical capture must be read rather than refused over a vendor path nothing can parse")
}
