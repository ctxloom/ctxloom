package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const liveSessionID = "live-session"

// liveClaudeEntry is claudeEntry for a binding that has a session id — the
// key a watermark is stored under, so the only kind of entry that gets one.
func liveClaudeEntry(harp, vendorPath string) sessions.Entry {
	e := claudeEntry(harp, vendorPath)
	e.SessionID = liveSessionID
	return e
}

func watermarkPath(t *testing.T, harp string) string {
	t.Helper()
	p, err := paths.ResolveHarpSegmentWatermarkPath(harp, liveSessionID)
	require.NoError(t, err)
	return p
}

func readWatermark(t *testing.T, harp string) transcriptWatermark {
	t.Helper()
	raw, err := os.ReadFile(watermarkPath(t, harp))
	require.NoError(t, err)
	var wm transcriptWatermark
	require.NoError(t, json.Unmarshal(raw, &wm))
	return wm
}

func writeWatermark(t *testing.T, harp string, wm transcriptWatermark) {
	t.Helper()
	raw, err := json.Marshal(wm)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(watermarkPath(t, harp), raw, 0o644))
}

func refresh(t *testing.T, e sessions.Entry) {
	t.Helper()
	converted, err := RefreshVendorTranscript(context.Background(), engines.Registry(), e)
	require.NoError(t, err)
	require.True(t, converted)
}

// fullConversionOf is what a from-scratch conversion of vendorPath writes for
// harp: the reference every resumed transcript must match, timestamps aside.
func fullConversionOf(t *testing.T, harp, vendorPath string) []string {
	t.Helper()
	_ = os.Remove(watermarkPath(t, harp))
	refresh(t, liveClaudeEntry(harp, vendorPath))
	return withoutRecordTimes(canonicalLines(t, harp))
}

// growTo rewrites vendorPath to the whole claude fixture — the session went on.
func growTo(t *testing.T, vendorPath string) {
	t.Helper()
	grown, err := os.ReadFile(vendorFileWithLines(t, 0))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(vendorPath, grown, 0o644))
}

// settledPrefix is a fixture prefix long enough for the session header to
// have settled (the claude reader checkpoints nothing before that), with a
// tool_use whose tool_result is still to come.
const settledPrefix = 8

// TestRefreshVendorTranscript_ResumesFromTheWatermarkWithoutReadingThePrefix:
// growth after the watermark is converted, and the vendor bytes before the
// checkpointed line are never read. Proven by destroying them: the prefix is
// overwritten with same-length junk after the first conversion, which a
// refresh that re-read it would turn into a different transcript. The result
// must still be exactly what converting the real grown file from scratch
// writes — Seq continuous, session ids carried, the open turn and the
// tool_use/tool_result pair spanning the watermark whole.
func TestRefreshVendorTranscript_ResumesFromTheWatermarkWithoutReadingThePrefix(t *testing.T) {
	testsupport.Isolate(t)
	harp := "watermark-resume-harp"
	vendorPath := vendorFileWithLines(t, settledPrefix)
	e := liveClaudeEntry(harp, vendorPath)

	converted, err := ConvertVendorTranscript(context.Background(), engines.Registry(), e)
	require.NoError(t, err)
	require.True(t, converted)
	wm := readWatermark(t, harp)
	require.Positive(t, wm.Vendor.Start)

	growTo(t, vendorPath)
	real, err := os.ReadFile(vendorPath)
	require.NoError(t, err)
	junked := append(bytes.Repeat([]byte("#"), int(wm.Vendor.Start)), real[wm.Vendor.Start:]...)
	require.NoError(t, os.WriteFile(vendorPath, junked, 0o644))

	refresh(t, e)
	resumed := withoutRecordTimes(canonicalLines(t, harp))
	assert.Greater(t, readWatermark(t, harp).Vendor.Offset, wm.Vendor.Offset, "the watermark advances with the growth it converted")
	assertWatermarkDescribesCanonical(t, harp)

	require.NoError(t, os.WriteFile(vendorPath, real, 0o644))
	assert.Equal(t, fullConversionOf(t, harp, vendorPath), resumed)
}

// assertWatermarkDescribesCanonical: the watermark a rebuild leaves must name
// a prefix of the canonical transcript beside it, by length and digest — or
// the next refresh, finding it stale, silently converts in full.
func assertWatermarkDescribesCanonical(t *testing.T, harp string) {
	t.Helper()
	wm := readWatermark(t, harp)
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	raw, err := os.ReadFile(p)
	require.NoError(t, err)
	require.LessOrEqual(t, wm.CanonicalLength, int64(len(raw)))
	sum := sha256.Sum256(raw[:wm.CanonicalLength])
	assert.Equal(t, hex.EncodeToString(sum[:]), wm.CanonicalSHA256)
}

