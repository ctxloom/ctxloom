package operations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// newAdoptManager isolates HOME (so sessions.Open("") — both here and inside
// openSessions() — resolve the SAME sandboxed index.yaml) and opens it.
func newAdoptManager(t *testing.T) *sessions.Manager {
	t.Helper()
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	return mgr
}

// writeClaudeVendorFile writes a minimal claude-code-shaped vendor transcript
// at dir/<sessionID>.jsonl carrying two lines timestamped start and end — the
// file's internal record span claudeRecordSpan reads. mtime, when non-nil,
// overrides the file's on-disk modification time AFTER writing: the ordering
// tests below rig it to DISAGREE with the internal span, since the whole
// point of the measured ordering rule is that mtime must never be consulted.
func writeClaudeVendorFile(t *testing.T, dir, sessionID string, start, end time.Time, mtime *time.Time) string {
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
	if mtime != nil {
		require.NoError(t, os.Chtimes(path, *mtime, *mtime))
	}
	return path
}

// TestScanAdoptCandidates_OrdersByInternalTimestampNeverMtime is the
// measured-ordering mutation kill: two orphans both predate the live
// binding, with mtimes RIGGED to disagree with their internal record order
// (the newer-internally-earlier file gets the NEWER mtime). A version that
// ordered by mtime instead of internal timestamp would adopt/report them
// swapped, and would compute the wrong RotatedAt successor for each.
func TestScanAdoptCandidates_OrdersByInternalTimestampNeverMtime(t *testing.T) {
	mgr := newAdoptManager(t)
	dir := t.TempDir()

	entry, err := mgr.AssignHarp("/proj", config.BackendClaudeCode)
	require.NoError(t, err)
	harp := entry.HarpName

	liveStart := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	liveEnd := time.Date(2026, 1, 10, 1, 0, 0, 0, time.UTC)
	livePath := writeClaudeVendorFile(t, dir, "id-live", liveStart, liveEnd, nil)
	require.NoError(t, mgr.BindSession(harp, "id-live", livePath))

	earlyStart := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	earlyEnd := time.Date(2026, 1, 5, 1, 0, 0, 0, time.UTC)
	newMtime := time.Now()
	writeClaudeVendorFile(t, dir, "id-early", earlyStart, earlyEnd, &newMtime) // internally EARLIEST, mtime NEWEST

	midStart := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	midEnd := time.Date(2026, 1, 7, 1, 0, 0, 0, time.UTC)
	oldMtime := time.Unix(0, 0)
	writeClaudeVendorFile(t, dir, "id-mid", midStart, midEnd, &oldMtime) // internally MIDDLE, mtime OLDEST

	scan, err := ScanAdoptCandidates(harp)
	require.NoError(t, err)
	// id-early, id-mid (both adopted) plus id-live itself — the live
	// binding's OWN vendor file sits in the same scanned directory, and is
	// correctly re-discovered and skipped as already-in-lineage via the
	// store lookup rather than silently excluded by identity.
	require.Len(t, scan.Candidates, 3)

	assert.Equal(t, "id-early", scan.Candidates[0].SessionID, "internal-timestamp order, not mtime order")
	assert.Equal(t, AdoptVerdictAdopt, scan.Candidates[0].Verdict)
	assert.Equal(t, "id-mid", scan.Candidates[1].SessionID)
	assert.Equal(t, AdoptVerdictAdopt, scan.Candidates[1].Verdict)
	assert.Equal(t, "id-live", scan.Candidates[2].SessionID)
	assert.Equal(t, AdoptVerdictSkip, scan.Candidates[2].Verdict)
	assert.Contains(t, scan.Candidates[2].Reason, "already in")

	assert.True(t, scan.Candidates[0].RotatedAt.Equal(midStart), "id-early's successor is id-mid, so RotatedAt is id-mid's FIRST record")
	assert.Equal(t, AdoptRotatedAtSuccessorFirstRecord, scan.Candidates[0].RotatedAtSource)
	assert.True(t, scan.Candidates[1].RotatedAt.Equal(liveStart), "id-mid's successor is the live binding")
	assert.Equal(t, AdoptRotatedAtSuccessorFirstRecord, scan.Candidates[1].RotatedAtSource)

	applied, err := ApplyAdopt(harp, scan.Candidates)
	require.NoError(t, err)
	assert.Equal(t, 2, applied)

	found, err := mgr.Find(harp)
	require.NoError(t, err)
	require.Len(t, found.Rotations, 2)
	assert.Equal(t, "id-early", found.Rotations[0].SessionID, "chronologically earliest rotation must be array-first (harp-lifetime rebuild order)")
	assert.Equal(t, "id-mid", found.Rotations[1].SessionID)
}

