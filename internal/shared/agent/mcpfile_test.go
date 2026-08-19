package agent

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// failOpenFs fails Open for exactly one path with a non-NotExist error
// (os.ErrPermission), passing everything else through to the wrapped Fs — the
// seam this regression test uses to distinguish "the ledger doesn't
// exist" from "the ledger could not be read".
type failOpenFs struct {
	afero.Fs
	path string
}

func (f failOpenFs) Open(name string) (afero.File, error) {
	if name == f.path {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrPermission}
	}
	return f.Fs.Open(name)
}

// TestMCPFileConfig_WriteServers_CommandOverride pins the fix at the
// shared MCP-registry reconciler kiro binds (mcpFile()):
// a zero-value CommandOverride ("" — every cell but an isolated container)
// writes EXACTLY CtxloomCommand()'s host self-exec-absolute path, byte-for-
// byte unchanged from before the override existed; a non-empty
// CommandOverride (populated ONLY on the container axis) replaces it.
func TestMCPFileConfig_WriteServers_CommandOverride(t *testing.T) {
	t.Run("host-unchanged: empty override writes CtxloomCommand()", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: func(string, ...interface{}) {}}
		require.NoError(t, c.WriteServers(map[string]wire.MCPServer{MCPServerName: ctxloomBundleServer()}))

		data, err := afero.ReadFile(fs, "/proj/mcp.json")
		require.NoError(t, err)
		assert.Contains(t, string(data), CtxloomCommand(), "no override → the host self-exec-absolute command must be unchanged")
	})

	t.Run("container cell: override wins", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		const containerBin = "/usr/local/bin/ctxloom"
		c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: func(string, ...interface{}) {}, CommandOverride: containerBin}
		require.NoError(t, c.WriteServers(map[string]wire.MCPServer{MCPServerName: ctxloomBundleServer()}))

		data, err := afero.ReadFile(fs, "/proj/mcp.json")
		require.NoError(t, err)
		assert.Contains(t, string(data), containerBin, "a container-cell override must be the emitted command")
		assert.NotContains(t, string(data), CtxloomCommand(), "the host self-exec path must NOT leak in once an override is set")
	})
}

// TestMCPFileConfig_WriteServers_RefusesUnparsableRegistry pins the fix: an
// unparsable MCP registry used to be warned about and silently degraded to an
// EMPTY table, which every caller (WriteServers) then wrote straight back —
// destroying every user-authored server AND every foreign top-level field on
// a success path. It must now refuse instead, matching the "refuse to
// overwrite, never self-heal" posture corrupt-config handling already uses.
func TestMCPFileConfig_WriteServers_RefusesUnparsableRegistry(t *testing.T) {
	fs := afero.NewMemMapFs()
	original := []byte(`{ this is not valid json`)
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", original, 0644))

	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: func(string, ...interface{}) {}}
	err := c.WriteServers(nil)
	require.Error(t, err, "an unparsable registry must refuse the write, not silently replace it")

	data, readErr := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, readErr)
	assert.Equal(t, original, data, "the unparsable file must survive untouched")
}

// TestMCPFileConfig_WriteServers_LedgerReadErrorSurfaces pins the fix:
// readLedger mapped ANY read error (not just "does not exist") to nil, so a
// permission/IO failure silently defeated the ledger — dropManaged then
// believed there was nothing previously managed to drop, the exact failure
// the ledger's own doc comment says it exists to prevent.
func TestMCPFileConfig_WriteServers_LedgerReadErrorSurfaces(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/proj/mcp.json", []byte(`{"mcpServers":{}}`), 0644))
	require.NoError(t, afero.WriteFile(base, "/proj/.ctxloom-managed", []byte("stale-server\tmcp\n"), 0644))
	fs := failOpenFs{Fs: base, path: "/proj/.ctxloom-managed"}

	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: func(string, ...interface{}) {}}
	err := c.WriteServers(nil)
	require.Error(t, err, "a ledger read failure (not simply missing) must surface, not be silently treated as an empty ledger")
}

