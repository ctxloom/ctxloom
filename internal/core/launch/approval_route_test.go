package launch_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// resolved resolves src over env and returns the launch with the hooks its
// carried package delivers.
func resolved(t *testing.T, env launchtest.Env, src launch.Source) (launch.Launch, wire.UnifiedHooks) {
	t.Helper()
	src.Identity, src.WorkDir = env.Identity, env.Project
	l, err := launch.Resolve(context.Background(), env.Deps, src)
	require.NoError(t, err)
	t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
	pkg, err := launch.Open(context.Background(), env.Deps, l)
	require.NoError(t, err)
	return l, pkg.Hooks.Unified
}

// approvalHooksOf is the approval hooks of env's engine (the codec-declaring
// fixture when env's own declares none) for timeout.
func approvalHooksOf(t *testing.T, env launchtest.Env, l launch.Launch, timeout time.Duration) wire.UnifiedHooks {
	t.Helper()
	eng, ok := env.Deps.Engines.Lookup(l.Engine)
	require.True(t, ok)
	codec, ok := eng.Approvals().Get()
	require.True(t, ok, "the engine declares an approval codec")
	return codec.Hooks(timeout)
}

// TestResolve_AHumanApprovedRunCarriesTheApprovalHooks: a structured run
// whose approver is the human, on an engine with an approval codec, is
// delivered the approval hooks for its own approval timeout — the route by
// which what its rules leave open reaches the human.
func TestResolve_AHumanApprovedRunCarriesTheApprovalHooks(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev", launchtest.PermissionBlock(agents.Permissions{
		NeutralPermissions: agents.NeutralPermissions{Approver: "human", ApprovalTimeout: "20m"},
	})))
	l, hooks := resolved(t, env, launch.Source{Agent: "dev", Mode: engine.Structured})
	want := approvalHooksOf(t, env, l, 20*time.Minute)

	assert.True(t, l.RoutesApprovals())
	assert.Equal(t, want.PermissionAsk, hooks.PermissionAsk)
	assert.Subset(t, hooks.PreTool, want.PreTool)
}

// TestResolve_NoApprovalHooksWhereNobodyIsAsked: approver none denies what
// is left open, an interactive run's human answers in the engine's own UI,
// and an engine with no codec cannot put a request to anyone (its launch
// resolves only with approver none) — none of them carries the approval
// hooks.
func TestResolve_NoApprovalHooksWhereNobodyIsAsked(t *testing.T) {
	for name, tc := range map[string]struct {
		opts []launchtest.Option
		mode engine.Mode
	}{
		"approver none": {opts: []launchtest.Option{launchtest.WithAgent("dev", launchtest.PermissionBlock(agents.Permissions{
			NeutralPermissions: agents.NeutralPermissions{Approver: "none"},
		}))}, mode: engine.Structured},
		"interactive": {opts: []launchtest.Option{launchtest.WithAgent("dev")}, mode: engine.Interactive},
		"no codec": {opts: []launchtest.Option{launchtest.WithAgent("dev", launchtest.PermissionBlock(agents.Permissions{
			NeutralPermissions: agents.NeutralPermissions{Approver: "none"},
		})), launchtest.NoApprovals()}, mode: engine.Structured},
	} {
		t.Run(name, func(t *testing.T) {
			env := launchtest.Deps(t, tc.opts...)
			l, hooks := resolved(t, env, launch.Source{Agent: "dev", Mode: tc.mode})
			assert.False(t, l.RoutesApprovals())
			assert.Empty(t, hooks.PermissionAsk)
			for _, h := range approvalHooksOf(t, launchtest.Deps(t), l, l.Permission.ApprovalTimeout).All() {
				assert.NotContains(t, hooks.All(), h)
			}
		})
	}
}