// forgeMarker rewrites harp's canonical prefix to carry a marker and re-signs
// the watermark to match, so the watermark stays valid in every respect a
// test does not deliberately break. The marker survives a refresh only if the
// refresh RESUMED (copying the prefix); a full rebuild re-derives the prefix
// from the vendor file and drops it.
func forgeMarker(t *testing.T, harp string) transcriptWatermark {
	t.Helper()
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	raw, err := os.ReadFile(p)
	require.NoError(t, err)
	wm := readWatermark(t, harp)
	prefix := string(raw[:wm.CanonicalLength])
	require.Contains(t, prefix, `"content":"`)
	forged := strings.Replace(prefix, `"content":"`, `"content":"FORGED-MARKER `, 1)
	require.NoError(t, os.WriteFile(p, append([]byte(forged), raw[wm.CanonicalLength:]...), 0o644))
	sum := sha256.Sum256([]byte(forged))
	wm.CanonicalLength, wm.CanonicalSHA256 = int64(len(forged)), hex.EncodeToString(sum[:])
	writeWatermark(t, harp, wm)
	return wm
}

func carriesMarker(t *testing.T, harp string) bool {
	return strings.Contains(strings.Join(canonicalLines(t, harp), "\n"), "FORGED-MARKER")
}

// TestRefreshVendorTranscript_StaleWatermarkRebuildsInFull: every way a
// watermark can stop describing the files it was taken over must fall back
// to a full conversion — and produce exactly what one writes. The first case
// is the control: a consistent watermark IS resumed from, which is what makes
// the marker's absence in the others mean "rebuilt in full".
func TestRefreshVendorTranscript_StaleWatermarkRebuildsInFull(t *testing.T) {
	testsupport.Isolate(t)
	cases := []struct {
		name    string
		resumes bool
		spoil   func(t *testing.T, harp, vendorPath string, wm transcriptWatermark)
	}{
		{name: "consistent watermark resumes", resumes: true, spoil: func(*testing.T, string, string, transcriptWatermark) {}},
		{name: "canonical prefix edited", spoil: func(t *testing.T, harp, _ string, _ transcriptWatermark) {
			p, err := paths.HarpCanonicalTranscriptPath(harp)
			require.NoError(t, err)
			raw, err := os.ReadFile(p)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(p, bytes.Replace(raw, []byte("FORGED-MARKER"), []byte("FORGED-MARKEX"), 1), 0o644))
		}},
		{name: "canonical shorter than the watermark", spoil: func(t *testing.T, harp, _ string, wm transcriptWatermark) {
			p, err := paths.HarpCanonicalTranscriptPath(harp)
			require.NoError(t, err)
			require.NoError(t, os.Truncate(p, wm.CanonicalLength-1))
		}},
		{name: "vendor file rewritten", spoil: func(t *testing.T, _, vendorPath string, _ transcriptWatermark) {
			raw, err := os.ReadFile(vendorPath)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(vendorPath, append([]byte("\n"), raw...), 0o644))
		}},
		{name: "watermark for another source", spoil: func(t *testing.T, harp, _ string, wm transcriptWatermark) {
			wm.Source += ".old"
			writeWatermark(t, harp, wm)
		}},
		{name: "rotation lineage changed", spoil: func(t *testing.T, harp, _ string, wm transcriptWatermark) {
			wm.Rotations = []string{"rotated-away"}
			writeWatermark(t, harp, wm)
		}},
		{name: "watermark unreadable", spoil: func(t *testing.T, harp, _ string, _ transcriptWatermark) {
			require.NoError(t, os.WriteFile(watermarkPath(t, harp), []byte("{not json"), 0o644))
		}},
		// A wrong-typed field still lets the rest decode; resuming from what
		// did would restart Seq at 0.
		{name: "watermark field of the wrong type", spoil: func(t *testing.T, harp, _ string, _ transcriptWatermark) {
			var m map[string]any
			raw, err := os.ReadFile(watermarkPath(t, harp))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &m))
			m["next_seq"] = "seven"
			raw, err = json.Marshal(m)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(watermarkPath(t, harp), raw, 0o644))
		}},
		{name: "checkpoint state unreadable", spoil: func(t *testing.T, harp, _ string, wm transcriptWatermark) {
			wm.Vendor.State = json.RawMessage(`"not state"`)
			writeWatermark(t, harp, wm)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testsupport.Isolate(t)
			harp := "watermark-stale-harp"
			vendorPath := vendorFileWithLines(t, settledPrefix)
			e := liveClaudeEntry(harp, vendorPath)
			refresh(t, e)
			wm := forgeMarker(t, harp)
			growTo(t, vendorPath)

			tc.spoil(t, harp, vendorPath, wm)
			refresh(t, e)
			if tc.resumes {
				assert.True(t, carriesMarker(t, harp), "a consistent watermark must be resumed from")
				return
			}
			assert.False(t, carriesMarker(t, harp), "a stale watermark must not be resumed from")
			got := withoutRecordTimes(canonicalLines(t, harp))
			assert.Equal(t, fullConversionOf(t, harp, vendorPath), got, "the fallback is a correct full conversion")
		})
	}
}