// TestMCPFileConfig_WriteServers_IsIdempotentAcrossCalls pins the property the
// collision check now RESTS ON: the ledger is exactly the current managed set,
// never an accumulation of every set ever written.
//
// writeLedger overwrites rather than appends, so repeated reconciles converge.
// That is structural today and easy to lose to a refactor that "just appends
// the new names" — and losing it is not cosmetic: WriteServers infers
// "unmanaged, therefore the user's" from a name's ABSENCE after dropManaged
// has cleared every ledger name. A ledger that accumulated stale names would
// silently widen what ctxloom believes it owns, which is the exact direction
// this reconciler must never drift.
func TestMCPFileConfig_WriteServers_IsIdempotentAcrossCalls(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: func(string, ...interface{}) {}}

	bundleMCP := map[string]wire.MCPServer{
		"alpha": {Command: "/bin/alpha"},
		"beta":  {Command: "/bin/beta"},
	}

	require.NoError(t, c.WriteServers(bundleMCP))
	ledger1, err := afero.ReadFile(fs, "/proj/.ctxloom-managed")
	require.NoError(t, err)
	registry1, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)

	require.NoError(t, c.WriteServers(bundleMCP))
	ledger2, err := afero.ReadFile(fs, "/proj/.ctxloom-managed")
	require.NoError(t, err)
	registry2, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)

	assert.Equal(t, string(ledger1), string(ledger2),
		"a second reconcile of the same managed set must leave the ledger byte-identical, not accumulate names")
	assert.Equal(t, string(registry1), string(registry2),
		"a second reconcile of the same managed set must leave the registry byte-identical")

	for _, name := range []string{"alpha", "beta"} {
		count := 0
		for _, l := range strings.Split(strings.TrimSpace(string(ledger2)), "\n") {
			if strings.TrimSpace(strings.SplitN(l, "\t", 2)[0]) == name {
				count++
			}
		}
		assert.Equal(t, 1, count, "%q must appear exactly once in the ledger after two reconciles", name)
	}
}

// warnRecorder returns a Warn func that appends every formatted line to a
// slice the test can assert against, plus a reader for that slice.
func warnRecorder() (warn func(string, ...interface{}), lines func() []string) {
	var got []string
	return func(format string, args ...interface{}) {
			got = append(got, fmt.Sprintf(format, args...))
		}, func() []string {
			return got
		}
}

// TestMCPFileConfig_WriteServers_UnmanagedEntryWithNoCollisionSurvives pins
// the existing promise this reconciler makes for the ordinary case: a
// user-authored server whose name ctxloom never derives is left byte-for-
// byte untouched, and never appears in the ledger.
func TestMCPFileConfig_WriteServers_UnmanagedEntryWithNoCollisionSurvives(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", []byte(`{"mcpServers":{"theirs":{"command":"/usr/bin/theirs","args":["--flag"]}}}`), 0644))
	warn, lines := warnRecorder()
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: warn}

	bundleMCP := map[string]wire.MCPServer{"ours": {Command: "ctxloom-server"}}
	require.NoError(t, c.WriteServers(bundleMCP))

	data, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)
	assert.Contains(t, string(data), `"command": "/usr/bin/theirs"`, "the unmanaged entry's command must survive untouched")
	assert.Contains(t, string(data), `"--flag"`, "the unmanaged entry's args must survive untouched")
	assert.Contains(t, string(data), "ctxloom-server", "the genuinely managed, non-colliding name must still be written")

	ledger, err := afero.ReadFile(fs, "/proj/.ctxloom-managed")
	require.NoError(t, err)
	ledgerStr := string(ledger)
	assert.NotContains(t, ledgerStr, "theirs", "an unmanaged, non-colliding name must never be claimed in the ledger")
	assert.Contains(t, ledgerStr, "ours", "the genuinely managed name must still be claimed in the ledger")
	assert.Empty(t, lines(), "a non-colliding write must not warn")
}

