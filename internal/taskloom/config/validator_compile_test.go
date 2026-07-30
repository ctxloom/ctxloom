package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
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

// TestLoad_ValidationFailureReturnsZeroConfig pins that Load never hands back
// a config it just refused.
//
// Every other error return in Load yields Config{}; the validation-failure
// path was the one that returned the decoded value alongside the error. A
// caller that mishandles the error then holds a config whose contents the
// schema rejected — the "homing" case is the sharp one, since the value that
// failed the enum is the value that selects which task store gets written.
// One error contract, uniformly: on error there is no config.
func TestLoad_ValidationFailureReturnsZeroConfig(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "homing: sometimes\n")

	cfg, err := Load(project, nil)
	require.Error(t, err)
	assert.Equal(t, Config{}, cfg,
		"a rejected config must not escape alongside its rejection")
}
