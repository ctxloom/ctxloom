package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
)

func parseAgentWriteFlags(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	agentSetEnvHost, agentSetEnv = true, nil
	cmd := &cobra.Command{}
	registerAgentWriteFlags(cmd)
	require.NoError(t, cmd.Flags().Parse(args))
	return cmd
}

// --env-host and the repeatable --env reach the request as typed; untyped
// they are not sent, so an edit keeps what is recorded; --env "" sends an
// empty list, which clears the names.
func TestBuildSetAgentRequest_EnvHostAndEnv(t *testing.T) {
	req := buildSetAgentRequest(parseAgentWriteFlags(t, "--env-host=false", "--env", "GITHUB_TOKEN", "--env", "NPM_TOKEN"), "dev")
	require.NotNil(t, req.EnvHost)
	assert.False(t, *req.EnvHost)
	require.NotNil(t, req.Env)
	assert.Equal(t, []string{"GITHUB_TOKEN", "NPM_TOKEN"}, *req.Env)

	req = buildSetAgentRequest(parseAgentWriteFlags(t, "--env-host"), "dev")
	require.NotNil(t, req.EnvHost)
	assert.True(t, *req.EnvHost)
	assert.Nil(t, req.Env)

	req = buildSetAgentRequest(parseAgentWriteFlags(t, "--llm", "x"), "dev")
	assert.Nil(t, req.EnvHost)
	assert.Nil(t, req.Env)

	req = buildSetAgentRequest(parseAgentWriteFlags(t, "--env", ""), "dev")
	require.NotNil(t, req.Env)
	assert.Empty(t, *req.Env)
}

// agent show renders the declared env_host and env; undeclared, neither
// line appears.
func TestRenderAgentDeclaration_ShowsEnvHostAndEnv(t *testing.T) {
	off := false
	var buf bytes.Buffer
	w := errwriter.New(&buf)
	renderAgentDeclaration(w, &operations.AgentEntry{Name: "dev", EnvHost: &off, Env: []string{"GITHUB_TOKEN"}})
	require.NoError(t, w.Err())
	assert.Contains(t, buf.String(), "Env host: false\n")
	assert.Contains(t, buf.String(), "Env:\n  - GITHUB_TOKEN\n")

	var bare bytes.Buffer
	w = errwriter.New(&bare)
	renderAgentDeclaration(w, &operations.AgentEntry{Name: "dev"})
	require.NoError(t, w.Err())
	assert.NotContains(t, bare.String(), "Env")
}

// The create/edit confirmation names them too.
func TestRenderAgentWritten_NamesEnvHostAndEnv(t *testing.T) {
	off := false
	var buf bytes.Buffer
	require.NoError(t, renderAgentWritten(&buf, &operations.AgentEntry{Name: "dev", EnvHost: &off, Env: []string{"A", "B"}}, false))
	assert.Contains(t, buf.String(), "env_host: false")
	assert.Contains(t, buf.String(), "env: A, B")

	var bare bytes.Buffer
	require.NoError(t, renderAgentWritten(&bare, &operations.AgentEntry{Name: "dev"}, false))
	assert.NotContains(t, bare.String(), "env")
}
