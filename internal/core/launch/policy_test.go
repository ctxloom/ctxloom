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

// Each field is taken from the first rung that declares it — the agent,
// the label, the project — so a binding that declares only its rules
// inherits its mode, and one that declares only its mode inherits the
// project's rules.
func TestResolvePolicy_FieldByField(t *testing.T) {
	env := launchtest.Deps(t,
		launchtest.WithAgent("dev", launchtest.PermissionBlock(agents.Permissions{Allow: []string{"Read"}, Approver: "none"})),
		launchtest.GuardedLabelPermissions(agents.Permissions{Mode: "acceptEdits", Deny: []string{"Bash(rm *)"}, Allow: []string{"Glob"}}),
		launchtest.ProjectPermissionBlock(agents.Permissions{Ask: []string{"WebFetch"}, ApprovalTimeout: "20m", Allow: []string{"Grep"}, Deny: []string{"Write"}}),
	)
	p, err := resolvePolicy(t, env, launch.Source{Agent: "dev", Label: "guarded"})
	require.NoError(t, err)
	assert.Equal(t, engine.PermissionAcceptEdits, p.Mode, "the label's mode")
	assert.Equal(t, []string{"Read"}, p.Allow, "the agent's allow, not a union")
	assert.Equal(t, []string{"Bash(rm *)"}, p.Deny, "the label's deny")
	assert.Equal(t, []string{"WebFetch"}, p.Ask, "the project's ask")
	assert.Equal(t, engine.ApproverNone, p.Approver)
	assert.Equal(t, 20*time.Minute, p.ApprovalTimeout)
	assert.Equal(t, engine.PermissionAcceptEdits, p.Ceiling, "no after_plan: the ceiling is the mode")
	_, planFirst := p.AfterPlan.Get()
	assert.False(t, planFirst)
}

func TestResolvePolicy_Defaults(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"))
	p, err := resolvePolicy(t, env, launch.Source{Agent: "dev"})
	require.NoError(t, err)
	assert.Equal(t, engine.PermissionPolicy{
		Mode: engine.PermissionDefault, Ceiling: engine.PermissionDefault,
		Approver: engine.ApproverHuman, ApprovalTimeout: engine.DefaultApprovalTimeout,
	}, p, "nothing declared: the host default, the human approves, 15 minutes")
}

func TestResolvePolicy_FlagSetsOnlyTheMode(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev", launchtest.PermissionBlock(agents.Permissions{Mode: "plan", Deny: []string{"Bash"}})))
	p, err := resolvePolicy(t, env, launch.Source{Agent: "dev", Permission: engine.PermissionAcceptEdits})
	require.NoError(t, err)
	assert.Equal(t, engine.PermissionAcceptEdits, p.Mode)
	assert.Equal(t, []string{"Bash"}, p.Deny, "the binding's rules survive a flag")
}

func TestResolvePolicy_PlanFirst(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("planner", launchtest.PermissionBlock(agents.Permissions{Mode: "plan", AfterPlan: "acceptEdits"})))
	p, err := resolvePolicy(t, env, launch.Source{Agent: "planner"})
	require.NoError(t, err)
	assert.Equal(t, engine.PermissionPlan, p.Mode)
	after, ok := p.AfterPlan.Get()
	require.True(t, ok)
	assert.Equal(t, engine.PermissionAcceptEdits, after)
	assert.Equal(t, engine.PermissionAcceptEdits, p.Ceiling, "the ceiling is what an approved plan reaches")

	// A flag that moves the mode off plan leaves nothing for after_plan to
	// continue from: it is dropped, not refused.
	p, err = resolvePolicy(t, env, launch.Source{Agent: "planner", Permission: engine.PermissionDefault})
	require.NoError(t, err)
	_, ok = p.AfterPlan.Get()
	assert.False(t, ok)
	assert.Equal(t, engine.PermissionDefault, p.Ceiling)
}

// On an engine with no read-only tier plan collapses to default, and a
// plan-first posture with it: there is no plan to approve.
func TestResolvePolicy_PlanFirstCollapsesWithPlan(t *testing.T) {
	env := launchtest.Deps(t, launchtest.NoReadOnlyPlan(), launchtest.WithAgent("planner", launchtest.PermissionBlock(agents.Permissions{Mode: "plan", AfterPlan: "acceptEdits"})))
	p, err := resolvePolicy(t, env, launch.Source{Agent: "planner"})
	require.NoError(t, err)
	assert.Equal(t, engine.PermissionDefault, p.Mode)
	_, ok := p.AfterPlan.Get()
	assert.False(t, ok)
	assert.Equal(t, engine.PermissionDefault, p.Ceiling)
}

