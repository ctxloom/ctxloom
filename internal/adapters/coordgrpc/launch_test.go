package coordgrpc_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// TestEncodeLaunch_PopulatesEveryFieldTheLiteralsPopulated: the six run-start
// literals this codec replaces populated, between them, exactly these fields
// of today's message; a Launch encodes to all of them from its own typed
// values and nothing is re-derived.
func TestEncodeLaunch_PopulatesEveryFieldTheLiteralsPopulated(t *testing.T) {
	managed := &agent.ManagedConfig{ManageStatusline: true, DenyTools: []string{"Task"}}
	l := launch.Launch{
		Identity:   sessions.Identity{Harp: "harp-1", Project: "proj-1"},
		Engine:     "fixture",
		Label:      engine.LabelConfig{Label: "primary", Model: "fast-model"},
		Mode:       engine.Structured,
		Permission: engine.PermissionPlan,
		Cell: launch.Cell{
			Paths:     present.OnHost(present.Paths{ProjectRoot: present.Root{Host: "/proj"}}),
			Workspace: "/proj/.worktrees/harp-1",
			Env:       map[string]string{"WS_VAR": "ws"},
		},
		Package: launch.Package{Context: "the assembled context", Managed: managed},
		MCP:     sessions.Endpoint{URL: "http://127.0.0.1:41234/mcp", Credential: "bearer"},
		Prompt:  "do the thing",
		Env:     map[string]string{"USER_VAR": "u"},
	}
	req := coordgrpc.EncodeLaunch(l, 2)

	require.Len(t, req.Fragments, 1, "the assembled context rides as the one lead fragment")
	require.Equal(t, "the assembled context", req.Fragments[0].Content)
	require.Equal(t, "do the thing", req.GetPrompt().GetContent())
	require.NotNil(t, req.ManagedConfig, "the managed surfaces ride the message: a one-shot is a real session")
	require.True(t, req.ManagedConfig.GetManageStatusline())

	o := req.GetOptions()
	require.Equal(t, "/proj/.worktrees/harp-1", o.GetWorkDir(), "the engine's cwd is the CELL's workspace")
	require.Equal(t, engine.PermissionPlan.String(), o.GetPermissionMode())
	require.Equal(t, pb.ExecutionMode_ONESHOT, o.GetMode())
	require.Equal(t, "fast-model", o.GetModel())
	require.Equal(t, agent.WireVerbosity(2), o.GetVerbosity())
	require.Equal(t, pb.CellKindToProto(agent.CellKindDirectoryIsolated), o.GetCellKind(), "a workspace apart from the project root is a directory-isolated cell")
	require.Equal(t, pb.LaunchFormToProto(agent.LaunchFormDeliver), o.GetLaunchForm(), "every launch owns a harp and delivers its own surfaces")

	env := o.GetEnv()
	require.Equal(t, "harp-1", env[sessions.EnvHarp], "the identity carriers are stamped from the Launch")
	require.Equal(t, "proj-1", env[sessions.EnvProjectID])
	require.Equal(t, "ws", env["WS_VAR"], "the cell's env rides")
	require.Equal(t, "u", env["USER_VAR"], "the caller's passthrough rides")
}

// TestEncodeLaunch_CellKind_FollowsTheCell: the wire's cell kind is a
// projection of the cell, decided nowhere else.
func TestEncodeLaunch_CellKind_FollowsTheCell(t *testing.T) {
	shared := launch.Launch{Cell: launch.Cell{Paths: present.OnHost(present.Paths{ProjectRoot: present.Root{Host: "/proj"}}), Workspace: "/proj"}}
	require.Equal(t, pb.CellKindToProto(agent.CellKindShared), coordgrpc.EncodeLaunch(shared, 0).GetOptions().GetCellKind())

	boxed := shared
	boxed.Cell.Container = &launch.ContainerCell{Runtime: launch.RuntimeRootless}
	require.Equal(t, pb.CellKindToProto(agent.CellKindProcessIsolated), coordgrpc.EncodeLaunch(boxed, 0).GetOptions().GetCellKind())
}
