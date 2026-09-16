package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/confpatch"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// A user's .mcp.json, spelled the way a person spells one and NOT the way a
// JSON encoder does: a top-level key that sorts AFTER "mcpServers" comes
// first, the foreign entry's members are out of alphabetical order, and it
// sits on one line. Every one of those is destroyed by a decode-then-encode
// rewrite and survives a byte-preserving patch, so the assertions below can
// tell the two apart.
const userMCPJSON = "{\n  \"zeta\": 1,\n  \"mcpServers\": {\n    \"other\": {\"command\": \"x\", \"env\": {\"B\": \"2\", \"A\": \"1\"}}\n  }\n}\n"

const foreignEntryLine = `"other": {"command": "x", "env": {"B": "2", "A": "1"}}`

func writeUserMCP(t *testing.T, proj, content string) string {
	t.Helper()
	path := filepath.Join(proj, ".mcp.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(raw)
}

// taskloomRecords lists the application records taskloom has written, so a
// test can assert on the COUNT: one write, one record.
func taskloomRecords(t *testing.T) []string {
	t.Helper()
	dir, err := recordStoreDir()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestManageInstall_PreservesForeignBytesAndRecordsOnce(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	path := writeUserMCP(t, proj, userMCPJSON)

	var errOut bytes.Buffer
	require.NoError(t, manageInstall("claude-code", proj, false, false, &errOut))
	assert.Contains(t, errOut.String(), "registered MCP server")

	got := readFile(t, path)
	servers := readServers(t, path)
	require.Contains(t, servers, "taskloom", "the entry must actually be written, not merely reported")
	entry := servers["taskloom"].(map[string]any)
	assert.Equal(t, "taskloom", entry["command"])
	assert.Equal(t, []any{"mcp"}, entry["args"])

	assert.Contains(t, got, foreignEntryLine,
		"the user's own entry must survive byte for byte, member order and one-line layout included")
	assert.Less(t, strings.Index(got, `"zeta"`), strings.Index(got, `"mcpServers"`),
		"the user's key order must survive; a sorting encoder would move zeta after mcpServers")
	records := taskloomRecords(t)
	assert.Len(t, records, 1, "one write, one record")

	// Registering again changes nothing and touches no record: a re-install
	// that reformats the file, or replaces the record with an identical
	// one under a new name, is churn. The record's NAME is the assertion
	// because the store prunes superseded records, so a count of one
	// cannot tell one-record-per-write from one-record-total.
	require.NoError(t, manageInstall("claude-code", proj, false, false, &errOut))
	assert.Equal(t, got, readFile(t, path), "a repeat install must not rewrite the file")
	assert.Equal(t, records, taskloomRecords(t), "a no-op write must not write a record")
}

func TestManageUninstall_RestoresTheUsersBytesExactly(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	path := writeUserMCP(t, proj, userMCPJSON)
	require.NoError(t, manageInstall("claude-code", proj, false, false, os.Stderr))
	require.NotEqual(t, userMCPJSON, readFile(t, path), "the install must have changed the file for the restore to mean anything")

	var errOut bytes.Buffer
	require.NoError(t, manageUninstall("claude-code", proj, false, &errOut))

	assert.Contains(t, errOut.String(), "removed MCP server")
	assert.Equal(t, userMCPJSON, readFile(t, path),
		"uninstall must hand back exactly the file the user wrote, not a re-encoding of it")
}

// An entry taskloom wrote BEFORE it kept records — or one a user copied in by
// hand from another machine — has no record to reverse it. Ownership is then
// proved from the executable the entry runs, and the entry comes out; the
// user's own servers are not touched.
func TestManageUninstall_ReclaimsARecordlessTaskloomEntry(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	leftover := "{\n  \"mcpServers\": {\n    " + foreignEntryLine + ",\n    \"taskloom\": {\"command\": \"taskloom\", \"args\": [\"mcp\"]}\n  }\n}\n"
	path := writeUserMCP(t, proj, leftover)
	require.Empty(t, taskloomRecords(t), "the premise is that no record exists")

	var errOut bytes.Buffer
	require.NoError(t, manageUninstall("claude-code", proj, false, &errOut))

	assert.Contains(t, errOut.String(), "removed MCP server")
	servers := readServers(t, path)
	assert.NotContains(t, servers, "taskloom", "the leftover must actually be gone")
	assert.Contains(t, readFile(t, path), foreignEntryLine, "the user's own entry survives verbatim")
}

// ctxloom and taskloom both write .mcp.json, each through its own record
// store. The stores must not see each other: a record is one writer's memory
// of what IT applied, and a taskloom that read ctxloom's record as its own
// would reverse ctxloom's servers out of the file on the way in.
func TestManageInstall_LeavesCtxloomsOwnRecordAlone(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	path := writeUserMCP(t, proj, userMCPJSON)

	ctxloomDir, err := paths.HomeRecordsDir()
	require.NoError(t, err)
	fs := afero.NewOsFs()
	ctxloomStore, err := confpatch.NewStore(fs, ctxloomDir, "ctxloom")
	require.NoError(t, err)
	addCtxloom := func(doc *hew.Doc, cur hew.Document) (int, error) {
		p, err := hew.ParsePathIn(doc.Format(), "/mcpServers/ctxloom")
		if err != nil {
			return 0, err
		}
		doc.AtPath(p).Set(map[string]any{"command": "ctxloom", "args": []any{"mcp"}})
		return 1, nil
	}
	res, err := ctxloomStore.Apply(fs, path, addCtxloom)
	require.NoError(t, err)
	require.True(t, res.Changed)
	ctxloomRecord := res.RecordPath

	require.NoError(t, manageInstall("claude-code", proj, false, false, os.Stderr))

	servers := readServers(t, path)
	assert.Contains(t, servers, "ctxloom", "taskloom's install must not reverse ctxloom's application")
	assert.Contains(t, servers, "taskloom")
	assert.FileExists(t, ctxloomRecord, "ctxloom's record must not be pruned as taskloom's superseded one")
	assert.Len(t, taskloomRecords(t), 1)

	// And ctxloom's own uninstall reverses only ITS entry.
	_, err = ctxloomStore.Apply(fs, path, func(*hew.Doc, hew.Document) (int, error) { return 0, nil })
	require.NoError(t, err)
	servers = readServers(t, path)
	assert.NotContains(t, servers, "ctxloom")
	assert.Contains(t, servers, "taskloom", "ctxloom's reversal must not take taskloom's entry with it")
}

func TestManageInstall_PrintOnlyWritesNothing(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	path := writeUserMCP(t, proj, userMCPJSON)

	var errOut bytes.Buffer
	require.NoError(t, manageInstall("claude-code", proj, false, true, &errOut))

	assert.Contains(t, errOut.String(), `"taskloom"`, "print-only must show the document that WOULD be written")
	assert.Contains(t, errOut.String(), foreignEntryLine, "…including the user's own content around it")
	assert.Equal(t, userMCPJSON, readFile(t, path), "print-only must not touch the file")
	assert.Empty(t, taskloomRecords(t), "print-only must not leave a record claiming a write happened")
}

// A config that will not parse is BACKED UP before taskloom refuses: refusing
// already guarantees nothing is destroyed, the backup is what makes the
// original recoverable if the user cannot see what broke it.
func TestManageInstall_CorruptConfigIsBackedUpBeforeRefusing(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	const broken = "{\"mcpServers\": {\"other\": {\"command\": \"x\"}\n"
	path := writeUserMCP(t, proj, broken)

	err := manageInstall("claude-code", proj, false, false, os.Stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backed up")

	assert.Equal(t, broken, readFile(t, path), "the unparseable file is left exactly as found")
	backups, err := filepath.Glob(path + ".corrupt-*")
	require.NoError(t, err)
	require.Len(t, backups, 1, "exactly one backup of the original")
	assert.Equal(t, broken, readFile(t, backups[0]))
	assert.Empty(t, taskloomRecords(t))
}
