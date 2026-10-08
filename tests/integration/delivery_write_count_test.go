//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// renameCounter counts the renames onto each path: every atomic write lands
// by one.
type renameCounter struct {
	afero.Fs
	mu sync.Mutex
	n  map[string]int
}

func (r *renameCounter) Rename(o, n string) error {
	r.mu.Lock()
	r.n[n]++
	r.mu.Unlock()
	return safefs.Rename(r.Fs, o, n)
}

func (r *renameCounter) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n[path]
}

// TestDelivery_TheAtRestInstallWritesEachFileOnce: claude's at-rest install
// into a project whose .mcp.json and settings.json the user already wrote —
// the MCP item into one, the settings and hooks items both into the other —
// writes each file ONCE, and a redelivery of the same package writes neither.
func TestDelivery_TheAtRestInstallWritesEachFileOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	mcpPath := filepath.Join(project, claude.MCPFileName)
	settingsPath := filepath.Join(project, claude.ConfigDirName, claude.SettingsFileName)
	require.NoError(t, os.WriteFile(mcpPath, []byte(`{"mcpServers": {"theirs": {"command": "their-server"}}}`+"\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o755))
	require.NoError(t, os.WriteFile(settingsPath, []byte(`{"model": "opus"}`+"\n"), 0o644))

	kind, err := claude.Build()
	require.NoError(t, err)
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"),
		compositetest.WithMCP("tasks", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}}),
		compositetest.WithMCP("other", wire.MCPServer{Command: "other-mcp"}))
	pkg.Hooks.Unified.SessionStart = []wire.Hook{{Command: "ctxloom", Args: []string{"hook", "session-bind"}}}
	pkg.Hooks.Unified.PreShell = []wire.Hook{{Command: "ltk", Args: []string{"evaluate"}}}
	pkg.DenyTools = []string{"Task"}
	pkg.Statusline = true

	fs := &renameCounter{Fs: afero.NewOsFs(), n: map[string]int{}}
	root := safefs.New()
	root.Fs = fs
	_, _, err = operations.Deliver(context.Background(), root, kind, pkg, delivery.Loadout{}, atRestAt(project, kind))
	require.NoError(t, err)
	require.Equal(t, 1, fs.count(mcpPath), ".mcp.json is written once")
	require.Equal(t, 1, fs.count(settingsPath), "settings.json is written once, for both the settings and the hooks items")

	fs.n = map[string]int{}
	_, _, err = operations.Deliver(context.Background(), root, kind, pkg, delivery.Loadout{}, atRestAt(project, kind))
	require.NoError(t, err)
	require.Zero(t, fs.count(mcpPath), "a redelivery of the same package does not rewrite .mcp.json")
	require.Zero(t, fs.count(settingsPath), "nor settings.json")

	// A settings+hooks subset with a changed deny list: both kinds' items
	// still fold into ONE write of settings.json, and .mcp.json, which the
	// subset does not speak for, is not touched.
	pkg.DenyTools = []string{"Task", "WebFetch"}
	subset := atRestAt(project, kind)
	subset.Kinds = []present.Kind{present.Settings, present.Hooks}
	fs.n = map[string]int{}
	_, _, err = operations.Deliver(context.Background(), root, kind, pkg, delivery.Loadout{}, subset)
	require.NoError(t, err)
	require.Equal(t, 1, fs.count(settingsPath), "a settings+hooks subset writes settings.json once")
	require.Zero(t, fs.count(mcpPath), "and leaves .mcp.json alone")
}

// atRestAt is the at-rest placement the install and materialize deliver at:
// dir as the project root, under kind's per-kind project writers, every
// kind.
func atRestAt(dir string, kind engine.Engine) operations.Placement {
	return operations.Placement{Start: present.ProjectOnHost(dir), Family: delivery.ProjectWriterFor(kind.Root().Name), Kinds: delivery.AllKinds()}
}
