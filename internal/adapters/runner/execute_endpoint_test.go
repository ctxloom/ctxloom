package runner_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
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
// that URL with the bearer, rendered through the engine's dynamic approach
// from ctxloom's session-endpoint declaration — nothing executable is
// written, so the engine launches nothing for it.
func TestExecute_BindsTheLaunchEndpoint_AndTheMCPConfigNamesIt(t *testing.T) {
	env := newDeliveryEnv(t)
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: env.mint(t, 1, "run-ep"),
		Agent:    "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
	})
	require.NoError(t, err)
	require.NotEmpty(t, l.MCP.URL, "the resolver minted the endpoint")
	require.NotEmpty(t, l.MCP.Credential)

	dyn := &recordingDynamic{}
	drive := &recordingDriver{}
	out, err := runner.Execute(context.Background(), runner.Deps{
		Kind: mock.New(mock.WithDynamic()), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
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
	assert.Empty(t, entry.Command, "nothing executable: the engine dials the runner directly")
	assert.NotContains(t, string(body), "served_by", "the declaration is rendered, never written as declared")

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
		Identity: env.mint(t, 1, "run-busy"),
		Agent:    "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
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

// TestExecute_AHumanApprovedRunIsWiredForTheApprovalRoute: a structured run
// whose approver is the human is delivered the approval hooks, and its
// engine is started with the address they post to — the session endpoint's
// origin at the hook path — under the endpoint's own bearer. A run whose
// approver is not the human gets neither.
func TestExecute_AHumanApprovedRunIsWiredForTheApprovalRoute(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approver engine.Approver
		routed   bool
	}{
		{"human", engine.ApproverHuman, true},
		{"none", engine.ApproverNone, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newDeliveryEnv(t)
			l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
				Identity: env.mint(t, 1, "run-approval-"+tc.name),
				Agent:    "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
			})
			require.NoError(t, err)
			l.Permission.Approver = tc.approver
			require.Equal(t, tc.routed, l.RoutesApprovals())

			drive := &recordingDriver{}
			_, err = runner.Execute(context.Background(), runner.Deps{
				Kind: mock.New(mock.WithDynamic()), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
				Static: staticWriter(t), Records: records(t), Driver: drive, Dynamic: &recordingDynamic{},
			}, l)
			require.NoError(t, err)
			require.Len(t, drive.turns, 1)
			turn := drive.turns[0]

			hook, err := sessions.DecodeHookReach(func(k string) string { return turn.Exec.Env[k] })
			if !tc.routed {
				require.ErrorIs(t, err, sessions.ErrNoHookReach, "no route, no hook address")
				return
			}
			require.NoError(t, err)
			mcpURL, err := url.Parse(l.MCP.URL)
			require.NoError(t, err)
			assert.Equal(t, "http://"+mcpURL.Host+runner.HookPath, hook.URL, "the hook posts to the session endpoint's own listener")
			assert.Equal(t, l.MCP.Credential, hook.Credential, "under the endpoint's own bearer")

			var hooksFile string
			for i, a := range turn.Exec.Args {
				if a == mock.HooksFlag && i+1 < len(turn.Exec.Args) {
					hooksFile = turn.Exec.Args[i+1]
				}
			}
			require.NotEmpty(t, hooksFile, "the hooks were delivered")
			delivered, err := mock.DeliveredHooksFile(hooksFile)
			require.NoError(t, err)
			assert.Equal(t, agent.ApprovalHooks(l.Permission.ApprovalTimeout).PermissionAsk, delivered.PermissionAsk)
		})
	}
}
