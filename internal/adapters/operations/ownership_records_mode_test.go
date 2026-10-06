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
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestDeliverProject_ClaudesMCPRecordOverALooseRecordDir: the claims record
// for claude's .mcp.json holds every value ctxloom put there, so a records
// directory an older binary left 0755 must be owner-only before the record
// lands in it; the process establishes it at startup
// (paths.EnsureHomeRoots), before any delivery can run.
func TestDeliverProject_ClaudesMCPRecordOverALooseRecordDir(t *testing.T) {
	testsupport.Isolate(t)
	recordsDir, err := paths.HomeRecordsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(recordsDir, 0o755))
	require.NoError(t, os.Chmod(recordsDir, 0o755))
	require.NoError(t, paths.EnsureHomeRoots(safefs.New().Private))

	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	pkg := compositetest.Fixture(t, compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	dir := t.TempDir()

	_, _, err = DeliverProject(context.Background(), safefs.New(), kind, pkg, dir)
	require.NoError(t, err)

	require.NotEmpty(t, claimsRecordsIn(t, recordsDir), "the delivery must have written its claims record into the directory under test")
	require.FileExists(t, filepath.Join(dir, ".mcp.json"))
	info, err := os.Stat(recordsDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// TestDeliver_CreatesAMissingRecordDirOwnerOnly: a delivery path that opened
// its ownership record while the records directory did not exist yet lands
// its record in a directory the delivery itself creates owner-only.
func TestDeliver_CreatesAMissingRecordDirOwnerOnly(t *testing.T) {
	testsupport.Isolate(t)
	recordsDir, err := paths.HomeRecordsDir()
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(recordsDir))
	fs := afero.NewOsFs()
	records, err := OwnershipRecordsOn(fs)
	require.NoError(t, err)
	require.NoDirExists(t, recordsDir, "opening the store creates nothing")

	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	pkg := compositetest.Fixture(t, compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	dir := t.TempDir()
	root := kind.Root()
	items := pkg.EngineItems(root.Name)
	plan, err := delivery.ProjectPlan(root, items, dir)
	require.NoError(t, err)
	exports, err := kind.Exports(items)
	require.NoError(t, err)

	lo := delivery.Loadout{Plan: plan, Package: pkg, Exports: exports, WorkDir: dir}
	_, err = fsstatic.New(safefs.NewMem(fs)).Deliver(context.Background(), lo, root, delivery.ProjectTarget(dir, records))
	require.NoError(t, err)

	require.NotEmpty(t, claimsRecordsIn(t, recordsDir), "the delivery must have written its claims record into the directory under test")
	require.FileExists(t, filepath.Join(dir, ".mcp.json"))
	info, err := os.Stat(recordsDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func claimsRecordsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".claims.yaml") {
			out = append(out, e.Name())
		}
	}
	return out
}
