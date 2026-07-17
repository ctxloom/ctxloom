package mcpschema

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestRelayBudget_DistillationToolsOutrunTheCoordinationDefault is the
// recover-failure regression. Host-relay distillation is N chunks × an LLM
// subprocess each — minutes of honest work — while a coordination frame is a
// round trip. Billing both against the same short default made a 21-chunk
// recover_session fail at 60s every time, mid-distillation, while the host
// carried happily on.
func TestRelayBudget_DistillationToolsOutrunTheCoordinationDefault(t *testing.T) {
	const coordinationDefault = 60 * time.Second

	for _, tool := range []string{
		"recover_session",
		"load_session",
		"compact_session",
		"get_previous_session",
		"list_sessions",
	} {
		budget := RelayBudget(tool)
		assert.Greater(t, budget, coordinationDefault,
			"%s distills a transcript through an LLM — it cannot share the coordination-frame budget", tool)
	}
}

// TestRelayBudget_FastToolsKeepTheDefault: only the genuinely long-running
// tools get an extended budget. Everything else falls through to the caller's
// default, so a hung coordination frame still fails fast.
func TestRelayBudget_FastToolsKeepTheDefault(t *testing.T) {
	for _, tool := range []string{
		ToolAgentSend,
		ToolAgentRecv,
		ToolRoster,
		"assemble_context",
		"search_content",
		"evaluate_triggers",
		"no_such_tool",
	} {
		assert.Zero(t, RelayBudget(tool),
			"%s is not a long-running distillation — it must keep the caller's default budget", tool)
	}
}

// TestRelayBudget_OnlyClassifiedToolsCarryBudgets keeps the budget table from
// drifting off the routing table: a budget for a tool the surface no longer
// serves is dead config, and a budget on a non-relay tool is a category error.
func TestRelayBudget_OnlyClassifiedToolsCarryBudgets(t *testing.T) {
	routes := Routes()
	for tool := range relayBudgets {
		route, classified := routes[tool]
		assert.True(t, classified, "budgeted tool %q is absent from Routes — stale entry", tool)
		assert.Equal(t, RouteHostRelay, route,
			"budgeted tool %q is not host-relayed; only relayed requests carry a plane-2 budget", tool)
	}
}
