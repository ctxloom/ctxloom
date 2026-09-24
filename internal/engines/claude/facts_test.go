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
// directory the session home lands in IS the directory CLAUDE_CONFIG_DIR
// names.
func TestHome_IsBuiltFromClaudesOwnConstants(t *testing.T) {
	home := claudeKind(t).Home()
	require.NoError(t, home.Validate())
	require.Len(t, home.Vars, 1)
	assert.Equal(t, ConfigDirEnv, home.Vars[0].Name)
	assert.Equal(t, HomeLeaf, home.Vars[0].Subdir)
	assert.NotNil(t, home.InstanceConfig, "claude generates its own instance config into a provisioned home")
}

// claude authenticates from the one long-lived token `claude setup-token`
// mints, or from an API key, a gateway token or a cloud provider instead.
func TestHome_DeclaresTokenAuth(t *testing.T) {
	a, ok := claudeKind(t).Home().Auth.Get()
	require.True(t, ok)
	assert.Equal(t, engine.TokenAuth{
		TokenVar:    "CLAUDE_CODE_OAUTH_TOKEN",
		EnvTriggers: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"},
		MintHint:    "claude setup-token",
	}, a)
}

// A host run shares the human's login through the credential-storage var,
// falling back to the config dir as claude itself does; the probe
// TestClaudeSecureStorage_FollowsTheVar pins that the installed claude honours it.
func TestHome_DeclaresSharedLogin(t *testing.T) {
	l, ok := claudeKind(t).Home().SharedLogin.Get()
	require.True(t, ok)
	assert.Equal(t, engine.SharedLogin{Var: "CLAUDE_SECURESTORAGE_CONFIG_DIR", FallbackVar: "CLAUDE_CONFIG_DIR"}, l)
}

// A container authenticates from the env alone: no credential file is ever
// mounted into it, and the refusal names how to mint and store a token.
func TestContainer_AuthIsEnvOnly(t *testing.T) {
	c, err := claudeKind(t).Container()
	require.NoError(t, err)
	require.NoError(t, c.Validate())
	assert.NotEmpty(t, c.Install, "claude has an official npm installer")
	assert.Equal(t, "claude --version", c.ValidateCommand)
	assert.Equal(t, []string{ConfigDirName}, c.OverlayDirs)
	assert.Equal(t, filepath.Join(ConfigDirName, TranscriptsDirName), c.TranscriptStoreRel)

	auth, ok := c.Auth.Get()
	require.True(t, ok)
	assert.Equal(t, []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}, auth.EnvTriggers,
		"the setup-token var authenticates a container on its own, so it is a trigger")
	assert.Contains(t, auth.EnvPassthrough, "CLAUDE_CODE_OAUTH_TOKEN", "a trigger that does not cross leaves the container logged out")
	assert.Contains(t, auth.EnvPassthrough, "ANTHROPIC_BASE_URL")
	assert.Contains(t, auth.Hint, "claude setup-token")
	assert.Contains(t, auth.Hint, "ctxloom auth set-token")
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
