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
	assert.Equal(t, []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}, seed.EnvTriggers)
	assert.Equal(t, "claude login", seed.LoginHint)
	require.Len(t, seed.Files, 1, "only .credentials.json crosses — never .claude.json (the user's whole config)")
	assert.Equal(t, filepath.ToSlash(filepath.Join(ConfigDirName, CredentialsFileName)), seed.Files[0].HostRelHome)
	assert.Equal(t, CredentialsFileName, seed.Files[0].DestName)
	assert.True(t, seed.Files[0].Required)
	assert.NotNil(t, home.InstanceConfig, "claude generates its own instance config into a provisioned home")
}

// The seed is a PROJECTION (the refresh half withheld), and a projection can
// only be delivered by a mechanism that copies: replication re-projects the
// host file on every change; a mount shares by identity and would hand the
// instance the very field the seed withholds. So replication is the one
// accepted delivery, and the projection is declared on the file itself.
func TestHome_AcceptsReplicationOnly_BecauseTheSeedIsAProjection(t *testing.T) {
	seed, ok := claudeKind(t).Home().Credentials.Get()
	require.True(t, ok)
	assert.Equal(t, []engine.MaterialDelivery{engine.MaterialDeliveryReplicated}, seed.Accept)
	require.NotNil(t, seed.Files[0].Project, "the credential file declares its projection")
	got, err := seed.Files[0].Project([]byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","refreshTokenExpiresAt":1,"expiresAt":2,"scopes":["s"]},"other":true}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"claudeAiOauth":{"accessToken":"a","expiresAt":2,"scopes":["s"]},"other":true}`, string(got))
}

// An API-key credential carries no OAuth object: the projection has nothing
// to withhold and passes the object through. Anything that is not a JSON
// object is refused rather than seeded uninspected.
func TestProjectCredential_NoOAuthObjectPassesThrough_NonObjectRefused(t *testing.T) {
	got, err := projectCredential([]byte(`{"apiKey":"k"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"apiKey":"k"}`, string(got))
	_, err = projectCredential([]byte(`not json`))
	assert.Error(t, err)
	_, err = projectCredential([]byte(`{"claudeAiOauth":"a string"}`))
	assert.Error(t, err)
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

// On macOS the store is the Keychain: the seed declares the default item's
// service and the same projection the file arm applies.
func TestHome_DeclaresTheMacOSKeychainStore(t *testing.T) {
	seed, ok := claudeKind(t).Home().Credentials.Get()
	require.True(t, ok)
	require.NotNil(t, seed.Keychain)
	assert.Equal(t, "Claude Code-credentials", seed.Keychain.Service)
	require.NotNil(t, seed.Keychain.Project)
	got, err := seed.Keychain.Project([]byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r"}}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"claudeAiOauth":{"accessToken":"a"}}`, string(got))
}