// TestMCPFileConfig_WriteServers_RefusesCollisionWithUserAuthoredName pins
// the fix for lively-canine (consequence 1, SILENT OVERWRITE ON WRITE): a
// name a user hand-authored, that a config/bundle/plugin source later also
// declares, must NOT be overwritten. The colliding server is skipped (its
// original definition survives byte-for-byte), a warning names it, and every
// OTHER managed name in the same call still lands — a single collision must
// not block the rest of the reconcile.
func TestMCPFileConfig_WriteServers_RefusesCollisionWithUserAuthoredName(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", []byte(`{"mcpServers":{"foo":{"command":"/usr/bin/user-foo","args":["--user"]}}}`), 0644))
	warn, lines := warnRecorder()
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: warn}

	bundleMCP := map[string]wire.MCPServer{
		"foo":  {Command: "/opt/ctxloom-bundled-foo"},
		"safe": {Command: "/opt/ctxloom-safe"},
	}
	require.NoError(t, c.WriteServers(bundleMCP), "a name collision must not fail the whole reconcile")

	data, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, `"command": "/usr/bin/user-foo"`, "the colliding user entry's command must survive untouched")
	assert.Contains(t, got, `"--user"`, "the colliding user entry's args must survive untouched")
	assert.NotContains(t, got, "ctxloom-bundled-foo", "the bundle's colliding definition must never be written")
	assert.Contains(t, got, `"command": "/opt/ctxloom-safe"`, "a non-colliding managed server in the same call must still be written")

	ledger, err := afero.ReadFile(fs, "/proj/.ctxloom-managed")
	require.NoError(t, err)
	ledgerStr := string(ledger)
	assert.NotContains(t, ledgerStr, "foo\t", "a refused name must not be claimed in the ledger")
	assert.Contains(t, ledgerStr, "safe", "the non-colliding managed name must still be claimed in the ledger")

	found := false
	for _, l := range lines() {
		if strings.Contains(l, "foo") && strings.Contains(l, "refus") {
			found = true
		}
	}
	assert.True(t, found, "a warning naming the colliding server must be emitted; got: %v", lines())
}

// TestMCPFileConfig_RemoveServers_DoesNotDeleteUserAuthoredEntry pins the fix
// for lively-canine (consequence 2, SILENT DELETION ON REMOVE): because a
// refused collision is never claimed in the ledger, a later RemoveServers
// (uninstall, or a config change that drops the colliding name from the
// managed set) must not delete what began as the user's entry.
func TestMCPFileConfig_RemoveServers_DoesNotDeleteUserAuthoredEntry(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", []byte(`{"mcpServers":{"foo":{"command":"/usr/bin/user-foo","args":["--user"]}}}`), 0644))
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: func(string, ...interface{}) {}}

	bundleMCP := map[string]wire.MCPServer{"foo": {Command: "/opt/ctxloom-bundled-foo"}}
	require.NoError(t, c.WriteServers(bundleMCP))
	require.NoError(t, c.RemoveServers())

	data, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)
	assert.Contains(t, string(data), `"command": "/usr/bin/user-foo"`, "RemoveServers must not delete an entry it never claimed")
}

// TestMCPFileConfig_WriteServers_ManagedNameRoundTripsWriteThenRemove pins
// that a genuinely ctxloom-managed name (no collision) still round-trips:
// write creates it and claims it in the ledger, remove deletes exactly it
// and leaves an unrelated user entry alone.
func TestMCPFileConfig_WriteServers_ManagedNameRoundTripsWriteThenRemove(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", []byte(`{"mcpServers":{"theirs":{"command":"/usr/bin/theirs"}}}`), 0644))
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: func(string, ...interface{}) {}}

	bundleMCP := map[string]wire.MCPServer{"ours": {Command: "/opt/ctxloom-ours"}}
	require.NoError(t, c.WriteServers(bundleMCP))

	data, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)
	assert.Contains(t, string(data), "ctxloom-ours", "the managed server must be written")
	ledger, err := afero.ReadFile(fs, "/proj/.ctxloom-managed")
	require.NoError(t, err)
	assert.Contains(t, string(ledger), "ours", "the managed name must be claimed in the ledger")

	require.NoError(t, c.RemoveServers())
	data, err = afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)
	got := string(data)
	assert.NotContains(t, got, "ctxloom-ours", "RemoveServers must delete the managed server it claimed")
	assert.Contains(t, got, `"command": "/usr/bin/theirs"`, "RemoveServers must leave the unrelated user entry alone")

	_, err = afero.ReadFile(fs, "/proj/.ctxloom-managed")
	assert.True(t, os.IsNotExist(err), "an empty ledger must be removed, not left as an empty file")
}

