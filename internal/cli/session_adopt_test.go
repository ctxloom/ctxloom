package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// resetSessionAdoptFlags restores sessionAdoptApply, for the reason every
// other flag-reset helper in this package documents: pflag never un-sets a
// flag a prior test's invocation set, so a leftover --apply would turn a
// later report-only test into an apply.
func resetSessionAdoptFlags(t *testing.T) {
	t.Helper()
	sessionAdoptApply = false
	resetRootFormat(t)
}

// writeAdoptVendorFile writes a minimal claude-code-shaped vendor transcript
// at dir/<sessionID>.jsonl carrying two lines timestamped start and end —
// the file's internal record span `session adopt` reads. This end-to-end
// suite drives the real cobra command, so the fixture lives here rather than
// being shared with operations' own (identically-shaped, deliberately
// separate) copy: this file proves the CLI's flag/report/apply wiring, not
// the scan/verdict algorithm operations/session_adopt_test.go already covers.
func writeAdoptVendorFile(t *testing.T, dir, sessionID string, start, end time.Time) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, sessionID+".jsonl")
	line := func(ts time.Time) string {
		b, err := json.Marshal(map[string]string{
			"type": "user", "sessionId": sessionID, "timestamp": ts.UTC().Format(time.RFC3339Nano),
		})
		require.NoError(t, err)
		return string(b)
	}
	content := line(start) + "\n" + line(end) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// seedClaudeHarpWithVendorDir mints a claude-code harp bound to a live
// vendor transcript in a fresh directory, and returns the Manager, the harp
// name and that directory (the one `session adopt` will scan).
func seedClaudeHarpWithVendorDir(t *testing.T, projectDir string) (mgr *sessions.Manager, harp, dir string) {
	t.Helper()
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	dir = t.TempDir()
	livePath := writeAdoptVendorFile(t, dir, "id-live",
		time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 10, 1, 0, 0, 0, time.UTC))
	require.NoError(t, mgr.BindSession(entry.HarpName, "id-live", livePath))
	return mgr, entry.HarpName, dir
}

// indexBytes reads the raw session index file — used to prove a dry run
// leaves it byte-for-byte untouched, the payload-level check a mutation that
// wired --apply's write through on a report-only run would fail (a mutation
// that merely dropped a LOG LINE would not).
func indexBytes(t *testing.T) []byte {
	t.Helper()
	p, err := paths.SessionIndexPath()
	require.NoError(t, err)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return b
}

// TestSessionAdopt_DryRunWritesNothing is the dry-run-writes-nothing
// mutation kill: a report-only run must leave index.yaml BYTE-IDENTICAL,
// not merely "the harp still has the same Rotations count" (which a
// mutation reordering fields, or writing and reverting, could still pass).
func TestSessionAdopt_DryRunWritesNothing(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp, vendorDir := seedClaudeHarpWithVendorDir(t, dir)
	writeAdoptVendorFile(t, vendorDir, "id-orphan",
		time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 5, 1, 0, 0, 0, time.UTC))
	t.Cleanup(func() { resetSessionAdoptFlags(t) })

	before := indexBytes(t)

	stdout, stderr, err := execRootCmdBoth(t, "session", "adopt", harp)
	require.NoError(t, err)

	after := indexBytes(t)
	assert.Equal(t, before, after, "a report-only run must leave index.yaml byte-identical")

	assert.Contains(t, stdout, "id-orphan")
	assert.Contains(t, stdout, "would adopt")
	assert.Contains(t, stderr, "adopted nothing — this was a report")
	assert.Contains(t, stderr, "ctxloom session adopt "+harp+" --apply")
}