// Every declaration that cannot be honoured is refused at launch, naming
// the value and the rung it came from.
func TestResolvePolicy_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		opts []launchtest.Option
		want []string
	}{
		"after_plan bypass": {[]launchtest.Option{launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{Mode: "plan", AfterPlan: "bypass"}))}, []string{`"bypass"`, "after_plan", `agent "a"`}},
		"after_plan typo":   {[]launchtest.Option{launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{Mode: "plan", AfterPlan: "acceptEdit"}))}, []string{`"acceptEdit"`, "default|acceptEdits"}},
		"after_plan off plan": {[]launchtest.Option{launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{Mode: "default", AfterPlan: "acceptEdits"}))},
			[]string{"after_plan", "mode: plan"}},
		"approver":         {[]launchtest.Option{launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{Approver: "agent"}))}, []string{`"agent"`, "human|none"}},
		"timeout syntax":   {[]launchtest.Option{launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{ApprovalTimeout: "soon"}))}, []string{`"soon"`, "approval_timeout"}},
		"timeout zero":     {[]launchtest.Option{launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{ApprovalTimeout: "0s"}))}, []string{`"0s"`, "60m"}},
		"timeout over cap": {[]launchtest.Option{launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{ApprovalTimeout: "61m"}))}, []string{`"61m"`, "60m"}},
		"project timeout":  {[]launchtest.Option{launchtest.WithAgent("a"), launchtest.ProjectPermissionBlock(agents.Permissions{ApprovalTimeout: "2h"})}, []string{`"2h"`, "project config"}},
		"rule":             {[]launchtest.Option{launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{Deny: []string{"Read", "!Bash"}}))}, []string{`"!Bash"`, "deny", `agent "a"`}},
		"human, no codec":  {[]launchtest.Option{launchtest.NoApprovals(), launchtest.WithAgent("a")}, []string{"approver", "approver: none"}},
		"rules, no codec":  {[]launchtest.Option{launchtest.NoApprovals(), launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{Approver: "none", Allow: []string{"Read"}}))}, []string{"rules", "Read"}},
	} {
		t.Run(name, func(t *testing.T) {
			env := launchtest.Deps(t, tc.opts...)
			_, err := resolvePolicy(t, env, launch.Source{Agent: "a"})
			require.ErrorIs(t, err, launch.ErrPermissionUnhonoured)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

func TestResolvePolicy_NoCodecNeedsNoApprover(t *testing.T) {
	env := launchtest.Deps(t, launchtest.NoApprovals(), launchtest.WithAgent("a", launchtest.PermissionBlock(agents.Permissions{Approver: "none"})))
	p, err := resolvePolicy(t, env, launch.Source{Agent: "a"})
	require.NoError(t, err)
	assert.Equal(t, engine.ApproverNone, p.Approver)
}

// A child launched by a child is capped at its parent's ceiling: it may
// start no wider, and neither an approved plan nor a mode change may take
// it past it.
func TestResolvePolicy_ParentCeilingCaps(t *testing.T) {
	env := launchtest.Deps(t,
		launchtest.WithAgent("wide", launchtest.Permissions("bypass")),
		launchtest.WithAgent("narrow", launchtest.Permissions("dontAsk")),
		launchtest.WithAgent("planner", launchtest.PermissionBlock(agents.Permissions{Mode: "plan", AfterPlan: "acceptEdits"})),
		launchtest.WithAgent("modest", launchtest.PermissionBlock(agents.Permissions{Mode: "plan", AfterPlan: "default"})),
	)
	_, err := resolvePolicy(t, env, launch.Source{Agent: "wide", ParentCeiling: engine.PermissionAcceptEdits})
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured)
	assert.Contains(t, err.Error(), "ceiling")

	_, err = resolvePolicy(t, env, launch.Source{Agent: "planner", ParentCeiling: engine.PermissionDefault})
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured, "after_plan past the parent's ceiling")

	p, err := resolvePolicy(t, env, launch.Source{Agent: "narrow", ParentCeiling: engine.PermissionAcceptEdits})
	require.NoError(t, err)
	assert.Equal(t, engine.PermissionDontAsk, p.Ceiling)

	p, err = resolvePolicy(t, env, launch.Source{Agent: "modest", ParentCeiling: engine.PermissionAcceptEdits})
	require.NoError(t, err)
	assert.Equal(t, engine.PermissionDefault, p.Ceiling)

	p, err = resolvePolicy(t, env, launch.Source{Agent: "wide"})
	require.NoError(t, err, "a root launch has no parent to cap it")
	assert.Equal(t, engine.PermissionBypass, p.Ceiling)
}
