package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// TestMCPCommandOverrideForPolicy pins dire-five's fix at the SAME run
// boundary CellKindForPolicy is pinned at (cellkind_test.go): only a
// container policy (either base tier) reports a non-empty MCP command
// override — the in-container ctxloom binary path — and every other policy
// (none, worktree) reports "", which is exactly the value that keeps
// agent.CtxloomCommand()'s host self-exec-absolute invariant untouched
// (agent.ResolveMCPCommand("") == CtxloomCommand(), unconditionally). A
// regression here (the override leaking onto none/worktree) would
// reintroduce the staged/installed binary-divergence bug that invariant
// exists to prevent — see settings_io.go's CtxloomCommand doc.
func TestMCPCommandOverrideForPolicy(t *testing.T) {
	cases := []struct {
		name   string
		policy isolation.Policy
		want   string
	}{
		{"none→no override (host self-exec stays)", isolation.None{}, ""},
		{"worktree→no override (host self-exec stays)", isolation.NewWorktree(nil, ""), ""},
		{"container→in-container binary path", isolation.NewContainerFor(nil, "mock").WithImage("img"), "/usr/local/bin/ctxloom"},
		{"container-worktree→in-container binary path", isolation.NewContainerWorktreeFor(nil, "mock", isolation.ImageConfig{Image: "img"}, nil), "/usr/local/bin/ctxloom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MCPCommandOverrideForPolicy(tc.policy)
			assert.Equal(t, tc.want, got, "policy %q → MCP command override", tc.policy.Name())
		})
	}
}
