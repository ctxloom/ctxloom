package operations

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// link writes one engine-transcript symlink plus the vendor log it points at,
// stamping the TARGET's mtime (ordering is by target, not by link).
func link(t *testing.T, harpDir, vendorDir, engine, sessionID string, age time.Duration) string {
	t.Helper()
	target := filepath.Join(vendorDir, sessionID+".jsonl")
	require.NoError(t, os.WriteFile(target, []byte("{}\n"), 0o600))
	require.NoError(t, os.Chtimes(target, time.Now().Add(-age), time.Now().Add(-age)))
	leaf := paths.EngineTranscriptLinkPrefix + engine + "-" + sessionID + ".jsonl"
	require.NoError(t, os.Symlink(target, filepath.Join(harpDir, leaf)))
	return target
}

func TestHarpTranscripts_NewestFirstAndDerivesIDFromTarget(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := paths.HarpDir("some-harp")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	vendorDir := t.TempDir()

	// A multi-dash engine AND a dash-bearing UUID: splitting the LINK NAME
	// cannot separate them, which is why the id comes from the target.
	link(t, harpDir, vendorDir, "claude-code", "9847366d-9f1a-4a34-b2c2-de0000000000", 2*time.Hour)
	link(t, harpDir, vendorDir, "claude-code", "fc735796-6258-4354-a1b0-cc0000000000", 24*time.Hour)
	link(t, harpDir, vendorDir, "claude-code", "0b1d5db0-37fd-4df4-b733-91f000000000", 1*time.Minute)

	got, err := HarpTranscripts("some-harp")
	require.NoError(t, err)
	require.Len(t, got, 3, "every resolvable link is one lineage entry")
	assert.Equal(t, []string{
		"0b1d5db0-37fd-4df4-b733-91f000000000",
		"9847366d-9f1a-4a34-b2c2-de0000000000",
		"fc735796-6258-4354-a1b0-cc0000000000",
	}, []string{got[0].SessionID, got[1].SessionID, got[2].SessionID},
		"newest TARGET first, with the id taken from the target's base name")
	assert.False(t, got[0].ModTime.IsZero(), "the target's mtime is what ordering rests on")
}

// On a real box the overwhelming majority of these links dangle, so a dangling
// link is the ordinary case and must be skipped rather than fail a recovery.
func TestHarpTranscripts_SkipsDanglingLinksAndNonLinks(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := paths.HarpDir("some-harp")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	vendorDir := t.TempDir()

	live := link(t, harpDir, vendorDir, "claude-code", "live-session", time.Minute)

	dead := filepath.Join(vendorDir, "reaped-session.jsonl")
	require.NoError(t, os.WriteFile(dead, []byte("{}\n"), 0o600))
	require.NoError(t, os.Symlink(dead, filepath.Join(harpDir,
		paths.EngineTranscriptLinkPrefix+"claude-code-reaped-session.jsonl")))
	require.NoError(t, os.Remove(dead)) // now dangling

	// A non-link file at the harp root must not be mistaken for lineage.
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "essence.md"), []byte("x"), 0o600))

	got, err := HarpTranscripts("some-harp")
	require.NoError(t, err)
	require.Len(t, got, 1, "the dangling link and the plain file are both excluded")
	assert.Equal(t, "live-session", got[0].SessionID)
	assert.Equal(t, live, got[0].Path, "Path is the RESOLVED target, not the link")
}

func TestHarpTranscripts_MissingHarpDirIsNotAFault(t *testing.T) {
	testsupport.Isolate(t)
	got, err := HarpTranscripts("never-existed")
	require.NoError(t, err, "a harp that has authored nothing is not an error")
	assert.Empty(t, got)
}
