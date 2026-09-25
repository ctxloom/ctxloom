package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestDeliverProject_ClaudesMCPRecordOverALooseRecordDir: claude's MCP approach
// writes its undo record into the home records directory THROUGH fsstatic's
// copy-on-write overlay, which cannot chmod a directory that already exists
// underneath it. So a records directory an older binary left 0755 is tightened
// only because this delivery path opens the ownership record store on the
// real filesystem before the approach runs. A path that stops doing so fails
// here, on the loose directory, instead of on a user's machine.
func TestDeliverProject_ClaudesMCPRecordOverALooseRecordDir(t *testing.T) {
	testsupport.Isolate(t)
	recordsDir, err := paths.HomeRecordsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(recordsDir, 0o755))
	require.NoError(t, os.Chmod(recordsDir, 0o755))

	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	pkg := compositetest.Fixture(t, compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	dir := t.TempDir()

	_, _, err = DeliverProject(context.Background(), afero.NewOsFs(), kind, pkg, dir)
	require.NoError(t, err)

	require.NotEmpty(t, hewRecordsIn(t, recordsDir), "claude's MCP approach must have written its record into the directory under test")
	require.FileExists(t, filepath.Join(dir, ".mcp.json"))
	info, err := os.Stat(recordsDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// TestDeliver_PreparesTheRecordDirItself: a delivery path that opened its
// ownership record while the records directory did not exist yet, so opening
// tightened nothing, still delivers claude's MCP record over the 0755
// directory an older binary then left. Deliver prepares the record store
// itself before any approach writes through the overlay; no caller ordering
// is involved.
func TestDeliver_PreparesTheRecordDirItself(t *testing.T) {
	testsupport.Isolate(t)
	recordsDir, err := paths.HomeRecordsDir()
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(recordsDir))
	fs := afero.NewOsFs()
	records, err := OwnershipRecordsOn(fs)
	require.NoError(t, err)
	require.NoDirExists(t, recordsDir, "opening the store must have had nothing to tighten")
	require.NoError(t, os.MkdirAll(recordsDir, 0o755))
	require.NoError(t, os.Chmod(recordsDir, 0o755))

	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	pkg := compositetest.Fixture(t, compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	dir := t.TempDir()
	root := kind.Root()
	items := pkg.EngineItems(root.Name)
	plan, err := ProjectPlan(root, items, dir)
	require.NoError(t, err)
	exports, err := kind.Exports(items)
	require.NoError(t, err)

	lo := delivery.Loadout{Plan: plan, Package: pkg, Exports: exports, WorkDir: dir}
	_, err = fsstatic.New(fs).Deliver(context.Background(), lo, root, ProjectTarget(dir, records))
	require.NoError(t, err)

	require.NotEmpty(t, hewRecordsIn(t, recordsDir), "claude's MCP approach must have written its record into the directory under test")
	require.FileExists(t, filepath.Join(dir, ".mcp.json"))
	info, err := os.Stat(recordsDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func hewRecordsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".hew-record.yaml") {
			out = append(out, e.Name())
		}
	}
	return out
}
