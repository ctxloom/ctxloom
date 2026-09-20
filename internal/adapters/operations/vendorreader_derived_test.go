package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// The vendor-reader roster is a VIEW over the backend registry: an engine is
// readable here exactly when its descriptor provides TranscriptReaders, and
// an engine whose Transcripts() is empty is a stated gap, not a missed
// entry. This is the reverse of the arch floor (every listed name is
// registered): every registered name whose kind supplies readers IS listed,
// so a lookup miss can no longer mean "somebody forgot the table".
func TestVendorReaderAdaptersFor_AgreesWithEveryRegisteredDeclaration(t *testing.T) {
	names := backends.List()
	require.NotEmpty(t, names)
	for _, name := range names {
		declared, provided := backends.TranscriptReadersFor(name)
		got, ok := VendorReaderAdaptersFor(name)
		assert.Equal(t, provided, ok, "%s: the reader roster must carry the engine exactly when its kind supplies readers", name)
		if provided {
			assert.Equal(t, declared, got, "%s: the roster must hand back the engine's own readers", name)
			assert.Contains(t, VendorReaderEngineNames(), name)
		} else {
			assert.NotContains(t, VendorReaderEngineNames(), name)
		}
	}
}

// An engine whose kind supplies NO transcript readers is absent from the
// roster: an empty slice, not an error and not a flag.
func TestVendorReaderAdaptersFor_EngineWithoutReadersIsNotReadable(t *testing.T) {
	const name = "fixture-no-transcripts"
	require.NoError(t, backends.Register(enginefixture.Registry(enginefixture.Hosting(name)), enginefixture.Hosting(name)))
	t.Cleanup(func() { backends.UnregisterForTesting(name) })

	_, ok := VendorReaderAdaptersFor(name)
	assert.False(t, ok)
	assert.NotContains(t, VendorReaderEngineNames(), name)
	_, ok = backends.TranscriptReadersFor("never-registered")
	assert.False(t, ok, "an unregistered name has no readers either")
}
