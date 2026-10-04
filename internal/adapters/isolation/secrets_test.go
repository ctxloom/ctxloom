package isolation

import (
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// fixtureSecret is a credential VALUE no wire byte may carry. Distinctive so
// a substring search cannot match anything else in a frame.
const fixtureSecret = "sk-ant-oat01-fixture-NEVER-ON-THE-WIRE-7f3a"

// startRunBytes is a container cell's placement as the runner receives it:
// the whole StartRun launch, encoded and marshalled as it crosses the link.
func startRunBytes(t *testing.T, pl launch.Placement) []byte {
	t.Helper()
	l := launchtest.FullLaunch(t)
	l.Cell = launch.Cell{Placement: pl}
	raw, err := proto.Marshal(coordgrpc.EncodeLaunch(l))
	require.NoError(t, err)
	return raw
}

// The ruling (local-secrets risk 1): a container runner may dial home over a
// LAN-visible cleartext listener, so the human's engine credential never
// rides StartRun. Only the variable's name and the engine-side file holding
// it do. Asserted on the marshalled bytes, through the production relocator
// and the production codec.
func TestContainerCell_StartRunCarriesTheCredentialByReferenceNeverByValue(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	creds := engine.Credentials{
		Env:   map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": fixtureSecret},
		Unset: []string{"ANTHROPIC_API_KEY"},
	}
	pl, _, err := c.relocator().relocate(layout{cwd: "/proj", creds: creds})
	require.NoError(t, err)

	wire := startRunBytes(t, pl)
	assert.NotContains(t, string(wire), fixtureSecret, "the credential value crossed the coordinator link")
	want := path.Join(secretsTarget, secretsFileName)
	assert.Equal(t, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": want}, pl.SecretFiles)
	assert.NotContains(t, pl.Env, "CLAUDE_CODE_OAUTH_TOKEN", "a secret variable is never ALSO in env")
	assert.Contains(t, string(wire), want, "the reference rides in its place")
	assert.Equal(t, []string{"ANTHROPIC_API_KEY"}, pl.Unset, "what the engine must not inherit is untouched")
}

// The host runtime is out of the mount's scope: its runner is a same-uid
// process the launching process spawned, dialling the loopback-only
// listener, so the value stays in env and no file is named.
func TestHostCell_KeepsTheCredentialInEnv(t *testing.T) {
	creds := engine.Credentials{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": fixtureSecret}}
	pl, _, err := hostRelocator{}.relocate(layout{cwd: "/proj", creds: creds})
	require.NoError(t, err)
	assert.Equal(t, fixtureSecret, pl.Env["CLAUDE_CODE_OAUTH_TOKEN"])
	assert.Empty(t, pl.SecretFiles)
}
