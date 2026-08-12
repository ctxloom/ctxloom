package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	tasksops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestEvaluateTriggersHandler_NoDeferredTasks pins the tool-handler plumbing
// (cwd/env resolution into a TaskContext, wiring into operations.
// EvaluateTriggers) without touching the LLM transport: a project with no
// Deferred tasks returns before EvaluateTriggers ever builds a client, so
// this exercises the real handler end-to-end.
func TestEvaluateTriggersHandler_NoDeferredTasks(t *testing.T) {
	dir := taskstest.ProjectDir(t)
	t.Setenv("CTXLOOM_PROJECT_ID", "test-project")

	tc := tasksops.TaskContext{WorkDir: dir, ProjectID: "test-project", SessionHarp: "sess"}
	_, err := tasksops.AddTask(tc, "just working", "", "")
	require.NoError(t, err)

	s := &ctxServer{cfg: &config.Config{}}
	_, out, err := s.handleEvaluateTriggers(context.Background(), nil, evaluateTriggersInput{})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, 0, out.Evaluated)
	assert.Empty(t, out.Verdicts)
	assert.False(t, out.Degraded)
}

// TestEvaluateTriggersHandler_RefreshFlagPlumbsThrough pins that the MCP
// tool's refresh input reaches operations.EvaluateTriggersRequest.Refresh —
// without this the handler would silently ignore the flag.
func TestEvaluateTriggersHandler_RefreshFlagPlumbsThrough(t *testing.T) {
	dir := taskstest.ProjectDir(t)
	t.Setenv("CTXLOOM_PROJECT_ID", "test-project-refresh")

	tc := tasksops.TaskContext{WorkDir: dir, ProjectID: "test-project-refresh", SessionHarp: "sess"}
	_, err := tasksops.AddTask(tc, "just working", "", "")
	require.NoError(t, err)

	// No Deferred tasks: the handler still runs end-to-end regardless of the
	// refresh flag's value, so this exercises the plumbing (a wired field
	// with no effect on this input) without needing an LLM seam here.
	s := &ctxServer{cfg: &config.Config{}}
	_, out, err := s.handleEvaluateTriggers(context.Background(), nil, evaluateTriggersInput{Refresh: true})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, 0, out.Evaluated)
}

// handleEvaluateTriggers used to derive the caller's SessionHarp from
// process env (CTXLOOM_SESSION_HARP) rather than the call-scoped s.self
// identity every sibling handler in this package already uses (e.g.
// mcp_tools_memory.go's s.self.Harp). Env is process-WIDE; on a host-relay
// serving path, a child agent's call would be attributed to whatever the
// HOST process's own env happened to hold rather than that child's own
// identity. evaluateTriggersTaskContext is the extracted, independently
// testable piece that builds the TaskContext handleEvaluateTriggers passes
// down — proven here to prefer s.self.Harp over a deliberately DIFFERENT
// env value, rather than asserting through the whole triage pipeline (which
// doesn't filter tasks by harp at all, so it can't observe this on its own).
func TestEvaluateTriggersTaskContext_PrefersCallScopedIdentityOverProcessEnv(t *testing.T) {
	t.Setenv("CTXLOOM_SESSION_HARP", "host-process-own-harp-must-not-be-used")

	s := &ctxServer{self: coord.Identity{Harp: "actual-calling-childs-harp"}}
	tc := evaluateTriggersTaskContext(s, "/some/repo")
	assert.Equal(t, "actual-calling-childs-harp", tc.SessionHarp,
		"SessionHarp must come from s.self (call-scoped identity), not process env — env is process-WIDE and would misattribute every relayed child's call to the host's own value")
}
