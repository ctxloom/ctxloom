package isolation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNone_IsHostIdentical pins the None policy to today's host behaviour: the
// workspace IS the project directory, cleanup is a noop, and approvals stay
// Prompt. This is the behaviour-identity contract Step 0.2 must preserve.
func TestNone_IsHostIdentical(t *testing.T) {
	p := None{}
	assert.Equal(t, "none", p.Name())
	assert.Equal(t, ApprovalsPrompt, p.Approvals(), "none keeps the engine's in-tool approval prompt")

	ws, err := p.PrepareWorkspace(context.Background(), "/project/root", "member-a")
	require.NoError(t, err, "None never fails to prepare a workspace")
	assert.Equal(t, "/project/root", ws.Dir(), "none workspace is the live project directory")
	assert.NoError(t, ws.Cleanup(), "none cleanup is a noop")
	// Cleanup is idempotent — safe to call more than once.
	assert.NoError(t, ws.Cleanup())
}

// TestFactoryForWorkspace_BindsPolicy proves the bridge yields a usable
// pb.ClientFactory (the seam the fan-out injects). The None factory's spawn body
// is verbatim pb.NewSelfInvokingClientForLabel — the same call
// pb.DefaultClientFactory makes — so a live spawn is left to the operations /
// conformance suites; here we assert the bridge wires a non-nil factory.
func TestFactoryForWorkspace_BindsPolicy(t *testing.T) {
	ws, err := None{}.PrepareWorkspace(context.Background(), "/project/root", "m")
	require.NoError(t, err)
	factory := FactoryForWorkspace(None{}, ws)
	require.NotNil(t, factory, "the bridge must produce a client factory")
}

// TestResolve_DefaultsAndDegrades: empty/"none"/"host" axes resolve to None;
// unknown axis values degrade to that axis's default (fault tolerance) rather
// than failing.
func TestResolve_DefaultsAndDegrades(t *testing.T) {
	for _, axes := range []Axes{
		{},
		{Workspace: WorkspaceShared, Runtime: RuntimeHost},
	} {
		p := Resolve(axes, "claude-code", ImageConfig{})
		assert.IsType(t, None{}, p, "axes %+v resolve to None", axes)
	}
	// Unknown axis values → degrade to the axis defaults (warn; never block).
	assert.IsType(t, None{}, Resolve(Axes{Workspace: "podracer", Runtime: "hyperdrive"}, "claude-code", ImageConfig{}),
		"unknown axis values degrade to None")
	// Independence: an unknown RUNTIME never drops a requested worktree.
	assert.IsType(t, Worktree{}, Resolve(Axes{Workspace: WorkspaceWorktree, Runtime: "hyperdrive"}, "claude-code", ImageConfig{}),
		"an unknown runtime axis degrades alone; the workspace axis survives")
}

// TestApprovals_String renders the approvals axis for diagnostics.
func TestApprovals_String(t *testing.T) {
	assert.Equal(t, "prompt", ApprovalsPrompt.String())
	assert.Equal(t, "bypass", ApprovalsBypass.String())
}
