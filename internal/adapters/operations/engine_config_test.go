package operations

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A labeled entry's body decodes into the typed config of the engine its
// TYPE names, and the decoded config reports that engine back — never a
// sibling's.
func TestDecodeEngineConfig_DecodesIntoTheNamedEnginesOwnConfig(t *testing.T) {
	for _, name := range EngineNames(engines.Registry()) {
		cfg, err := DecodeEngineConfig(engines.Registry(), name, map[string]any{"model": "m"})
		require.NoError(t, err, name)
		assert.Equal(t, name, cfg.BackendType())
	}
}

// An unknown type is an error the caller degrades; a body the type cannot
// decode is an error that NAMES the engine, so a multi-backend config load
// can attribute the failure to its source entry.
func TestDecodeEngineConfig_RefusesUnknownTypesAndNamesTheEngineOnBadBodies(t *testing.T) {
	_, err := DecodeEngineConfig(engines.Registry(), "no-such-engine", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-engine")

	_, err = DecodeEngineConfig(engines.Registry(), "mock", map[string]any{"model": []int{1}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `backend "mock"`)
}
