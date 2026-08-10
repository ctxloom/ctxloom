package coord

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// childMCPServers can compose a set with no ctxloom MCP server in it —
// and that set is the child's ONLY coordination surface. Without it the child has
// no agent_send, no agent_recv and no agent_report: it launches, consumes its
// budget, and can never answer its parent or be steered. Nothing reported it.
//
// The reachable cause is `mcp.auto_register_ctxloom: false` (NOT, as the finding
// implied, a failed config load: backends.AssembleManagedMCP always returns a
// non-nil MCPConfig and ShouldAutoRegisterCtxloom defaults to true, so
// ComposeChatMCPServers' nil-nil early return is unreachable from here).
//
// Asserted on the PAYLOAD — the composed set really has no ctxloom entry — plus
// the report, because a warning about a set that did contain one would prove
// nothing.
func TestChildMCPServers_WarnsWhenTheChildGetsNoCtxloomServer(t *testing.T) {
	newSpawner := func(t *testing.T, body string) *prodSpawner {
		t.Helper()
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		appDir := filepath.Join(t.TempDir(), ".ctxloom")
		writeSpawnerConfig(t, appDir, body)
		cfg, err := config.Load(config.WithAppDir(appDir))
		require.NoError(t, err)
		return newProdSpawner(cfg, filepath.Dir(appDir), nil)
	}

	hasCtxloom := func(servers []agent.ChatMCPServer) bool {
		for _, s := range servers {
			if s.Name == agent.MCPServerName {
				return true
			}
		}
		return false
	}

	t.Run("auto_register_ctxloom off strands the child, loudly", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nmcp:\n  auto_register_ctxloom: false\nagents:\n  dev:\n    llm: claude-code\n    permissions: bypass\n")

		var buf bytes.Buffer
		restore := clidiag.SetSink(&buf)
		plan, err := s.Resolve(context.Background(), "dev")
		restore()
		require.NoError(t, err, "the setting is a deliberate project choice, not a refusal")

		require.False(t, hasCtxloom(plan.MCPServers),
			"precondition: this config really does compose a child set with no ctxloom server")
		out := buf.String()
		assert.Contains(t, out, "agent_send/agent_recv/agent_report",
			"a child with no coordination surface must be reported, naming what it lost")
		assert.Contains(t, out, "dev", "the report must name which agent is stranded")
	})

	t.Run("the default composition keeps the ctxloom server and stays silent", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: bypass\n")

		var buf bytes.Buffer
		restore := clidiag.SetSink(&buf)
		plan, err := s.Resolve(context.Background(), "dev")
		restore()
		require.NoError(t, err)

		assert.True(t, hasCtxloom(plan.MCPServers),
			"the ordinary child gets its reach-back server auto-registered")
		assert.NotContains(t, buf.String(), "agent_send/agent_recv/agent_report",
			"the ordinary path must not warn")
	})
}
