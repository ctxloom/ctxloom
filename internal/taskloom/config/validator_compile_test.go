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

// TestNewValidator_CompilesOncePerProcess pins that the embedded config
// schema is compiled once, not once per call.
//
// Every taskloom command resolved the config twice, and each resolution
// compiled the schema twice (loadRaw and Load), for four JSON Schema compiles
// on the startup path of a CLI that mostly does one small thing and exits.
// The schema is baked into the binary and cannot change while the process
// runs, so the compile has exactly one possible outcome; identity of the
// returned validator is the observable form of "it was not recompiled".
func TestNewValidator_CompilesOncePerProcess(t *testing.T) {
	a, err := newValidator()
	require.NoError(t, err)
	b, err := newValidator()
	require.NoError(t, err)
	assert.Same(t, a, b, "the embedded schema must be compiled once per process, not once per call")
}

// TestConfigResolveMode_MatchesPackageResolveMode pins that splitting the
// flag-vs-config half out of ResolveMode kept the two answers identical
// across every arm of the precedence chain — the property that makes
// taskContextSingle's single Load a pure de-duplication and not a change of
// behaviour.
func TestConfigResolveMode_MatchesPackageResolveMode(t *testing.T) {
	cases := []struct {
		name, body, flag string
	}{
		{"unset everywhere", "", ""},
		{"config home", "homing: home\n", ""},
		{"config repo", "homing: repo\n", ""},
		{"flag beats config", "homing: home\n", "repo"},
		{"invalid flag", "homing: home\n", "nonsense"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := taskstest.ProjectDir(t)
			if tc.body != "" {
				writeConfig(t, project, tc.body)
			}

			wantMode, wantErr := ResolveMode(project, nil, tc.flag)

			cfg, err := Load(project, nil)
			require.NoError(t, err)
			gotMode, gotErr := cfg.ResolveMode(tc.flag)

			assert.Equal(t, wantMode, gotMode)
			if wantErr != nil {
				require.Error(t, gotErr)
				assert.Equal(t, wantErr.Error(), gotErr.Error())
				return
			}
			assert.NoError(t, gotErr)
		})
	}
}
