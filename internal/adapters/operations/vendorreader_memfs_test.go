package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// memVendorRoot is where these tests place vendor files: a path that exists
// only in the in-memory fs, so any read that reaches for the disk fails.
const memVendorRoot = "/operations-memfs-only"

// seedMem writes raw at p in mem, after checking p is absent from disk.
func seedMem(t *testing.T, mem afero.Fs, p string, raw []byte) string {
	t.Helper()
	requireAbsentFromDisk(t, p)
	require.NoError(t, afero.WriteFile(mem, p, raw, 0o644))
	return p
}

func requireAbsentFromDisk(t *testing.T, p string) {
	t.Helper()
	_, err := os.Stat(p)
	require.ErrorIs(t, err, fs.ErrNotExist, "%s must not exist on disk", p)
}

// fixtureLines is the first n lines of the claude fixture (all of it for 0).
func fixtureLines(t *testing.T, n int) []byte {
	t.Helper()
	raw, err := os.ReadFile(vendorFileWithLines(t, n))
	require.NoError(t, err)
	return raw
}

// memCanonical is harp's canonical transcript as the in-memory fs holds it,
// after checking the disk holds none.
func memCanonical(t *testing.T, mem afero.Fs, harp string) []string {
	t.Helper()
	dest, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	requireAbsentFromDisk(t, dest)
	raw, err := afero.ReadFile(mem, dest)
	require.NoError(t, err)
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestConvertVendorTranscript_ReadsAndWritesThroughTheGivenFs(t *testing.T) {
	testsupport.Isolate(t)
	mem := afero.NewMemMapFs()
	harp := "memfs-convert-harp"
	src := seedMem(t, mem, path.Join(memVendorRoot, "live.jsonl"), fixtureLines(t, 0))

	converted, err := ConvertVendorTranscript(context.Background(), mem, engines.Registry(), claudeEntry(harp, src))
	require.NoError(t, err)
	require.True(t, converted)
	assert.NotEmpty(t, memCanonical(t, mem, harp))

	converted, err = ConvertVendorTranscript(context.Background(), mem, engines.Registry(), claudeEntry(harp, src))
	require.NoError(t, err)
	assert.False(t, converted, "the canonical transcript the fs holds is the idempotency guard")
}

func TestConvertVendorTranscript_RotationSegmentLivesInTheGivenFs(t *testing.T) {
	testsupport.Isolate(t)
	mem := afero.NewMemMapFs()
	harp := "memfs-rotation-harp"
	rotSrc := seedMem(t, mem, path.Join(memVendorRoot, "pre-clear.jsonl"), fixtureLines(t, 0))
	liveRaw, err := os.ReadFile(liveFixturePath)
	require.NoError(t, err)
	liveSrc := seedMem(t, mem, path.Join(memVendorRoot, "live.jsonl"), liveRaw)
	e := claudeEntry(harp, liveSrc)
	e.Rotations = []sessions.Rotation{{SessionID: "pre-clear-id", TranscriptPath: rotSrc, RotatedAt: time.Now()}}

	converted, err := ConvertVendorTranscript(context.Background(), mem, engines.Registry(), e)
	require.NoError(t, err)
	require.True(t, converted)
	assert.Contains(t, strings.Join(memCanonical(t, mem, harp), "\n"), rotationMarker)

	seg, err := paths.ResolveHarpSegmentPath(harp, "pre-clear-id")
	require.NoError(t, err)
	requireAbsentFromDisk(t, seg)
	cached, err := afero.Exists(mem, seg)
	require.NoError(t, err)
	assert.True(t, cached, "the converted rotation segment is cached in the given fs")
}

func TestRefreshVendorTranscript_ResumesFromAWatermarkInTheGivenFs(t *testing.T) {
	testsupport.Isolate(t)
	mem := afero.NewMemMapFs()
	harp := "memfs-watermark-harp"
	src := seedMem(t, mem, path.Join(memVendorRoot, "live.jsonl"), fixtureLines(t, settledPrefix))
	e := liveClaudeEntry(harp, src)

	converted, err := ConvertVendorTranscript(context.Background(), mem, engines.Registry(), e)
	require.NoError(t, err)
	require.True(t, converted)
	wmPath := watermarkPath(t, harp)
	requireAbsentFromDisk(t, wmPath)
	rawWM, err := afero.ReadFile(mem, wmPath)
	require.NoError(t, err)
	var wm transcriptWatermark
	require.NoError(t, json.Unmarshal(rawWM, &wm))
	require.Positive(t, wm.Vendor.Start)

	// Grow the session, then destroy the vendor prefix the watermark covers:
	// only a resume that read the watermark and the canonical prefix from
	// the given fs reproduces the full conversion.
	grown := fixtureLines(t, 0)
	junked := append(bytes.Repeat([]byte("#"), int(wm.Vendor.Start)), grown[wm.Vendor.Start:]...)
	require.NoError(t, afero.WriteFile(mem, src, junked, 0o644))
	converted, err = RefreshVendorTranscript(context.Background(), mem, engines.Registry(), e)
	require.NoError(t, err)
	require.True(t, converted)
	resumed := withoutRecordTimes(memCanonical(t, mem, harp))

	fresh := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fresh, src, grown, 0o644))
	converted, err = ConvertVendorTranscript(context.Background(), fresh, engines.Registry(), e)
	require.NoError(t, err)
	require.True(t, converted)
	assert.Equal(t, withoutRecordTimes(memCanonical(t, fresh, harp)), resumed)
}

func TestResolveTurnTranscript_StatsTheHookPathInTheGivenFs(t *testing.T) {
	testsupport.Isolate(t)
	harp := mintTurnSession(t, "claude-code", "2.1.225")
	mem := afero.NewMemMapFs()
	src := seedMem(t, mem, path.Join(memVendorRoot, "turn.jsonl"), []byte("{}\n"))

	_, got, err := ResolveTurnTranscript(context.Background(), mem, engines.Registry(), harp, src)
	require.NoError(t, err)
	assert.Equal(t, src, got, "a hook path the given fs holds is the turn's transcript")
}

func TestScanAdoptCandidates_ScansTheVendorDirInTheGivenFs(t *testing.T) {
	mgr := newAdoptManager(t)
	mem := afero.NewMemMapFs()
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	line := func(id string, ts time.Time) string {
		b, err := json.Marshal(map[string]string{"type": "user", "sessionId": id, "timestamp": ts.UTC().Format(time.RFC3339Nano)})
		require.NoError(t, err)
		return string(b) + "\n"
	}
	at := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	live := seedMem(t, mem, path.Join(memVendorRoot, "id-live.jsonl"), []byte(line("id-live", at)+line("id-live", at.Add(time.Hour))))
	seedMem(t, mem, path.Join(memVendorRoot, "id-orphan.jsonl"), []byte(line("id-orphan", at.Add(-48*time.Hour))+line("id-orphan", at.Add(-47*time.Hour))))
	require.NoError(t, mgr.BindSession(entry.HarpName, "id-live", live))

	scan, err := ScanAdoptCandidates(mem, entry.HarpName)
	require.NoError(t, err)
	var orphan *AdoptCandidate
	for i := range scan.Candidates {
		if scan.Candidates[i].SessionID == "id-orphan" {
			orphan = &scan.Candidates[i]
		}
	}
	require.NotNil(t, orphan, "the orphan the given fs holds is found")
	assert.True(t, orphan.HasSpan, "its record span is read through the given fs")
}
