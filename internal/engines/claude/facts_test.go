package claude

import (
	"path"
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

// claude declares its auth capability with every mode the shared vocabulary
// has: the human's login, a minted token, and a pay-per-use key.
func TestHome_DeclaresAuthWithEveryMode(t *testing.T) {
	a, ok := claudeKind(t).Home().Auth.Get()
	require.True(t, ok)
	assert.Equal(t, []engine.AuthMode{engine.AuthLogin, engine.AuthToken, engine.AuthAPIKey, engine.AuthCloud}, a.Modes())
}

// The container declaration is how the image is built and what it overlays;
// how a container run authenticates is the run's Credentials, not this.
func TestContainer_Declaration(t *testing.T) {
	c, err := claudeKind(t).Container()
	require.NoError(t, err)
	require.NoError(t, c.Validate())
	assert.NotEmpty(t, c.Install, "claude has an official npm installer")
	assert.Equal(t, "claude --version", c.ValidateCommand)
	assert.Equal(t, []string{ConfigDirName}, c.OverlayDirs)
	assert.Equal(t, path.Join(ConfigDirName, TranscriptsDirName), c.TranscriptStoreRel)

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