// TestSessionAdopt_ApplyAppendsThroughStore_SurvivesReload is the --apply
// mutation kill: the new Rotation must be a REAL persisted write a fresh
// Manager (opened over the same path, no shared in-memory state) can see —
// not just a mutation this process happens to still hold.
func TestSessionAdopt_ApplyAppendsThroughStore_SurvivesReload(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp, vendorDir := seedClaudeHarpWithVendorDir(t, dir)
	orphanPath := writeAdoptVendorFile(t, vendorDir, "id-orphan",
		time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 5, 1, 0, 0, 0, time.UTC))
	t.Cleanup(func() { resetSessionAdoptFlags(t) })

	stdout, stderr, err := execRootCmdBoth(t, "session", "adopt", harp, "--apply")
	require.NoError(t, err)
	assert.Contains(t, stdout, "adopted")
	assert.Contains(t, stderr, "adopted 1 rotation")
	assert.Contains(t, stderr, "session distill "+harp)

	fresh, err := sessions.Open("")
	require.NoError(t, err)
	found, err := fresh.Find(harp)
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Len(t, found.Rotations, 1)
	assert.Equal(t, "id-orphan", found.Rotations[0].SessionID)
	assert.Equal(t, orphanPath, found.Rotations[0].TranscriptPath)
	assert.False(t, found.Rotations[0].RotatedAt.IsZero())
	assert.Equal(t, "id-live", found.SessionID, "the live binding is untouched")
}

// TestSessionAdopt_UnsupportedBackendFails pins the backend-scope refusal:
// only claude-code is supported today, and every other backend fails
// loudly, naming itself, rather than silently scanning nothing.
func TestSessionAdopt_UnsupportedBackendFails(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(dir, "codex")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(entry.HarpName, "id-1", filepath.Join(t.TempDir(), "id-1.jsonl")))
	t.Cleanup(func() { resetSessionAdoptFlags(t) })

	_, _, err = execRootCmdBoth(t, "session", "adopt", entry.HarpName)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex")
	assert.Contains(t, err.Error(), "not supported yet")
}

// TestSessionAdopt_UnknownHarpFails pins the plain not-found error.
func TestSessionAdopt_UnknownHarpFails(t *testing.T) {
	testsupport.ProjectDir(t)
	t.Cleanup(func() { resetSessionAdoptFlags(t) })

	_, _, err := execRootCmdBoth(t, "session", "adopt", "no-such-harp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-harp")
}

// TestSessionAdopt_JSONShapesCandidatesWithVerdictAndReason pins the
// structured payload's shape end to end: a machine caller needs the verdict
// and reason per candidate, not just a human-readable table.
func TestSessionAdopt_JSONShapesCandidatesWithVerdictAndReason(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp, vendorDir := seedClaudeHarpWithVendorDir(t, dir)
	// Overlaps the live binding's span (2026-04-10T00:00 to 01:00).
	writeAdoptVendorFile(t, vendorDir, "id-concurrent",
		time.Date(2026, 4, 10, 0, 30, 0, 0, time.UTC), time.Date(2026, 4, 10, 2, 0, 0, 0, time.UTC))
	t.Cleanup(func() { resetSessionAdoptFlags(t) })

	out, err := execRootCmd(t, "session", "adopt", harp, "--format", "json")
	require.NoError(t, err)

	var got struct {
		Harp       string `json:"harp"`
		Backend    string `json:"backend"`
		ScanDir    string `json:"scan_dir"`
		Applied    bool   `json:"applied"`
		Adopted    int    `json:"adopted"`
		Candidates []struct {
			SessionID string `json:"session_id"`
			Verdict   string `json:"verdict"`
			Reason    string `json:"reason"`
		} `json:"candidates"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output must be clean JSON: %s", out)
	assert.Equal(t, harp, got.Harp)
	assert.Equal(t, "claude-code", got.Backend)
	assert.False(t, got.Applied)
	assert.Equal(t, 0, got.Adopted)

	var sawConcurrent, sawLive bool
	for _, c := range got.Candidates {
		switch c.SessionID {
		case "id-concurrent":
			sawConcurrent = true
			assert.Equal(t, "skip", c.Verdict)
			assert.Contains(t, c.Reason, "overlaps")
		case "id-live":
			sawLive = true
			assert.Equal(t, "skip", c.Verdict)
			assert.Contains(t, c.Reason, "already in")
		}
	}
	assert.True(t, sawConcurrent, "the overlapping candidate must be reported")
	assert.True(t, sawLive, "the live binding's own vendor file must be reported as already-known")
}
