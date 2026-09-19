package operations

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// SetLLM / RemoveLLM: `llm create`/`llm edit`/`llm remove`'s shared write
// core, on Manager.Update — mirrors agent_write_test.go's coverage for the
// agent CRUD sibling this closes the parity gap with.
// =============================================================================

// TestSetLLM_CreatesAndPersists proves the write half round-trips through a
// real config.yaml: a fresh label with a type, model and permissions posture
// all land and survive a reload.
func TestSetLLM_CreatesAndPersists(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(t, appDir)

	entry, err := SetLLM(context.Background(), mgr, SetLLMRequest{
		Label:       "big",
		Type:        ptr("mock"),
		Model:       ptr("o1"),
		Permissions: ptr("bypass"),
	})
	require.NoError(t, err)
	assert.Equal(t, "big", entry.Label)
	assert.Equal(t, "mock", entry.Type)
	assert.Equal(t, "o1", entry.Model)
	assert.Equal(t, "bypass", entry.Permissions)

	reloaded, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	got, ok := reloaded.GetLLMEntry("big")
	require.True(t, ok, "the created llm must survive a reload")
	assert.Equal(t, "mock", got.Type)
}

// TestSetLLM_RefusesNonRegisteredSpellings pins the write BOUNDARY: an engine
// has one name, so a retired short spelling or a case variant is an unknown
// type, refused with nothing written. config.json pins llm.configs.*.type to
// a const per backend, so an entry under any other spelling would warn on
// every subsequent config load.
func TestSetLLM_RefusesNonRegisteredSpellings(t *testing.T) {
	for _, spelling := range []string{"claude", "CLAUDE", "Claude-Code", "claudecode"} {
		t.Run(spelling, func(t *testing.T) {
			_, appDir := loadConfigDir(t, "version: 5\n")
			mgr := managerFor(t, appDir)

			_, err := SetLLM(context.Background(), mgr, SetLLMRequest{Label: "big", Type: ptr(spelling)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown type")
			assert.Contains(t, err.Error(), "claude-code", "the refusal names the registered spelling")

			reloaded, err := configload.Load(configload.WithAppDir(appDir))
			require.NoError(t, err)
			_, ok := reloaded.GetLLMEntry("big")
			assert.False(t, ok, "a refused type must write nothing")
		})
	}
}

// TestSetLLM_RejectsUnknownType pins the write-time membership check: a type
// no backend registers leaves the entry broken (EffectiveType would silently
// degrade at resolve time). Nothing must be written.
func TestSetLLM_RejectsUnknownType(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(t, appDir)

	_, err := SetLLM(context.Background(), mgr, SetLLMRequest{Label: "big", Type: ptr("bogus-backend")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus-backend")

	reloaded, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := reloaded.GetLLMEntry("big")
	assert.False(t, ok, "a rejected SetLLM call must persist nothing")
}

// TestSetLLM_EditOnlyChangesNamedFields proves edit semantics: a field the
// caller did not name keeps its stored value, mirroring SetAgent's contract
// (`agent edit dev --runtime container` must not wipe dev's engine).
func TestSetLLM_EditOnlyChangesNamedFields(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(t, appDir)

	_, err := SetLLM(context.Background(), mgr, SetLLMRequest{Label: "big", Type: ptr("mock"), Model: ptr("o1")})
	require.NoError(t, err)

	entry, err := SetLLM(context.Background(), mgr, SetLLMRequest{Label: "big", Permissions: ptr("plan")})
	require.NoError(t, err)
	assert.Equal(t, "mock", entry.Type, "an unnamed field must survive an edit that names a different one")
	assert.Equal(t, "o1", entry.Model)
	assert.Equal(t, "plan", entry.Permissions)
}

// TestRemoveLLM_DeletesAndPersists proves the removal round-trips.
func TestRemoveLLM_DeletesAndPersists(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(t, appDir)

	_, err := SetLLM(context.Background(), mgr, SetLLMRequest{Label: "big", Type: ptr("mock")})
	require.NoError(t, err)

	cfg, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	require.NoError(t, RemoveLLM(context.Background(), mgr, cfg, "big"))

	reloaded, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := reloaded.GetLLMEntry("big")
	assert.False(t, ok, "removed llm must not survive a reload")
}

// TestRemoveLLM_UnknownLabelErrors: removing a label config.yaml never
// declared (including a bare backend name like "claude-code", which has no
// config entry to delete — mergeDefaultConfig's whole-registry fallback
// fills an EMPTY llm.configs with it, but that is not a user declaration,
// see IsLLMUserAuthored) is an error, never a silent zero-effect success.
func TestRemoveLLM_UnknownLabelErrors(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(t, appDir)

	err := RemoveLLM(context.Background(), mgr, cfg, "claude-code")
	require.Error(t, err)
}

// TestRemoveLLM_UserDeclaredOverrideOfADefaultName_Succeeds is the positive
// mirror: a label sharing a shipped default's NAME but with an explicit
// user override IS removable — IsLLMUserAuthored must not blanket-refuse
// every default-shaped name, only the ones the user never actually wrote.
func TestRemoveLLM_UserDeclaredOverrideOfADefaultName_Succeeds(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\nllm:\n  configs:\n    claude-code: { permissions: bypass }\n")
	mgr := managerFor(t, appDir)
	cfg, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)

	require.NoError(t, RemoveLLM(context.Background(), mgr, cfg, "claude-code"))

	reloaded, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	assert.False(t, reloaded.IsLLMUserAuthored("claude-code"))
}
