package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// SetLLM's --set path: the assignments land on the entry, and the result is
// checked — by the schema, then by the label's engine for its permission
// keys — before anything is written.

// TestSetLLM_ASingleSetLands: one assignment is applied and persisted.
func TestSetLLM_ASingleSetLands(t *testing.T) {
	_, appDir := loadConfigDir(t, "schema_version: 8\n")
	mgr := managerFor(t, appDir)

	_, err := SetLLM(context.Background(), mgr, SetLLMRequest{Label: "big", Type: ptr("mock"), Set: []string{"permissions.approver=none"}})
	require.NoError(t, err)

	reloaded, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	got, ok := reloaded.GetLLMEntry("big")
	require.True(t, ok)
	assert.Equal(t, "none", got.Permissions.Approver)
}

// TestSetLLM_ASetTheNextLoadWouldRefuseWritesNothing: a field the schema does
// not know, a permission key the label's engine refuses, and an assignment
// that cannot be applied at all are each refused with nothing written.
func TestSetLLM_ASetTheNextLoadWouldRefuseWritesNothing(t *testing.T) {
	for name, set := range map[string]string{
		"a field the schema does not know":    "no_such_field=1",
		"a permission key the engine refuses": "permissions.mode=acceptEdits",
		"a permission its engine never takes": "permissions.no_such_setting=1",
		"an assignment with no key":           "=1",
	} {
		t.Run(name, func(t *testing.T) {
			_, appDir := loadConfigDir(t, "schema_version: 8\n")
			mgr := managerFor(t, appDir)

			_, err := SetLLM(context.Background(), mgr, SetLLMRequest{Label: "big", Type: ptr("mock"), Set: []string{set}})
			require.Error(t, err)

			reloaded, err := configload.Load(configload.WithAppDir(appDir))
			require.NoError(t, err)
			_, ok := reloaded.GetLLMEntry("big")
			assert.False(t, ok, "a refused --set writes nothing")
		})
	}
}

// TestApplyLLMFields_RefusesWhatTheEntryCannotHold: a value of the wrong shape
// for a typed field, and a path through a nested scalar, are refused.
func TestApplyLLMFields_RefusesWhatTheEntryCannotHold(t *testing.T) {
	_, err := applyLLMFields(config.LLMConfig{Type: "opencode"}, []string{"permissions.approver={a: 1}"})
	require.Error(t, err, "approver is a string, not an object")

	entry := config.LLMConfig{Type: "opencode", Body: map[string]any{"provider": map[string]any{"options": 5}}}
	_, err = applyLLMFields(entry, []string{"provider.options.timeout=1"})
	require.ErrorContains(t, err, "not an object")
}
