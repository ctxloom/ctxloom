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
	seedEngineVersion(t, mgr, entry.HarpName, "claude-code")
	require.NoError(t, mgr.BindSession(entry.HarpName, "sess-1", claudeVendorFixturePath(t)))

	convertVendorTranscriptOnExit(entry.HarpName)
	assert.True(t, canonicalTranscriptExists(t, entry.HarpName))

	// Re-running the exit seam a second time (e.g. a resumed `ctxloom run`
	// against the same harp exiting again) must not LOSE what the first call
	// captured. This is deliberately no longer a byte-identical assertion
	// (path H): the pre-fix behavior was a
	// presence-guarded no-op on the second call, which is exactly the bug
	// this fix removes — a second call now genuinely
	// re-converts, on purpose, so it may legitimately produce MORE than the
	// first (see TestConvertVendorTranscriptOnExit_CapturesTheTailAfterAMidSessionRecover
	// for the case that matters — a source that grew between calls). What
	// must never happen is losing content the first call already captured.
	p, err := paths.HarpCanonicalTranscriptPath(entry.HarpName)
	require.NoError(t, err)
	before, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NotEmpty(t, before)

	convertVendorTranscriptOnExit(entry.HarpName)
	after, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(after), len(before),
		"a second on-exit call against an unchanged source must not shrink the canonical transcript")
}

// TestConvertVendorTranscriptOnExit_CapturesTheTailAfterAMidSessionRecover is
// the filed defect's exact scenario, pinned: a canonical
// transcript already exists — because the user ran /recover mid-session,
// which materializes one — and the session then kept going. The pre-fix
// behavior called operations.ConvertVendorTranscript directly, whose
// presence guard makes it a PERMANENT no-op once ANY canonical transcript
// exists, so every turn after that /recover was silently lost at exit: exit
// 0, no error, canonical transcript frozen at the /recover moment forever.
//
// This asserts on the canonical transcript's own bytes, exactly like the
// D/E mid-session-refresh tests (mcp_tools_memory_recover_test.go) — the
// failure mode is silent, so the tool's own success is not evidence.
func TestConvertVendorTranscriptOnExit_CapturesTheTailAfterAMidSessionRecover(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/tmp/project", "claude-code")
	require.NoError(t, err)
	seedEngineVersion(t, mgr, entry.HarpName, "claude-code")

	full, err := os.ReadFile(claudeVendorFixturePath(t))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(full), "\n"), "\n")
	require.Greater(t, len(lines), 4)
	vendorPath := filepath.Join(t.TempDir(), "live-transcript.jsonl")
	// Half the transcript: simulates the moment a mid-session /recover ran
	// and materialized a canonical transcript from what existed so far.
	require.NoError(t, os.WriteFile(vendorPath, []byte(strings.Join(lines[:len(lines)/2], "\n")+"\n"), 0o644))
	require.NoError(t, mgr.BindSession(entry.HarpName, "sess-1", vendorPath))

	// The mid-session /recover's own capture (today's recover_session path,
	// unaffected by this fix — exercised directly here rather than through
	// the MCP server for a narrower, faster test).
	convertVendorTranscriptOnExit(entry.HarpName)
	require.True(t, canonicalTranscriptExists(t, entry.HarpName))
	p, err := paths.HarpCanonicalTranscriptPath(entry.HarpName)
	require.NoError(t, err)
	afterRecover, err := os.ReadFile(p)
	require.NoError(t, err)

	// The session keeps going after the mid-session /recover.
	require.NoError(t, os.WriteFile(vendorPath, full, 0o644))

	// The interactive session now exits for real.
	convertVendorTranscriptOnExit(entry.HarpName)
	afterExit, err := os.ReadFile(p)
	require.NoError(t, err)

	assert.Greater(t, len(afterExit), len(afterRecover),
		"exit-time capture must pick up everything the session did after the mid-session /recover, not freeze at the recover moment")
	assert.Contains(t, string(afterExit), "[Request interrupted by user for tool use]",
		"the turns added after the mid-session recover must be in the transcript exit-time capture writes")
}
