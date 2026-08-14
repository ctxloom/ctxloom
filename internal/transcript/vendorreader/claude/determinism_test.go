package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// TestConvert_DeterministicAcrossFreshAndSharedAdapterInstances pins the
// claim claude.go's doc comment already makes ("Stateless: ... one Adapter
// value is safe to reuse ... across concurrent Convert calls") by actually
// exercising it, rather than trusting the comment: given the SAME clock (a
// real conversion pins it to the source file's own mtime via
// operations.vendorSourceClock, one layer up — this test fixes it directly,
// since that helper is unexported there), converting the identical fixture
// through a FRESH Adapter{} value and through the SHARED
// VersionedAdapters[0].Adapter package var must produce byte-IDENTICAL
// canonical output.
//
// This is the ticket's original hypothesis (taskloom luxurious-roast:
// "stateful adapter — memoized ids? counters?") and it holds: Adapter really
// has no state to leak between calls. It would NOT, on its own, have caught
// this project's actual regression (transcript.fileRecorder's wall-clock TS,
// not adapter state) — see
// operations.TestConvertVendorTranscript_Deterministic_ReconversionsAreByteIdentical
// for the pin that exercises the real clock plumbing end to end, with no
// clock fixed by hand.
func TestConvert_DeterministicAcrossFreshAndSharedAdapterInstances(t *testing.T) {
	testsupport.Isolate(t)
	fixed := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return fixed }

	convert := func(t *testing.T, a vendorreader.VendorAdapter, label string) []byte {
		t.Helper()
		out := filepath.Join(t.TempDir(), "out.jsonl")
		rec, err := transcript.NewRecorder(fixtureHarp, "claude", transcript.WithPath(out), transcript.WithClock(clock))
		require.NoError(t, err)
		err = a.Convert(context.Background(), rec, filepath.Join("testdata", "transcript-fixture.jsonl"))
		require.NoError(t, err)
		require.NoError(t, rec.Close())
		b, err := os.ReadFile(out)
		require.NoError(t, err)
		require.NotEmpty(t, b, label)
		return b
	}

	fresh1 := convert(t, Adapter{}, "fresh Adapter{} #1")
	fresh2 := convert(t, Adapter{}, "fresh Adapter{} #2")
	shared := convert(t, VersionedAdapters[0].Adapter, "shared VersionedAdapters[0].Adapter")

	assert.Equal(t, fresh1, fresh2,
		"two fresh Adapter{} conversions of the same source under the same clock must be byte-identical")
	assert.Equal(t, fresh1, shared,
		"the shared VersionedAdapters instance must convert byte-identically to a fresh Adapter{}")
}
