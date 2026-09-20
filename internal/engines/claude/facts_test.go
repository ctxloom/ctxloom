package claude

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func claudeKind(t *testing.T) engine.Engine {
	t.Helper()
	kind, err := Build()
	require.NoError(t, err)
	return kind
}

// The home declaration is built from the engine's own constants, so the
// directory the seed lands in IS the directory CLAUDE_CONFIG_DIR names.
func TestHome_IsBuiltFromClaudesOwnConstants(t *testing.T) {
	home := claudeKind(t).Home()
	require.NoError(t, home.Validate())
	require.Len(t, home.Vars, 1)
	assert.Equal(t, ConfigDirEnv, home.Vars[0].Name)
	assert.Equal(t, HomeLeaf, home.Vars[0].Subdir)

	seed, ok := home.Credentials.Get()
	require.True(t, ok, "claude relocates credentials with its home var")
	assert.Equal(t, HomeLeaf, seed.Subdir)
	assert.Equal(t, "ANTHROPIC_API_KEY", seed.EnvTrigger)
	assert.Equal(t, "claude login", seed.LoginHint)
	require.Len(t, seed.Files, 1, "only .credentials.json crosses — never .claude.json (the user's whole config)")
	assert.Equal(t, filepath.ToSlash(filepath.Join(ConfigDirName, CredentialsFileName)), seed.Files[0].HostRelHome)
	assert.Equal(t, CredentialsFileName, seed.Files[0].DestName)
	assert.True(t, seed.Files[0].Required)
	assert.NotNil(t, home.InstanceConfig, "claude generates its own instance config into a provisioned home")
}

// Claude's refresh token is single-use and rotating, so the ORDER is the
// declaration: a mount has one inode and one refresh path, replication has a
// window in which an instance can present a token another already spent.
// Asserting the order, not the set, is what keeps a later edit from quietly
// preferring the mechanism with the failure mode. A stripped copy is refused
// at every position: it works until the access token expires and then that
// instance is stuck with no way back.
func TestHome_AcceptsMountedBeforeReplicated_AndNothingThatCannotRenew(t *testing.T) {
	seed, ok := claudeKind(t).Home().Credentials.Get()
	require.True(t, ok)
	assert.Equal(t, []engine.MaterialDelivery{engine.MaterialDeliveryMounted, engine.MaterialDeliveryReplicated}, seed.Accept)
	for _, d := range seed.Accept {
		assert.NotEqual(t, engine.MaterialDeliveryAbsent, d)
		assert.True(t, d.Decided(), "every accepted delivery must be a decided one")
	}
}

func TestContainer_AuthPrefersEnvAndMountsTheRealCredentialReadWrite(t *testing.T) {
	c, err := claudeKind(t).Container()
	require.NoError(t, err)
	require.NoError(t, c.Validate())
	assert.NotEmpty(t, c.Install, "claude has an official npm installer")
	assert.Equal(t, "claude --version", c.ValidateCommand)
	assert.Equal(t, []string{ConfigDirName}, c.OverlayDirs)
	assert.Equal(t, filepath.Join(ConfigDirName, TranscriptsDirName), c.TranscriptStoreRel)

	auth, ok := c.Auth.Get()
	require.True(t, ok)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}, auth.EnvTriggers)
	assert.Contains(t, auth.EnvPassthrough, "ANTHROPIC_BASE_URL")
	require.Len(t, auth.CredentialFiles, 1)
	assert.False(t, auth.CredentialFiles[0].ReadOnly, "claude's token refresh must write back into the one real file")
	assert.Equal(t, auth.CredentialFiles[0].HostRelHome, auth.CredentialFiles[0].ContainerRelHome)
	assert.NotEmpty(t, auth.Hint)
}

// Hooks decodes claude's native payload: the unified event for the native
// name (the payload's own, else the registration's), the native session and
// the transcript path.
func TestHooks_DecodesTheNativePayload(t *testing.T) {
	codec := claudeKind(t).Hooks()
	ev, err := codec.Decode("Stop", []byte(`{"session_id":"s1","hook_event_name":"Stop","transcript_path":"/t/s1.jsonl"}`))
	require.NoError(t, err)
	assert.Equal(t, engine.HookEvent{Event: "turn_end", NativeSession: "s1", Transcript: "/t/s1.jsonl"}, ev)
	ev, err = codec.Decode("SessionStart", []byte(`{"session_id":"s2"}`))
	require.NoError(t, err)
	assert.Equal(t, "session_start", ev.Event, "the registration's event names it when the payload carries none")
	_, err = codec.Decode("Stop", []byte(`not json`))
	require.Error(t, err)
}
