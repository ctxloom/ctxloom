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
// an engine that declares them absent is a stated gap, not a missed entry.
// This is the reverse of the arch floor (every listed name is registered):
// every registered name that declares readers IS listed, so a lookup miss can
// no longer mean "somebody forgot the table".
func TestVendorReaderAdaptersFor_AgreesWithEveryRegisteredDeclaration(t *testing.T) {
	names := backends.List()
	require.NotEmpty(t, names)
	for _, name := range names {
		declared, provided := backends.TranscriptReadersFor(name).Get()
		got, ok := VendorReaderAdaptersFor(name)
		assert.Equal(t, provided, ok, "%s: the reader roster must carry the engine exactly when its descriptor provides readers", name)
		if provided {
			assert.Equal(t, declared, got, "%s: the roster must hand back the engine's own declared adapters", name)
			assert.Contains(t, VendorReaderEngineNames(), name)
		} else {
			assert.NotEmpty(t, backends.TranscriptReadersFor(name).AbsentReason(), "%s: an engine outside the roster must say why", name)
			assert.NotContains(t, VendorReaderEngineNames(), name)
		}
	}
}

// An engine that declares NO transcript store is absent from the roster with
// its reason intact — the difference between a legitimate absence and a
// forgotten entry is that the former can be read back.
func TestVendorReaderAdaptersFor_DeclaredAbsentEngineIsNotReadable(t *testing.T) {
	const name = "fixture-no-transcripts"
	require.NoError(t, backends.Register(enginefixture.Descriptor(name)))
	t.Cleanup(func() { backends.UnregisterForTesting(name) })

	_, ok := VendorReaderAdaptersFor(name)
	assert.False(t, ok)
	assert.NotContains(t, VendorReaderEngineNames(), name)
	assert.NotEmpty(t, backends.TranscriptReadersFor(name).AbsentReason())
	assert.Empty(t, backends.TranscriptReadersFor("never-registered").AbsentReason(),
		"an unregistered name is undecided, not declared absent")
}
