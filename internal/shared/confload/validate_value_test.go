package confload

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refuseAt builds a ValidateValue hook that refuses exactly one resolved path,
// so a test can tell "the hook ran on the path I meant" apart from "the hook
// refused everything".
func refuseAt(path string) func([]string, any) error {
	return func(got []string, value any) error {
		if strings.Join(got, ".") != path {
			return nil
		}
		return fmt.Errorf("value %v is refused at %s", value, path)
	}
}

// TestApplyOverrides_SchemaRefusalIsReportedAndStillApplied pins both halves of
// the override schema gate. Reported: a value the product's schema refuses
// comes back as a typed SchemaViolationError a caller can classify, where
// before it produced nothing at all — the env/--config-set channels were the
// one door into the config that no schema ever saw. Still applied: the value
// stays in the resolved layer, because dropping it would resolve the key to
// whatever the layer below says, and for a privilege-carrying key that is how a
// typo silently ends up MORE permissive than the value that was typed.
func TestApplyOverrides_SchemaRefusalIsReportedAndStillApplied(t *testing.T) {
	p := testProduct("agents.reviewer.permissions")
	p.ValidateValue = refuseAt("agents.reviewer.permissions")

	base := map[string]any{"agents": map[string]any{"reviewer": map[string]any{"permissions": "plan"}}}
	o := Overrides{Flags: map[string]any{"agents.reviewer.permissions": "plann"}}

	out, err := p.ApplyOverrides(base, o)
	require.Error(t, err, "a refused override must not resolve silently")

	var schemaErr *SchemaViolationError
	require.True(t, errors.As(err, &schemaErr), "the fault must be classifiable as a schema violation, not folded into the generic parse bucket")
	assert.Equal(t, SourceFlag, schemaErr.Source)
	assert.Equal(t, []string{"agents", "reviewer", "permissions"}, schemaErr.Path)
	assert.Contains(t, schemaErr.Error(), "agents.reviewer.permissions")

	agents := out["agents"].(map[string]any)
	reviewer := agents["reviewer"].(map[string]any)
	assert.Equal(t, "plann", reviewer["permissions"],
		"the refused value is REPORTED, not dropped: dropping it would silently restore the layer below")
}

// TestApplyOverrides_SchemaGateCoversBothChannels: env and --config-set are two
// doors to the same document, and a gate on only one of them leaves the defect
// reachable through the other.
func TestApplyOverrides_SchemaGateCoversBothChannels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		o      Overrides
		source OverrideSource
	}{
		{"env", Overrides{Env: map[string]any{"PERMISSIONS": "plann"}}, SourceEnv},
		{"config-set", Overrides{Flags: map[string]any{"permissions": "plann"}}, SourceFlag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProduct("permissions")
			p.ValidateValue = refuseAt("permissions")

			_, err := p.ApplyOverrides(map[string]any{"permissions": "plan"}, tc.o)
			var schemaErr *SchemaViolationError
			require.True(t, errors.As(err, &schemaErr), "channel %s must reach the gate", tc.name)
			assert.Equal(t, tc.source, schemaErr.Source)
		})
	}
}

// TestApplyOverrides_SchemaGateStaysQuietOnAcceptedValues is the false-positive
// control: a hook that accepts must leave ApplyOverrides exactly as it was, or
// every legitimate override starts carrying a warning.
func TestApplyOverrides_SchemaGateStaysQuietOnAcceptedValues(t *testing.T) {
	var seen [][]string
	p := testProduct("agents.reviewer.permissions")
	p.ValidateValue = func(path []string, _ any) error {
		seen = append(seen, path)
		return nil
	}

	out, err := p.ApplyOverrides(map[string]any{}, Overrides{Flags: map[string]any{"agents.reviewer.permissions": "plan"}})
	require.NoError(t, err)
	require.Len(t, seen, 1, "fixture check: the hook must actually have been consulted")
	assert.Equal(t, []string{"agents", "reviewer", "permissions"}, seen[0])

	agents := out["agents"].(map[string]any)
	reviewer := agents["reviewer"].(map[string]any)
	assert.Equal(t, "plan", reviewer["permissions"])
}

// TestApplyOverrides_ScopeDropSkipsTheSchemaGate: a value the layer may not
// carry at all is dropped by ScopeAllows and never reaches the document, so
// complaining about its SHAPE as well would report a second fault about a value
// nothing will read.
func TestApplyOverrides_ScopeDropSkipsTheSchemaGate(t *testing.T) {
	p := testProduct("permissions")
	p.ScopeAllows = func(OverrideSource, []string) (bool, string) { return false, "env may not carry a privilege grant" }
	validated := 0
	p.ValidateValue = func([]string, any) error {
		validated++
		return nil
	}

	_, err := p.ApplyOverrides(map[string]any{"permissions": "plan"}, Overrides{Env: map[string]any{"PERMISSIONS": "plann"}})
	var scopeErr *ScopeViolationError
	require.True(t, errors.As(err, &scopeErr), "fixture check: the scope drop must be what fired")
	assert.Zero(t, validated, "a dropped override is never schema-checked")
}

// TestApplyOverrides_NilValidateValueIsTodaysBehaviour: the hook is optional, so
// a product that supplies none must resolve overrides exactly as before.
func TestApplyOverrides_NilValidateValueIsTodaysBehaviour(t *testing.T) {
	p := testProduct("permissions")
	require.Nil(t, p.ValidateValue, "fixture check: no hook installed")

	out, err := p.ApplyOverrides(map[string]any{"permissions": "plan"}, Overrides{Flags: map[string]any{"permissions": "plann"}})
	require.NoError(t, err)
	assert.Equal(t, "plann", out["permissions"])
}
