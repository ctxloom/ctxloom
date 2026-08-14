package operations

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// canonicalBytes reads harp's canonical transcript.jsonl back in full
// (unlike canonicalLines, which trims to non-empty lines and loses exact
// byte layout), or nil if it was never created.
func canonicalBytes(t *testing.T, harp string) []byte {
	t.Helper()
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return b
}

// TestConvertVendorTranscript_Deterministic_ReconversionsAreByteIdentical is
// the determinism pin for the merge-blocking defect filed as taskloom
// luxurious-roast: converting the SAME, UNCHANGED vendor transcript must
// produce byte-IDENTICAL canonical output every time, not merely
// same-length-or-longer (TestConvertVendorTranscriptOnExit_ConvertsBoundTranscript,
// one layer up in internal/cli, only ever asserted GreaterOrEqual, which is
// exactly the assertion an actively non-deterministic byte count could still
// fail — "4915" is not >= "4917" was the live repro).
//
// Root cause: transcript.fileRecorder stamped every record's TS from
// time.Now().UTC() (recorder.go's now field) with no way to override it —
// including on THIS import path, which re-reads and re-writes the whole
// vendor transcript from scratch on every call
// (convertVendorTranscript's doc comment). TS is RFC3339Nano, which trims
// trailing fractional zeros, so its formatted width varies from call to
// call even when nothing about the source did — corrupting any staleness
// fingerprint or byte-exact fixture assertion built on the canonical bytes.
// Reconversion used to be rare (a permanent presence-guarded no-op); it is
// now routine (RefreshVendorTranscript's refresh-once exit-capture heal —
// internal/cli/run.go's convertVendorTranscriptOnExit), which is what makes
// this a live, not theoretical, defect.
//
// The fix is transcript.WithClock, wired through vendorSourceClock: every
// record's TS is now the SOURCE FILE's own mtime, which does not change
// unless the source itself is rewritten.
//
// This test exercises the real, public entry points — ConvertVendorTranscript
// then RefreshVendorTranscript, twice — rather than fixing a clock by hand,
// so a regression anywhere on the path (the adapter, the recorder, or the
// clock plumbing between operations and transcript) turns it red. A
// reintroduced time.Now() on this path kills it: two Convert calls
// separated by a real sleep essentially never produce the identical
// RFC3339Nano value byte-for-byte, so exact equality — not just length — is
// the assertion, deliberately.
func TestConvertVendorTranscript_Deterministic_ReconversionsAreByteIdentical(t *testing.T) {
	testsupport.Isolate(t)
	harp := "determinism-pin-harp"
	e := claudeEntry(harp, claudeFixturePath)

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)
	first := canonicalBytes(t, harp)
	require.NotEmpty(t, first)

	// A real interval between conversions so a wall-clock regression is not
	// masked by two calls landing within the same clock tick.
	time.Sleep(50 * time.Millisecond)

	converted, err = RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)
	second := canonicalBytes(t, harp)

	time.Sleep(50 * time.Millisecond)

	converted, err = RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)
	third := canonicalBytes(t, harp)

	assert.Equal(t, first, second,
		"re-converting an unchanged vendor transcript must produce byte-identical canonical output")
	assert.Equal(t, first, third,
		"a third reconversion of the same unchanged source must still be byte-identical")
}
