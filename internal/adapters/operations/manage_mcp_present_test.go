package operations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func mcpPresent(t *testing.T, fs afero.Fs, dir string) bool {
	t.Helper()
	res, err := HarnessStatus(context.Background(), engines.Registry(), &config.Config{}, HarnessStatusRequest{FS: fs, WorkDir: dir})
	require.NoError(t, err)
	return backendWiring(t, res, "claude-code").MCPPresent
}

var installedServer = map[string]wire.MCPServer{"tasks": {Command: "taskloom", Args: []string{"mcp"}}}

// TestHarnessStatus_MCPPresentIsWhatTheProjectWriterInstalled: the record,
// not the file, says whether ctxloom's MCP servers are installed: the user's
// own .mcp.json is not an install, an uninstall ends one, and an entry the
// user has since taken out is no longer one.
func TestHarnessStatus_MCPPresentIsWhatTheProjectWriterInstalled(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	mcpPath := filepath.Join(dir, ".mcp.json")
	theirs := `{"mcpServers": {"theirs": {"command": "their-server"}}}` + "\n"
	testsupport.WriteFileString(t, fs, mcpPath, theirs, 0o644)
	require.False(t, mcpPresent(t, fs, dir), "the user's own servers are not ctxloom's install")

	deliverManagedSettings(t, "claude-code", nil, installedServer, false, dir, fs)
	require.True(t, mcpPresent(t, fs, dir))

	body, err := afero.ReadFile(fs, mcpPath)
	require.NoError(t, err)
	testsupport.WriteFileString(t, fs, mcpPath, strings.Replace(string(body), `"taskloom"`, `"edited"`, 1), 0o644)
	require.False(t, mcpPresent(t, fs, dir), "an entry the file no longer holds is not installed")
	testsupport.WriteFileString(t, fs, mcpPath, string(body), 0o644)

	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	require.NoError(t, RemoveProject(context.Background(), fs, kind, dir))
	require.False(t, mcpPresent(t, fs, dir), "an uninstall ends the install")
}

// TestHarnessStatus_ASessionsEntryIsNotTheProjectsInstall: a run that put
// its servers in the project's .mcp.json installed nothing at rest.
func TestHarnessStatus_ASessionsEntryIsNotTheProjectsInstall(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	root := kind.Root()
	pkg := composite.Package{MCP: installedServer}
	items := pkg.EngineItems(root.Name)
	plan, err := ProjectPlan(root, items, dir)
	require.NoError(t, err)
	records, err := OwnershipRecordsOn(fs)
	require.NoError(t, err)
	target := ProjectTarget(dir, records)
	target.Writer = delivery.SessionWriter("brisk-otter")
	_, err = fsstatic.New(fs).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, WorkDir: dir}, root, target)
	require.NoError(t, err)
	require.False(t, mcpPresent(t, fs, dir))
}
