package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/schema"
)

// TestNewValidator_EmbeddedSchemaAlwaysCompiles pins the property that makes
// loadRaw's deliberately discarded validator error unobservable.
//
// loadRaw degrades to a nil validator when the compile fails, and says
// nothing about it. That would be a silent fault if it could ship — but it
// cannot: loadRaw's only production caller is Load, which recompiles the same
// embedded, build-time schema two statements later and returns
// "taskloom: load config schema: ..." rather than validating nothing. The
// compile is deterministic over a resource baked into the binary, so it
// either fails for every invocation or for none, and the failing case never
// reaches a user without that error.
//
// The whole argument rests on the compile succeeding in a shipped binary, so
// that is what is pinned here. Break the embedded schema and this goes red —
// which is the point: the silent-degrade branch stops being unreachable at
// exactly the moment this assertion stops holding.
func TestNewValidator_EmbeddedSchemaAlwaysCompiles(t *testing.T) {
	v, err := newValidator()
	require.NoError(t, err, "the embedded taskloom config schema must compile in every built binary")
	require.NotNil(t, v)

	// The compile really can fail — the assertion above is not vacuous.
	_, err = schema.NewValidatorFromSchema([]byte(`{"type":`))
	assert.Error(t, err, "a malformed schema document must be a compile error, not a nil-error nil validator")
}
