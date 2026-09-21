package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// recordingDynamic is the Dynamic port double: it records the loadout it was
// asked to serve and reports whether it was closed.
type recordingDynamic struct {
	served []delivery.Loadout
	closed int
}

func (d *recordingDynamic) Serve(_ context.Context, lo delivery.Loadout, policy delivery.ServePolicy) (delivery.Served, error) {
	if len(policy.AllowedOrigins) == 0 {
		return delivery.Served{}, delivery.ErrNoAllowedOrigins
	}
	d.served = append(d.served, lo)
	return delivery.Served{Close: func() error { d.closed++; return nil }}, nil
}

// TestExecute_BindsTheLaunchEndpoint_AndTheMCPConfigNamesIt: the runner
// hands the Dynamic port the SAME endpoint the launch carries (it binds,
// never mints), and the session's .mcp.json names ctxloom's own server as
// that URL with the bearer — no `ctxloom mcp serve` command is written, so
// the engine spawns no shim.
func TestExecute_BindsTheLaunchEndpoint_AndTheMCPConfigNamesIt(t *testing.T) {
	env := newDeliveryEnv(t)
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity:     env.mint(t, 1, "run-ep"),
		Orchestrator: "root-harp",
		Agent:        "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
	})
	require.NoError(t, err)
	require.NotEmpty(t, l.MCP.URL, "the resolver minted the endpoint")
	require.NotEmpty(t, l.MCP.Credential)

	dyn := &recordingDynamic{}
	drive := &recordingDriver{}
	out, err := runner.Execute(context.Background(), runner.Deps{
		Kind: mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive, Dynamic: dyn,
	}, l)
	require.NoError(t, err)

	require.Len(t, dyn.served, 1, "the endpoint is served once, before the engine is driven")
	assert.Equal(t, l.MCP, dyn.served[0].MCP, "the runner binds the endpoint the Launch carries")
	assert.Equal(t, l.Identity, dyn.served[0].Identity)
	assert.Equal(t, l.Cell.Workspace, dyn.served[0].WorkDir)
	assert.Equal(t, "MARKER-7f3a", lastWord(dyn.served[0].Package.Context.Text), "the loadout is the decoded package")

	require.Len(t, drive.turns, 1)
	turn := drive.turns[0]
	var ctx agent.ChatMCPServer
	for _, s := range turn.MCPServers {
		if s.Name == agent.MCPServerName {
			ctx = s
		}
	}
	require.Equal(t, agent.MCPTransportHTTP, ctx.Transport, "ctxloom's own server is the bound endpoint, not a stdio command")
	assert.Equal(t, l.MCP.URL, ctx.URL)
	assert.Equal(t, "Bearer "+l.MCP.Credential, ctx.Headers["Authorization"])
	assert.Empty(t, ctx.Command)

	mcpConfig := out.MCPConfig
	require.NotEmpty(t, mcpConfig, "the MCP file was delivered")
	body, err := os.ReadFile(mcpConfig)
	require.NoError(t, err)
	var doc struct {
		MCPServers map[string]wire.MCPServer `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(body, &doc))
	entry := doc.MCPServers[agent.MCPServerName]
	assert.Equal(t, l.MCP.URL, entry.URL)
	assert.Equal(t, "Bearer "+l.MCP.Credential, entry.Headers["Authorization"])
	assert.Empty(t, entry.Command, "no shim command: the engine dials the runner directly")
	assert.NotContains(t, string(body), "mcp serve")

	info, err := os.Stat(mcpConfig)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the bearer is in this file; nobody else reads it")

	require.NotNil(t, out.Close)
	out.Close()
	assert.Equal(t, 1, dyn.closed, "Outcome.Close tears the served endpoint down")
}

// TestExecute_EndpointUnavailable_IsReturnedTyped: the one refusal the
// coordinator answers with a rebind must reach it as the delivery sentinel,
// unwrapped by nothing.
func TestExecute_EndpointUnavailable_IsReturnedTyped(t *testing.T) {
	env := newDeliveryEnv(t)
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity:     env.mint(t, 1, "run-busy"),
		Orchestrator: "root-harp",
		Agent:        "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
	})
	require.NoError(t, err)
	drive := &recordingDriver{}
	_, err = runner.Execute(context.Background(), runner.Deps{
		Kind: mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive, Dynamic: busyDynamic{},
	}, l)
	require.ErrorIs(t, err, delivery.ErrEndpointUnavailable)
	assert.Empty(t, drive.turns, "nothing is driven over an endpoint that did not bind")
}

type busyDynamic struct{}

func (busyDynamic) Serve(context.Context, delivery.Loadout, delivery.ServePolicy) (delivery.Served, error) {
	return delivery.Served{}, delivery.ErrEndpointUnavailable
}

func lastWord(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' || s[i] == '\n' {
			return s[i+1:]
		}
	}
	return s
}