// TestRefreshVendorTranscript_FailedResumeKeepsTheTranscriptAndWatermark is
// FailedRefreshKeepsTheTranscriptItHad for the resume path: a resumed
// conversion that dies partway leaves both the canonical transcript and the
// watermark exactly as they were.
func TestRefreshVendorTranscript_FailedResumeKeepsTheTranscriptAndWatermark(t *testing.T) {
	testsupport.Isolate(t)
	harp := "watermark-failure-harp"
	vendorPath := vendorFileWithLines(t, settledPrefix)
	e := liveClaudeEntry(harp, vendorPath)
	refresh(t, e)
	before := canonicalLines(t, harp)
	wmBefore, err := os.ReadFile(watermarkPath(t, harp))
	require.NoError(t, err)
	growTo(t, vendorPath)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = RefreshVendorTranscript(ctx, engines.Registry(), e)
	require.ErrorIs(t, err, context.Canceled)

	assert.Equal(t, before, canonicalLines(t, harp))
	wmAfter, err := os.ReadFile(watermarkPath(t, harp))
	require.NoError(t, err)
	assert.Equal(t, wmBefore, wmAfter)
}

// TestRefreshVendorTranscript_NoCheckpointNoWatermark: a rebuild that offers
// no checkpoint (here: the session header has not settled) leaves no
// watermark behind — including removing one an earlier rebuild wrote, which
// would otherwise be re-validated, and rejected, on every refresh.
func TestRefreshVendorTranscript_NoCheckpointNoWatermark(t *testing.T) {
	testsupport.Isolate(t)
	harp := "watermark-none-harp"
	vendorPath := vendorFileWithLines(t, settledPrefix)
	e := liveClaudeEntry(harp, vendorPath)
	refresh(t, e)
	_, err := os.Stat(watermarkPath(t, harp))
	require.NoError(t, err)

	unsettled, err := os.ReadFile(vendorFileWithLines(t, 5)) // the permission mode, no model yet
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(vendorPath, unsettled, 0o644))
	refresh(t, e)
	_, err = os.Stat(watermarkPath(t, harp))
	assert.True(t, os.IsNotExist(err), "no checkpoint, no watermark")
}

// TestRefreshVendorTranscript_UnkeyedBindingGetsNoWatermark: the watermark is
// keyed by the live binding's session id, so a binding without one has
// nowhere to keep it and is converted in full every time.
func TestRefreshVendorTranscript_UnkeyedBindingGetsNoWatermark(t *testing.T) {
	testsupport.Isolate(t)
	harp := "watermark-unkeyed-harp"
	refresh(t, claudeEntry(harp, vendorFileWithLines(t, settledPrefix)))
	dir, err := paths.ResolveHarpSegmentsDir(harp)
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	if !os.IsNotExist(err) {
		require.NoError(t, err)
		assert.Empty(t, entries)
	}
}

// TestRefreshVendorTranscript_NonResumableAdapterIgnoresTheWatermark: a
// watermark is only meaningful to an adapter that can resume. One the binding
// already has must not route a non-resumable adapter into a resume — which
// would copy the prefix and then convert the WHOLE vendor file after it — and
// that adapter failing keeps the transcript and the watermark it found.
func TestRefreshVendorTranscript_NonResumableAdapterIgnoresTheWatermark(t *testing.T) {
	testsupport.Isolate(t)
	harp := "watermark-nonresumable-harp"
	e := liveClaudeEntry(harp, vendorFileWithLines(t, 0))
	e.EngineVersion = stubEngineVersion
	refresh(t, e)
	before := canonicalLines(t, harp)
	wmBefore, err := os.ReadFile(watermarkPath(t, harp))
	require.NoError(t, err)

	e.Backend = registerReaderFixture(t, partialFailAdapter{n: 3})
	_, err = RefreshVendorTranscript(context.Background(), engines.Registry(), e)
	require.Error(t, err)

	assert.Equal(t, before, canonicalLines(t, harp))
	wmAfter, err := os.ReadFile(watermarkPath(t, harp))
	require.NoError(t, err)
	assert.Equal(t, wmBefore, wmAfter)
}
