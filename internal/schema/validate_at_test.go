package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateAt_RefusesWhatTheFileDoorRefuses is the gate the env/--config-set
// channels never had: those values reach the merged document without passing
// through ValidateBytes, so an enum-typed key could be set to anything from
// either channel while the identical value written into config.yaml was
// refused. The permissions row is the one that mattered — `plann` is an obvious
// `plan`, and the resolver's fallback for a value it cannot parse used to be
// the claude-code host stopgap, i.e. bypass.
func TestValidateAt_RefusesWhatTheFileDoorRefuses(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	cases := []struct {
		name  string
		path  []string
		value any
	}{
		{"agent permissions typo", []string{"agents", "reviewer", "permissions"}, "plann"},
		{"project permissions typo", []string{"permissions"}, "plann"},
		{"llm label permissions typo", []string{"llm", "configs", "big", "permissions"}, "plann"},
		{"a string key handed a list by the comma coercion", []string{"llm", "configs", "big", "model"}, []any{"opus", "sonnet"}},
		{"a string key handed an int by the numeric coercion", []string{"llm", "configs", "big", "model"}, 5},
		{"an integer key handed a bool by the 0/1 coercion", []string{"delegation", "concurrency"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, v.ValidateAt(tc.path, tc.value))
		})
	}
}

// TestValidateAt_AcceptsLegitimateValues is the false-positive control: every
// row here is a value a user may legitimately set from either override channel,
// and a gate that refused any of them would break the channel it is meant to
// protect.
func TestValidateAt_AcceptsLegitimateValues(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	cases := []struct {
		name  string
		path  []string
		value any
	}{
		{"a legal enum member", []string{"agents", "reviewer", "permissions"}, "plan"},
		{"a legal string", []string{"llm", "configs", "big", "model"}, "opus"},
		{"a legal integer", []string{"delegation", "concurrency"}, 4},
		{"a legal list", []string{"agents", "reviewer", "profiles"}, []any{"default"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NoError(t, v.ValidateAt(tc.path, tc.value))
		})
	}
}

// TestValidateAt_SilentWhereItHasNoAnswer pins the two non-answers, both of
// which must stay non-answers rather than become errors: a path the schema does
// not name at all is diagnosed by the unknown-key machinery in its own
// vocabulary, and a location under `additionalProperties: true` (an LLM
// backend's env passthrough) names every key and constrains none. A nil
// receiver degrades rather than panicking, matching the rest of this package.
func TestValidateAt_SilentWhereItHasNoAnswer(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	require.False(t, v.KnownPath([]string{"not_a_config_key"}), "fixture check: this path really is unknown")
	assert.NoError(t, v.ValidateAt([]string{"not_a_config_key"}, "whatever"), "an unknown path is not this gate's diagnosis")

	envPath := []string{"llm", "configs", "big", "env", "ANY_NAME"}
	require.True(t, v.KnownPath(envPath), "fixture check: the passthrough IS a known location, so this row is not vacuous")
	assert.NoError(t, v.ValidateAt(envPath, "a,b"), "a free-form passthrough constrains nothing")
	assert.NoError(t, v.ValidateAt(nil, "whatever"), "an empty path names nothing")

	var nilV *ConfigValidator
	assert.NoError(t, nilV.ValidateAt([]string{"permissions"}, "plann"), "a nil validator degrades, it never panics")
}

// TestValidateAt_ValidatesTheValueNotASyntheticDocument pins WHY the value is
// validated against its own sub-schema rather than wrapped in a one-key
// document and run through ValidateBytes: an object with `required` members
// would then fail over keys the override never mentioned. mcp.servers.<name>
// requires `command`, so setting only `args` must NOT be reported as a missing
// command.
func TestValidateAt_ValidatesTheValueNotASyntheticDocument(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)

	argsPath := []string{"mcp", "servers", "x", "args"}
	require.True(t, v.KnownPath(argsPath), "fixture check: the path resolves, so this row is not vacuous")
	require.Error(t, v.ValidateBytes([]byte("mcp:\n  servers:\n    x:\n      args: [--flag]\n")),
		"fixture check: as a whole document this really does fail the required-command rule")
	assert.NoError(t, v.ValidateAt(argsPath, []any{"--flag"}),
		"an override that named only args must not be reported as a missing command")
}