// TestScanAdoptCandidates_SkipsOverlappingSpan pins that a candidate whose
// span overlaps an existing lineage segment (a concurrent, unrelated
// session) is reported skipped and is NEVER adopted, even on --apply.
func TestScanAdoptCandidates_SkipsOverlappingSpan(t *testing.T) {
	mgr := newAdoptManager(t)
	dir := t.TempDir()
	entry, err := mgr.AssignHarp("/proj", config.BackendClaudeCode)
	require.NoError(t, err)
	harp := entry.HarpName

	liveStart := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	liveEnd := time.Date(2026, 2, 1, 1, 0, 0, 0, time.UTC)
	livePath := writeClaudeVendorFile(t, dir, "id-live", liveStart, liveEnd, nil)
	require.NoError(t, mgr.BindSession(harp, "id-live", livePath))

	writeClaudeVendorFile(t, dir, "id-concurrent",
		time.Date(2026, 2, 1, 0, 30, 0, 0, time.UTC),
		time.Date(2026, 2, 1, 2, 0, 0, 0, time.UTC), nil)

	scan, err := ScanAdoptCandidates(harp)
	require.NoError(t, err)
	// id-concurrent (overlap skip) plus id-live itself, re-discovered and
	// skipped as already-in-lineage (see the ordering test's comment).
	require.Len(t, scan.Candidates, 2)
	assert.Equal(t, "id-concurrent", scan.Candidates[0].SessionID)
	assert.Equal(t, AdoptVerdictSkip, scan.Candidates[0].Verdict)
	assert.Contains(t, scan.Candidates[0].Reason, "overlaps")
	assert.Equal(t, "id-live", scan.Candidates[1].SessionID)
	assert.Equal(t, AdoptVerdictSkip, scan.Candidates[1].Verdict)

	applied, err := ApplyAdopt(harp, scan.Candidates)
	require.NoError(t, err)
	assert.Equal(t, 0, applied, "an overlapping candidate must never be adopted")

	found, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.Empty(t, found.Rotations)
}

