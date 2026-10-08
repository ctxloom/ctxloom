package operations

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// TestDeliver_AtRest_ACompanionWhoseProbeFailedKeepsItsEntries: a verified
// companion whose loadout probe failed contributes nothing this time, and
// what it contributes is UNKNOWN — so its entries stay as the record says it
// left them. A later delivery whose probe succeeds and no longer names it
// takes them out.
func TestDeliver_AtRest_ACompanionWhoseProbeFailedKeepsItsEntries(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	const taskloom = "bundle:ctxloom+companion:taskloom"
	own := wire.MCPServer{Command: "own-mcp"}
	deliver := func(pkg composite.Package) map[string]any {
		t.Helper()
		_, _, err := Deliver(context.Background(), safefs.NewMem(fs), kind, pkg, delivery.Loadout{}, atRestPlacement(dir, kind.Root().Name, delivery.AllKinds()))
		require.NoError(t, err)
		return mcpServersIn(t, fs, dir)
	}

	got := deliver(composite.Package{MCP: map[string]wire.MCPServer{
		"taskloom": {Command: "taskloom", Args: []string{"mcp"}, SCM: taskloom}, "own": own}})
	require.Contains(t, got, "taskloom")

	got = deliver(composite.Package{MCP: map[string]wire.MCPServer{"own": own}, CarryForward: []string{taskloom}})
	require.Contains(t, got, "taskloom", "the companion whose probe failed keeps its entry")
	require.Contains(t, got, "own")

	got = deliver(composite.Package{MCP: map[string]wire.MCPServer{"own": own}})
	require.NotContains(t, got, "taskloom", "a delivery that no longer names it takes it out")
}

func mcpServersIn(t *testing.T, fs afero.Fs, dir string) map[string]any {
	t.Helper()
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	body, err := afero.ReadFile(fs, dir+"/.mcp.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &doc))
	return doc.MCPServers
}
