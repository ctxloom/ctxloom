package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
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
	mgr := managerFor(appDir)

	entry, err := SetLLM(mgr, SetLLMRequest{
		Label:       "big",
		Type:        ptr("codex"),
		Model:       ptr("o1"),
		Permissions: ptr("bypass"),
	})
	require.NoError(t, err)
	assert.Equal(t, "big", entry.Label)
	assert.Equal(t, "codex", entry.Type)
	assert.Equal(t, "o1", entry.Model)
	assert.Equal(t, "bypass", entry.Permissions)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	got, ok := reloaded.GetLLMEntry("big")
	require.True(t, ok, "the created llm must survive a reload")
	assert.Equal(t, "codex", got.Type)
}

// TestSetLLM_StoresTheCanonicalType pins the write BOUNDARY: an accepted alias
// or case variant of an engine name is persisted in its canonical spelling.
// config.json pins llm.configs.*.type to a const per backend, so storing what
// the caller typed would write an entry that resolves at every read and still
// warns on every subsequent config load.
func TestSetLLM_StoresTheCanonicalType(t *testing.T) {
	for _, spelling := range []string{"claude", "CLAUDE", "Claude-Code", "claudecode"} {
		t.Run(spelling, func(t *testing.T) {
			_, appDir := loadConfigDir(t, "version: 5\n")
			mgr := managerFor(appDir)

			entry, err := SetLLM(mgr, SetLLMRequest{Label: "big", Type: ptr(spelling)})
			require.NoError(t, err)
			assert.Equal(t, "claude-code", entry.Type)

			reloaded, err := config.Load(config.WithAppDir(appDir))
			require.NoError(t, err)
			got, ok := reloaded.GetLLMEntry("big")
			require.True(t, ok)
			assert.Equal(t, "claude-code", got.Type, "the stored type must be canonical, not what was typed")
		})
	}
}

// TestSetLLM_RejectsUnknownType pins the write-time membership check: a type
// no backend registers leaves the entry broken (EffectiveType would silently
// degrade at resolve time). Nothing must be written.
func TestSetLLM_RejectsUnknownType(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	_, err := SetLLM(mgr, SetLLMRequest{Label: "big", Type: ptr("bogus-backend")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus-backend")

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := reloaded.GetLLMEntry("big")
	assert.False(t, ok, "a rejected SetLLM call must persist nothing")
}

// TestSetLLM_EditOnlyChangesNamedFields proves edit semantics: a field the
// caller did not name keeps its stored value, mirroring SetAgent's contract
// (`agent edit dev --runtime container` must not wipe dev's engine).
func TestSetLLM_EditOnlyChangesNamedFields(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	_, err := SetLLM(mgr, SetLLMRequest{Label: "big", Type: ptr("codex"), Model: ptr("o1")})
	require.NoError(t, err)

	entry, err := SetLLM(mgr, SetLLMRequest{Label: "big", Permissions: ptr("plan")})
	require.NoError(t, err)
	assert.Equal(t, "codex", entry.Type, "an unnamed field must survive an edit that names a different one")
	assert.Equal(t, "o1", entry.Model)
	assert.Equal(t, "plan", entry.Permissions)
}

// TestSetLLM_EnvReplacesWholeBlock proves Env is a full-replace, not a
// per-key merge: cli/llm_write.go's --env-file always supplies the entry's
// complete desired env set, so SetLLM must not silently keep a key the
// caller's file no longer lists.
func TestSetLLM_EnvReplacesWholeBlock(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	_, err := SetLLM(mgr, SetLLMRequest{
		Label: "big",
		Type:  ptr("codex"),
		Env:   map[string]string{"A": "1", "B": "2"},
	})
	require.NoError(t, err)

	entry, err := SetLLM(mgr, SetLLMRequest{Label: "big", Env: map[string]string{"C": "3"}})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"C"}, entry.EnvKeys, "a fresh --env-file must REPLACE the old set, not merge into it")
}

// TestSetLLM_EnvKeysNeverCarryValues is the credential-withholding contract at
// its source: LLMEntry.EnvKeys reports which keys are declared, never their
// values, no matter what secret was actually stored. This is what
// cli.renderLLMWritten (and `llm list`) render from, so a leak anywhere
// downstream is structurally impossible if this holds.
func TestSetLLM_EnvKeysNeverCarryValues(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	const secret = "sk-TOTALLY-SECRET-abc123"
	entry, err := SetLLM(mgr, SetLLMRequest{
		Label: "big",
		Type:  ptr("codex"),
		Env:   map[string]string{"OPENAI_API_KEY": secret},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"OPENAI_API_KEY"}, entry.EnvKeys)
	for _, k := range entry.EnvKeys {
		assert.NotEqual(t, secret, k)
	}
	// LabelEnv is the ONE sanctioned path back to the real value (used to
	// actually launch the engine), and it is not what EnvEntry reports.
	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, secret, reloaded.LabelEnv("big")["OPENAI_API_KEY"], "the real value must still be recorded for launch to use")
}

// TestRemoveLLM_DeletesAndPersists proves the removal round-trips.
func TestRemoveLLM_DeletesAndPersists(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	_, err := SetLLM(mgr, SetLLMRequest{Label: "big", Type: ptr("codex")})
	require.NoError(t, err)

	cfg, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	require.NoError(t, RemoveLLM(mgr, cfg, "big"))

	reloaded, err := config.Load(config.WithAppDir(appDir))
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
	mgr := managerFor(appDir)

	err := RemoveLLM(mgr, cfg, "claude-code")
	require.Error(t, err)
}

// TestRemoveLLM_UserDeclaredOverrideOfADefaultName_Succeeds is the positive
// mirror: a label sharing a shipped default's NAME but with an explicit
// user override IS removable — IsLLMUserAuthored must not blanket-refuse
// every default-shaped name, only the ones the user never actually wrote.
func TestRemoveLLM_UserDeclaredOverrideOfADefaultName_Succeeds(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\nllm:\n  configs:\n    claude-code: { permissions: bypass }\n")
	mgr := managerFor(appDir)
	cfg, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)

	require.NoError(t, RemoveLLM(mgr, cfg, "claude-code"))

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	assert.False(t, reloaded.IsLLMUserAuthored("claude-code"))
}