// TestScanAdoptCandidates_SkipsAlreadyKnownAndAnotherHarp pins the store-
// lookup dedup: a vendor file already recorded in THIS harp's lineage is
// skipped as already known, and one bound to a DIFFERENT harp (even though
// it happens to sit in the same scanned directory) is skipped by name —
// never silently folded into this harp's lineage.
func TestScanAdoptCandidates_SkipsAlreadyKnownAndAnotherHarp(t *testing.T) {
	mgr := newAdoptManager(t)
	dir := t.TempDir()

	entry, err := mgr.AssignHarp("/proj", config.BackendClaudeCode)
	require.NoError(t, err)
	harp := entry.HarpName
	livePath := writeClaudeVendorFile(t, dir, "id-live",
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC), nil)
	require.NoError(t, mgr.BindSession(harp, "id-live", livePath))

	rotatedPath := writeClaudeVendorFile(t, dir, "id-rotated",
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 1, 0, 0, 0, time.UTC), nil)
	require.NoError(t, mgr.AppendRotations(harp, []sessions.Rotation{
		{SessionID: "id-rotated", TranscriptPath: rotatedPath, RotatedAt: time.Date(2026, 2, 1, 1, 0, 0, 0, time.UTC)},
	}))

	other, err := mgr.AssignHarp("/proj", config.BackendClaudeCode)
	require.NoError(t, err)
	otherPath := writeClaudeVendorFile(t, dir, "id-other-harp",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC), nil)
	require.NoError(t, mgr.BindSession(other.HarpName, "id-other-harp", otherPath))

	scan, err := ScanAdoptCandidates(harp)
	require.NoError(t, err)
	// id-live (this harp's own current binding), id-rotated (already in
	// Rotations) and id-other-harp (bound elsewhere) — all three resolve via
	// the store lookup and are skipped, none silently excluded by identity.
	require.Len(t, scan.Candidates, 3)
	byID := map[string]AdoptCandidate{}
	for _, c := range scan.Candidates {
		byID[c.SessionID] = c
	}

	require.Contains(t, byID, "id-live")
	assert.Equal(t, AdoptVerdictSkip, byID["id-live"].Verdict)
	assert.Contains(t, byID["id-live"].Reason, "already in")

	require.Contains(t, byID, "id-rotated")
	assert.Equal(t, AdoptVerdictSkip, byID["id-rotated"].Verdict)
	assert.Contains(t, byID["id-rotated"].Reason, "already in")

	require.Contains(t, byID, "id-other-harp")
	assert.Equal(t, AdoptVerdictSkip, byID["id-other-harp"].Verdict)
	assert.Contains(t, byID["id-other-harp"].Reason, other.HarpName)
}

// TestScanAdoptCandidates_UnsupportedBackendErrors pins the fail-loud backend
// scope: a non-claude-code harp errors clearly, naming the backend, rather
// than silently scanning nothing.
func TestScanAdoptCandidates_UnsupportedBackendErrors(t *testing.T) {
	mgr := newAdoptManager(t)
	entry, err := mgr.AssignHarp("/proj", "codex")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(entry.HarpName, "id-1", "/tmp/does-not-matter.jsonl"))

	_, err = ScanAdoptCandidates(entry.HarpName)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex")
	assert.Contains(t, err.Error(), "not supported yet")
}

// TestScanAdoptCandidates_UnknownHarpErrors pins the harp-not-found error.
func TestScanAdoptCandidates_UnknownHarpErrors(t *testing.T) {
	testsupport.Isolate(t)
	_, err := ScanAdoptCandidates("no-such-harp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-harp")
}

// TestScanAdoptCandidates_NoTranscriptPathErrors pins the other fail-loud
// guard: a pending harp with no transcript_path bound has no directory to
// scan, and this refuses rather than guessing one (see claude.go's own
// "not-yet-rebuilt" cwd->slug warning for why guessing is the wrong move).
func TestScanAdoptCandidates_NoTranscriptPathErrors(t *testing.T) {
	mgr := newAdoptManager(t)
	entry, err := mgr.AssignHarp("/proj", config.BackendClaudeCode)
	require.NoError(t, err)
	_, err = ScanAdoptCandidates(entry.HarpName)
	require.Error(t, err)
	assert.Contains(t, err.Error(), entry.HarpName)
}

func TestClaudeRecordSpan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	content := `{"type":"summary"}
{"type":"user","timestamp":"2026-01-01T00:00:00Z"}
not json at all
{"type":"assistant","timestamp":"2026-01-01T02:00:00.500Z"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	start, end, n, err := claudeRecordSpan(path)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.True(t, start.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	assert.True(t, end.Equal(time.Date(2026, 1, 1, 2, 0, 0, 500000000, time.UTC)))
}

func TestClaudeRecordSpan_NoTimestampsIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"summary"}`+"\n"), 0o644))
	_, _, n, err := claudeRecordSpan(path)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestClaudeRecordSpan_MissingFileErrors(t *testing.T) {
	_, _, _, err := claudeRecordSpan(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	require.Error(t, err)
}
