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
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// resolvePolicy resolves env's agent (optionally over a label) and returns
// the launch's policy, or the refusal.
func resolvePolicy(t *testing.T, env launchtest.Env, src launch.Source) (engine.PermissionPolicy, error) {
	t.Helper()
	src.Identity, src.WorkDir = env.Identity, env.Project
	if src.Mode == 0 {
		src.Mode = engine.Structured
	}
	l, err := launch.Resolve(context.Background(), env.Deps, src)
	if err != nil {
		return engine.PermissionPolicy{}, err
	}
	t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
	return l.Permission, nil
}

// fixtureBlock is a binding block holding the fixture engine's document.
func fixtureBlock(n agents.NeutralPermissions, doc map[string]any) agents.Permissions {
	return agents.Permissions{NeutralPermissions: n, Engines: map[string]map[string]any{string(launchtest.EngineName): doc}}
}

func yes() *bool { b := true; return &b }

// Each neutral field is the first of the binding, the label and the
// project that declares it; the engine settles its own document from the
// binding's block over the label's keys.
func TestResolvePolicy_FieldByField(t *testing.T) {
	env := launchtest.Deps(t,
		launchtest.WithAgent("dev", launchtest.PermissionBlock(fixtureBlock(agents.NeutralPermissions{Approver: "none"}, map[string]any{"deny": []any{"Bash"}}))),
		launchtest.GuardedLabelPermissions(agents.LabelPermissions{
			NeutralPermissions: agents.NeutralPermissions{Approver: "human", Sandbox: "workspace-write"},
			Engine:             map[string]any{"mode": "acceptEdits", "deny": []any{"Edit"}},
		}),
		launchtest.ProjectPermissionBlock(agents.NeutralPermissions{ApprovalTimeout: "20m", Sandbox: "full", Network: yes()}),
	)
	p, err := resolvePolicy(t, env, launch.Source{Agent: "dev", Label: "guarded"})
	require.NoError(t, err)
	assert.Equal(t, engine.Posture{Engine: launchtest.EngineName, Document: map[string]any{"mode": "acceptEdits", "deny": []any{"Bash"}}, Label: "the acceptEdits posture"}, p.Posture,
		"the label's mode, the binding's deny: the engine takes each key from the nearest declaration, and names the posture")
	assert.Equal(t, engine.ApproverNone, p.Approver, "the binding's approver")
	assert.Equal(t, engine.SandboxWorkspaceWrite, p.Sandbox, "the label's sandbox beats the project's")
	assert.Equal(t, 20*time.Minute, p.ApprovalTimeout, "the project's timeout")
	assert.True(t, p.Network, "the project's network")
}

// Nothing declared: the engine's resolved default, the human, the default
// timeout, the engine's default sandbox, no network.
func TestResolvePolicy_Defaults(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"))
	p, err := resolvePolicy(t, env, launch.Source{Agent: "dev"})
	require.NoError(t, err)
	assert.Equal(t, engine.PermissionPolicy{
		Posture:         engine.Posture{Engine: launchtest.EngineName, Document: map[string]any{"mode": "default"}, Label: "the default posture"},
		Approver:        engine.ApproverHuman,
		ApprovalTimeout: engine.DefaultApprovalTimeout,
		Sandbox:         engine.SandboxFull,
	}, p)
}

// The --permissions flag is a mode in the engine's vocabulary, over every
// declaration; it moves nothing else.
func TestResolvePolicy_FlagSetsOnlyTheMode(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev", launchtest.PermissionBlock(fixtureBlock(agents.NeutralPermissions{Approver: "none"}, map[string]any{"mode": "plan", "deny": []any{"Bash"}}))))
	p, err := resolvePolicy(t, env, launch.Source{Agent: "dev", Permission: "bypass"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "bypass", "deny": []any{"Bash"}}, p.Posture.Document)
	assert.Equal(t, engine.ApproverNone, p.Approver)
}

// A binding carrying engine blocks, none of them for the engine it
// resolved to, declared nothing that engine can honour: it is refused,
// naming the blocks it has and the one it lacks — or, under --degraded,
// runs at the engine's floor, announced.
func TestResolvePolicy_MissingEngineBlock(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev", launchtest.PermissionBlock(agents.Permissions{Engines: map[string]map[string]any{"claude-code": {"mode": "bypass"}}})))
	_, err := resolvePolicy(t, env, launch.Source{Agent: "dev"})
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured)
	require.ErrorContains(t, err, "claude-code")
	require.ErrorContains(t, err, "permissions."+string(launchtest.EngineName))

	var got report.Findings
	env.Deps.Reporter = &got
	p, err := resolvePolicy(t, env, launch.Source{Agent: "dev", Degraded: true})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "plan"}, p.Posture.Document, "the floor, never the other engine's bypass")
	require.Len(t, got, 1, "the drop to the floor is announced")
	assert.Contains(t, got[0].Text, "claude-code")
}

