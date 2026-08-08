package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// claudeVendorFixturePath resolves the same real claude transcript fixture
// internal/transcript/vendorreader/claude's own test suite exercises, anchored
// via runtime.Caller so it resolves regardless of the test binary's cwd.
func claudeVendorFixturePath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(file), "..", "transcript", "vendorreader", "claude", "testdata", "transcript-fixture.jsonl")
}

// canonicalTranscriptExists reports whether harp has a non-empty canonical
// transcript.jsonl on disk.
func canonicalTranscriptExists(t *testing.T, harp string) bool {
	t.Helper()
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return false
	}
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			return true
		}
	}
	return false
}

// TestConvertVendorTranscriptOnExit_EmptyHarp and
// TestConvertVendorTranscriptOnExit_UnknownHarp cover the two "there is
// nothing to convert" degrades convertVendorTranscriptOnExit must survive
// silently — no panic, no index mutation — mirroring RecordOneshot's own
// empty-input discipline.
func TestConvertVendorTranscriptOnExit_EmptyHarp(t *testing.T) {
	testsupport.Isolate(t)
	convertVendorTranscriptOnExit("") // must not panic
}

func TestConvertVendorTranscriptOnExit_UnknownHarp(t *testing.T) {
	testsupport.Isolate(t)
	convertVendorTranscriptOnExit("harp-never-indexed") // must not panic
	assert.False(t, canonicalTranscriptExists(t, "harp-never-indexed"))
}

// TestConvertVendorTranscriptOnExit_UnregisteredBackend covers a backend with
// no vendor reader (e.g. opencode, which keeps its own native reader —
// docs/transcript-schema.md §8): a silent no-op, no canonical file.
func TestConvertVendorTranscriptOnExit_UnregisteredBackend(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/tmp/project", "opencode")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(entry.HarpName, "sess-1", claudeVendorFixturePath(t)))

	convertVendorTranscriptOnExit(entry.HarpName)
	assert.False(t, canonicalTranscriptExists(t, entry.HarpName))
}

// TestConvertVendorTranscriptOnExit_ConvertsBoundTranscript is the real,
// end-to-end exit-seam behavior this function exists for: a harp whose
// SessionStart bind hook recorded a real (fixture) claude transcript path
// gets a canonical transcript.jsonl after the interactive session exits.
func TestConvertVendorTranscriptOnExit_ConvertsBoundTranscript(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/tmp/project", "claude-code")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(entry.HarpName, "sess-1", claudeVendorFixturePath(t)))

	convertVendorTranscriptOnExit(entry.HarpName)
	assert.True(t, canonicalTranscriptExists(t, entry.HarpName))

	// Re-running the exit seam a second time (e.g. a resumed `ctxloom run`
	// against the same harp exiting again) must not duplicate the transcript —
	// the same idempotency ConvertVendorTranscript's own tests cover directly,
	// exercised here through the exact call the run.go seam makes.
	p, err := paths.HarpCanonicalTranscriptPath(entry.HarpName)
	require.NoError(t, err)
	before, err := os.ReadFile(p)
	require.NoError(t, err)

	convertVendorTranscriptOnExit(entry.HarpName)
	after, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a second on-exit call must leave the canonical transcript byte-for-byte unchanged")
}
