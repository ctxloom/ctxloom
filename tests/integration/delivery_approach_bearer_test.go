//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// TestDeliveryApproach_ClaudeProjectMCPNeverHoldsTheBearerAcrossSessions:
// claude's unsafe-file MCP form, delivered by the ONE static writer over the
// production record into a project whose .mcp.json the user already wrote.
// Session A delivers, is swept the way a departed session is
// (Static.Reverse), and session B delivers into the same file. At every step
// the file holds the relay bearer by reference only, and the user's server
// survives; B's delivery is not refused by anything A's left behind (claude
// keeps its own confpatch record of the same file beside the ownership
// record).
func TestDeliveryApproach_ClaudeProjectMCPNeverHoldsTheBearerAcrossSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	project := t.TempDir()
	mcpPath := filepath.Join(project, claude.MCPFileName)
	theirs := `{"mcpServers": {"theirs": {"command": "their-server"}}}` + "\n"
	require.NoError(t, os.WriteFile(mcpPath, []byte(theirs), 0o644))
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)

	kind, err := claude.Build()
	require.NoError(t, err)
	root := kind.Root()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"),
		compositetest.WithMCP("ctxloom", wire.MCPServer{ServedBy: wire.ServedBySessionEndpoint}))
	items := pkg.EngineItems(root.Name)
	exports, err := kind.Exports(items)
	require.NoError(t, err)
	static := fsstatic.New(fs)

	deliver := func(harp, bearer string) delivery.Writer {
		t.Helper()
		home := t.TempDir()
		cell := present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}, SessionHome: present.Root{Host: home, Engine: home}}
		plan, err := delivery.Route(items, root, delivery.Preference{Root: map[present.Kind]present.RootKind{present.MCP: present.RootProjectRoot}}, cell)
		require.NoError(t, err)
		w := delivery.SessionWriter(harp)
		lo := delivery.Loadout{Plan: plan, Package: pkg, Exports: exports, MCP: sessions.Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: bearer}}
		_, err = static.Deliver(context.Background(), lo, root, delivery.Target{Root: present.New(present.OnHost(cell)), Ownership: rec, Writer: w})
		require.NoError(t, err, "session %s's delivery", harp)
		body, err := os.ReadFile(mcpPath)
		require.NoError(t, err)
		require.NotContains(t, string(body), bearer, "the project file holds session %s's bearer", harp)
		require.Contains(t, string(body), "${"+claude.EnvRelayBearer+"}")
		require.Contains(t, string(body), "their-server", "the user's server survives session %s's delivery", harp)
		return w
	}

	a := deliver("session-a", "bearer-of-session-a")
	require.NoError(t, static.Reverse(context.Background(), rec, a))
	body, err := os.ReadFile(mcpPath)
	require.NoError(t, err)
	require.JSONEq(t, theirs, string(body), "the sweep leaves the user's file as they wrote it")

	b := deliver("session-b", "bearer-of-session-b")
	require.NoError(t, static.Reverse(context.Background(), rec, b))
	body, err = os.ReadFile(mcpPath)
	require.NoError(t, err)
	require.JSONEq(t, theirs, string(body))
}
