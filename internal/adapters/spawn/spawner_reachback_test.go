package spawn

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// childMCPServers can compose a set with no ctxloom MCP server in it —
// and that set is the child's ONLY coordination surface. Without it the child has
// no agent_send, no agent_recv and no agent_report: it launches, consumes its
// budget, and can never answer its parent or be steered. Nothing reported it.
//
// The reachable cause is ctxloom's own loadout server being WITHHELD — a
// profile's `exclude_mcp: [ctxloom]`, or the item rejected. A companion's
// servers are registered unconditionally otherwise, so the ordinary child
// always gets it.
//
// Asserted on the PAYLOAD — the composed set really has no ctxloom entry — plus
// the report, because a warning about a set that did contain one would prove
// nothing.
func TestChildMCPServers_WarnsWhenTheChildGetsNoCtxloomServer(t *testing.T) {
	// dirProfiles writes .ctxloom/profiles/<name>.yaml alongside the config.
	// A DIRECTORY profile is what ResolveBundleMCPServers reads exclude_mcp
	// from, so that is where a project states "withhold this server".
	newSpawner := func(t *testing.T, body string, dirProfiles map[string]string) *spawner {
		t.Helper()
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		appDir := filepath.Join(t.TempDir(), ".ctxloom")
		writeSpawnerConfig(t, appDir, body)
		if len(dirProfiles) > 0 {
			require.NoError(t, os.MkdirAll(filepath.Join(appDir, "profiles"), 0o755))
			for name, doc := range dirProfiles {
				require.NoError(t, os.WriteFile(filepath.Join(appDir, "profiles", name+".yaml"), []byte(doc), 0o644))
			}
		}
		return newSpawner(termRep(), spawnerApp(t, appDir), filepath.Dir(appDir), nil)
	}

	hasCtxloom := func(servers []agent.ChatMCPServer) bool {
		for _, s := range servers {
			if s.Name == agent.MCPServerName {
				return true
			}
		}
		return false
	}

	t.Run("excluding ctxloom's own server strands the child, loudly", func(t *testing.T) {
		s := newSpawner(t,
			"version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions:\n      claude-code:\n        mode: bypass\n    profiles:\n      - noreach\n",
			map[string]string{"noreach": "description: withholds ctxloom's own MCP server\nexclude_mcp:\n  - ctxloom\n"})

		var buf bytes.Buffer
		restore := clidiag.SetSink(&buf)
		plan, err := s.Resolve(context.Background(), "dev")
		restore()
		require.NoError(t, err, "withholding it is a deliberate project choice, not a refusal")

		require.False(t, hasCtxloom(plan.MCPServers),
			"precondition: this config really does compose a child set with no ctxloom server")
		out := buf.String()
		assert.Contains(t, out, "agent_send/agent_recv/agent_report",
			"a child with no coordination surface must be reported, naming what it lost")
		assert.Contains(t, out, "dev", "the report must name which agent is stranded")
	})

	t.Run("the default composition keeps the ctxloom server and stays silent", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions:\n      claude-code:\n        mode: bypass\n", nil)

		var buf bytes.Buffer
		restore := clidiag.SetSink(&buf)
		plan, err := s.Resolve(context.Background(), "dev")
		restore()
		require.NoError(t, err)

		assert.True(t, hasCtxloom(plan.MCPServers),
			"the ordinary child gets its reach-back server from ctxloom's own loadout")
		assert.NotContains(t, buf.String(), "agent_send/agent_recv/agent_report",
			"the ordinary path must not warn")
	})
}
