package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// enqueueRun journals this projection (runEnqueued.MCPServers) and the roster
// surfaces it (consumer.go's ListRunsResult_RunInfo.McpServers). Two coord-side
// invariants ride on it that the summary surface's own tests do not state.

// TestMCPServerNames_JournaledOrderIsStable: composition order is an
// implementation detail of how a spawn plan was assembled; the journaled and
// rostered order must not be, or the same spawn reads differently run to run
// and a journal diff is noise.
func TestMCPServerNames_JournaledOrderIsStable(t *testing.T) {
	unordered := []agent.ChatMCPServer{
		{Name: "taskloom"},
		{Name: "ctxloom-context"},
		{Name: "serena"},
	}
	assert.Equal(t, []string{"ctxloom-context", "serena", "taskloom"}, operations.MCPServerNames(unordered))
}

// TestMCPServerNames_JournalsNamesOnly: command, args and env can each carry a
// credential, and the durable journal must never become a place one could leak.
func TestMCPServerNames_JournalsNamesOnly(t *testing.T) {
	got := operations.MCPServerNames([]agent.ChatMCPServer{
		{Name: "ctxloom-context", Command: "/usr/local/bin/ctxloom", Args: []string{"mcp"}, Env: map[string]string{"CTXLOOM_COORD_CRED": "s3cr3t"}},
	})
	assert.Equal(t, []string{"ctxloom-context"}, got)
}
