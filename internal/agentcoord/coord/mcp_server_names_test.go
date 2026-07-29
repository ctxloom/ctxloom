package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The MCP name projection is what enqueueRun journals (runEnqueued.MCPServers)
// and what the roster surfaces to an operator auditing a live delegation. Two
// things follow: the order must be STABLE, or the same spawn reads differently
// run to run and a journal diff is noise; and it must agree with the identical
// projection the session-init summary uses, since an operator comparing a
// child's roster row against its session summary is entitled to one answer.

// unorderedServers is deliberately not in name order: composition order is what
// a real spawn produces, and it is exactly where two projections diverge.
var unorderedServers = []agent.ChatMCPServer{
	{Name: "taskloom"},
	{Name: "ctxloom-context"},
	{Name: "serena"},
}

func TestMCPServerNames_JournalProjectionMatchesTheSummaryProjection(t *testing.T) {
	assert.Equal(t, operations.MCPServerNames(unorderedServers), mcpServerNames(unorderedServers),
		"the delegation journal and the session-init summary project the same set; they must not describe it in different orders")
}

func TestMCPServerNames_IsSortedSoTheJournalAndRosterAreStable(t *testing.T) {
	assert.Equal(t, []string{"ctxloom-context", "serena", "taskloom"}, mcpServerNames(unorderedServers),
		"composition order is an implementation detail; the journaled and rostered order must not be")
}

func TestMCPServerNames_NoServersIsNilNotAnEmptyList(t *testing.T) {
	assert.Nil(t, mcpServerNames(nil))
	assert.Nil(t, mcpServerNames([]agent.ChatMCPServer{}),
		"nil keeps the field absent from the journal line rather than writing an empty array")
}

// TestMCPServerNames_ProjectsNamesOnly: command, args and env can carry a
// credential, and the journal must never become a place one could leak.
func TestMCPServerNames_ProjectsNamesOnly(t *testing.T) {
	assert.Equal(t, []string{"ctxloom-context"}, mcpServerNames([]agent.ChatMCPServer{
		{Name: "ctxloom-context", Command: "/usr/local/bin/ctxloom", Args: []string{"mcp"}, Env: map[string]string{"SECRET": "s3cr3t"}},
	}))
}