// TestMCPFileConfig_WriteServers_StaleLedgerNameIsReleasedSilently pins the
// "config no longer declares this name" drift case from reconcileLedger's
// doc: dropManaged always removes every ledger name, and since nothing
// re-derives it this round, it simply stays gone — no warning, because "no
// longer wanted" is the expected case, not a surprise.
func TestMCPFileConfig_WriteServers_StaleLedgerNameIsReleasedSilently(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", []byte(`{"mcpServers":{"gone":{"command":"/opt/ctxloom-gone"}}}`), 0644))
	require.NoError(t, afero.WriteFile(fs, "/proj/.ctxloom-managed", []byte("gone\tmcp\n"), 0644))
	warn, lines := warnRecorder()
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: warn}

	// config no longer declares "gone" at all.
	require.NoError(t, c.WriteServers(nil))

	data, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ctxloom-gone", "a name no longer derived must be released, not recreated")
	assert.Empty(t, lines(), "releasing a no-longer-declared managed name must not warn")
}

// TestMCPFileConfig_WriteServers_RecreatesHandDeletedManagedServerWithWarning
// pins reconcileLedger's one "genuine tell-the-user" drift case: a name the
// ledger claims as managed, but that a human deleted from the registry file
// by hand, gets silently recreated by the ordinary drop-then-readd cycle if
// config/bundle/plugin still derives it — recreated is correct (ctxloom
// still owns the name), but doing it with NO warning is exactly the "looks
// applied and is not" / silent-surprise shape this project's fail-loud
// posture rejects. WriteServers must warn.
func TestMCPFileConfig_WriteServers_RecreatesHandDeletedManagedServerWithWarning(t *testing.T) {
	fs := afero.NewMemMapFs()
	// The registry does NOT have "gone" — hand-deleted since the last write —
	// but the ledger still claims it.
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", []byte(`{"mcpServers":{}}`), 0644))
	require.NoError(t, afero.WriteFile(fs, "/proj/.ctxloom-managed", []byte("gone\tmcp\n"), 0644))
	warn, lines := warnRecorder()
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: warn}

	bundleMCP := map[string]wire.MCPServer{"gone": {Command: "/opt/ctxloom-gone"}}
	require.NoError(t, c.WriteServers(bundleMCP))

	data, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)
	assert.Contains(t, string(data), "ctxloom-gone", "config still derives the name, so ctxloom must recreate it")

	found := false
	for _, l := range lines() {
		if strings.Contains(l, "gone") && strings.Contains(l, "removed by hand") {
			found = true
		}
	}
	assert.True(t, found, "recreating a hand-deleted managed server must warn; got: %v", lines())
}

// TestMCPFileConfig_WriteServers_PreservesLargeNumbers pins that the registry
// rewrite is value-preserving: a number a user put in mcp.json — beside the
// servers block or inside an unmanaged server's own config — must come back
// out of the rewrite as the same literal. A generic decode on the way to the
// canonicaliser rounds anything past float64's exact range, which rewrites the
// user's file while reporting success.
func TestMCPFileConfig_WriteServers_PreservesLargeNumbers(t *testing.T) {
	fs := afero.NewMemMapFs()
	original := `{"timeoutMs": 1234567890123456789,` +
		`"mcpServers": {"theirs": {"command": "x", "budget": 9223372036854775807}}}`
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", []byte(original), 0644))
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerDir: "/proj", Label: "mcp.json", Warn: func(string, ...interface{}) {}}

	bundleMCP := map[string]wire.MCPServer{"ours": {Command: "ctxloom"}}
	require.NoError(t, c.WriteServers(bundleMCP))

	data, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "1234567890123456789", "a preserved top-level number must survive the rewrite exactly")
	assert.Contains(t, got, "9223372036854775807", "a number inside a preserved server's config must survive too")
	assert.Contains(t, got, `"ours"`, "the managed server is still registered")
}
