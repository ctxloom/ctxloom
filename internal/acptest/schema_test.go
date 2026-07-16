package acptest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidator_KnownGoodAndBad pins the validator's own behavior against two
// unambiguous cases from the current schema, so a regression in the harness
// itself (e.g. a compiler misconfiguration that accepts everything) fails
// loudly here rather than silently passing every L0 conformance test.
func TestValidator_KnownGoodAndBad(t *testing.T) {
	v, err := NewValidator()
	require.NoError(t, err)

	t.Run("valid NewSessionResponse passes", func(t *testing.T) {
		err := v.ValidateDef("NewSessionResponse", json.RawMessage(`{"sessionId":"abc"}`))
		assert.NoError(t, err)
	})

	t.Run("NewSessionResponse missing required sessionId fails", func(t *testing.T) {
		err := v.ValidateDef("NewSessionResponse", json.RawMessage(`{"modes":{}}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sessionId")
	})

	t.Run("null result fails an object-typed response schema", func(t *testing.T) {
		// This is the exact shape ctxloom's fs/write_text_file response takes
		// (internal/acp/jsonrpc.marshalResult renders a nil result as JSON
		// null) — pinned here so the L0 divergence it feeds stays honest.
		err := v.ValidateDef("WriteTextFileResponse", json.RawMessage(`null`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected object, but got null")
	})

	t.Run("empty raw validates as null, not skipped", func(t *testing.T) {
		err := v.ValidateDef("WriteTextFileResponse", nil)
		require.Error(t, err, "an empty payload must be measured, never silently treated as valid")
	})
}
