package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// The vendor-reader roster is a VIEW over the backend registry: an engine is
// readable here exactly when its descriptor provides TranscriptReaders, and
// an engine whose Transcripts() is empty is a stated gap, not a missed
// entry. This is the reverse of the arch floor (every listed name is
// registered): every registered name whose kind supplies readers IS listed,
// so a lookup miss can no longer mean "somebody forgot the table".
func TestVendorReaderAdaptersFor_AgreesWithEveryRegisteredDeclaration(t *testing.T) {
	names := EngineNames()
	require.NotEmpty(t, names)
	for _, name := range names {
		declared, provided := declaredReaders(name)
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
	enginefixture.Install(t, enginefixture.Kind(name))

	_, ok := VendorReaderAdaptersFor(name)
	assert.False(t, ok)
	assert.NotContains(t, VendorReaderEngineNames(), name)
	_, ok = declaredReaders("never-registered")
	assert.False(t, ok, "an unregistered name has no readers either")
}

// declaredReaders reads the named kind's own Transcripts as vendor adapters.
func declaredReaders(name string) ([]vendorreader.VersionedAdapter, bool) {
	kind, ok := engines.Registry().Lookup(engine.Name(name))
	if !ok {
		return nil, false
	}
	var out []vendorreader.VersionedAdapter
	for _, r := range kind.Transcripts() {
		if a, ok := r.(vendorreader.VersionedAdapter); ok {
			out = append(out, a)
		}
	}
	return out, len(out) > 0
}