// An engine with no permission model takes no declaration of its own: a
// block, label keys or the flag are refused; declaring none is fine.
func TestResolvePolicy_NoModelTakesNoDeclaration(t *testing.T) {
	for name, tc := range map[string]struct {
		opts []launchtest.Option
		src  launch.Source
	}{
		"a block":    {[]launchtest.Option{launchtest.WithAgent("dev", launchtest.Permissions("plan"))}, launch.Source{Agent: "dev"}},
		"another's":  {[]launchtest.Option{launchtest.WithAgent("dev", launchtest.PermissionBlock(agents.Permissions{Engines: map[string]map[string]any{"mock": {}}}))}, launch.Source{Agent: "dev"}},
		"label keys": {[]launchtest.Option{launchtest.WithAgent("dev")}, launch.Source{Agent: "dev", Label: "guarded"}},
		"the flag":   {[]launchtest.Option{launchtest.WithAgent("dev")}, launch.Source{Agent: "dev", Permission: "plan"}},
	} {
		t.Run(name, func(t *testing.T) {
			env := launchtest.Deps(t, append(tc.opts, launchtest.NoPermissionModel())...)
			_, err := resolvePolicy(t, env, tc.src)
			require.ErrorIs(t, err, launch.ErrPermissionUnhonoured)
			require.ErrorContains(t, err, "NoPermissionModel")
		})
	}
	env := launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.NoPermissionModel())
	p, err := resolvePolicy(t, env, launch.Source{Agent: "dev"})
	require.NoError(t, err)
	assert.Equal(t, engine.Posture{Engine: launchtest.EngineName}, p.Posture, "no document: the engine has none to resolve")
	assert.Equal(t, engine.SandboxFull, p.Sandbox)
}

// The sandbox must be one the engine can enforce where the run executes:
// the fixture enforces workspace-write on the host only. There is no
// degraded fallback — every fallback from a sandbox is a wider one.
func TestResolvePolicy_SandboxFailsClosed(t *testing.T) {
	block := launchtest.PermissionBlock(agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Sandbox: "workspace-write"}})
	env := launchtest.Deps(t,
		launchtest.RuntimesAvailable(launch.RuntimeHost, launch.RuntimeRootless),
		launchtest.WithAgent("host", launchtest.Runtime(launch.RuntimeHost), block),
		launchtest.WithAgent("boxed", launchtest.Runtime(launch.RuntimeRootless), block),
		launchtest.WithAgent("ro", launchtest.PermissionBlock(agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Sandbox: "read-only"}})),
	)
	p, err := resolvePolicy(t, env, launch.Source{Agent: "host"})
	require.NoError(t, err)
	assert.Equal(t, engine.SandboxWorkspaceWrite, p.Sandbox)
	for _, agent := range []string{"boxed", "ro"} {
		for _, degraded := range []bool{false, true} {
			_, err := resolvePolicy(t, env, launch.Source{Agent: agent, Degraded: degraded})
			require.ErrorIsf(t, err, launch.ErrPermissionUnhonoured, "%s degraded=%v", agent, degraded)
			require.ErrorContainsf(t, err, "cannot be enforced", "%s", agent)
		}
	}
}

// approver: reviewer needs an engine that has one.
func TestResolvePolicy_ReviewerNeedsTheEngines(t *testing.T) {
	block := launchtest.PermissionBlock(agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Approver: "reviewer"}})
	_, err := resolvePolicy(t, launchtest.Deps(t, launchtest.WithAgent("dev", block)), launch.Source{Agent: "dev"})
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured)
	require.ErrorContains(t, err, "no reviewer")

	p, err := resolvePolicy(t, launchtest.Deps(t, launchtest.WithAgent("dev", block), launchtest.FixtureReviewer()), launch.Source{Agent: "dev"})
	require.NoError(t, err)
	assert.Equal(t, engine.ApproverReviewer, p.Approver)
}

// Refusals name the value and the rung it came from.
func TestResolvePolicy_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		block agents.Permissions
		want  string
	}{
		"mode":     {fixtureBlock(agents.NeutralPermissions{}, map[string]any{"mode": "plann"}), "plann"},
		"approver": {agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Approver: "boss"}}, `approver "boss" from agent "dev"`},
		"timeout":  {agents.Permissions{NeutralPermissions: agents.NeutralPermissions{ApprovalTimeout: "2h"}}, `from agent "dev"`},
		"sandbox":  {agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Sandbox: "jail"}}, `sandbox "jail" from agent "dev"`},
	} {
		t.Run(name, func(t *testing.T) {
			env := launchtest.Deps(t, launchtest.WithAgent("dev", launchtest.PermissionBlock(tc.block)))
			_, err := resolvePolicy(t, env, launch.Source{Agent: "dev"})
			require.ErrorIs(t, err, launch.ErrPermissionUnhonoured)
			require.ErrorContains(t, err, tc.want)
		})
	}
	env0 := launchtest.Deps(t, launchtest.WithAgent("dev", launchtest.PermissionBlock(fixtureBlock(agents.NeutralPermissions{}, map[string]any{"mode": "plann"}))))
	_, err0 := resolvePolicy(t, env0, launch.Source{Agent: "dev"})
	require.ErrorIs(t, err0, launchtest.ErrFixtureMode, "the engine's own refusal survives the wrap")
	env := launchtest.Deps(t, launchtest.WithAgent("dev", launchtest.PermissionBlock(agents.Permissions{NeutralPermissions: agents.NeutralPermissions{ApprovalTimeout: "2h"}})))
	_, err := resolvePolicy(t, env, launch.Source{Agent: "dev"})
	require.ErrorIs(t, err, engine.ErrApprovalTimeout, "the timeout's own refusal survives the wrap")
}

// The human is only an approver on an engine that can put a request to
// one; approver: none needs nobody.
func TestResolvePolicy_NoCodecNeedsNoApprover(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.NoApprovals())
	_, err := resolvePolicy(t, env, launch.Source{Agent: "dev"})
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured)
	env = launchtest.Deps(t, launchtest.WithAgent("dev", launchtest.PermissionBlock(agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Approver: "none"}})), launchtest.NoApprovals())
	p, err := resolvePolicy(t, env, launch.Source{Agent: "dev"})
	require.NoError(t, err)
	assert.Equal(t, engine.ApproverNone, p.Approver)
}
