package content

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/trust"
)

func builtinFS() fstest.MapFS {
	return fstest.MapFS{
		"core/fragments/style.md":           {Data: []byte("STYLE-BODY\n")},
		"core/mcp/ledger.yaml":              {Data: []byte("command: /bin/ledger\n")},
		"core/hooks/pre_tool/00-guard.yaml": {Data: []byte("type: command\ncommand: guard\n")},
		"extra/fragments/other.md":          {Data: []byte("OTHER\n")},
	}
}

func TestBuiltinStore_EnumeratesEmbeddedBundlesAndItems(t *testing.T) {
	ctx := context.Background()
	st, err := NewBuiltinStore(builtinFS())
	require.NoError(t, err)

	ids, err := st.Bundles(ctx)
	require.NoError(t, err)
	assert.Equal(t, []BundleID{"core", "extra"}, ids)

	b, err := st.Open(ctx, "core")
	require.NoError(t, err)
	refs, err := b.Refs(ctx)
	require.NoError(t, err)

	got := map[trust.ItemKind][]string{}
	for _, r := range refs {
		got[r.Kind] = append(got[r.Kind], r.Name)
	}
	assert.Equal(t, []string{"style"}, got[trust.KindFragment])
	assert.Equal(t, []string{"ledger"}, got[trust.KindMCP])
	assert.Equal(t, []string{"pre_tool/00-guard"}, got[trust.KindHook])
}

// Provenance is the store's job, not the surface type's: the SAME registered
// types serve an authored tree and an embedded builtin, and only the store
// knows which. A builtin store that stamped refs as local would let embedded
// content take the local-authored trust path.
func TestBuiltinStore_StampsRefsAsBuiltinNeverLocal(t *testing.T) {
	ctx := context.Background()
	st, err := NewBuiltinStore(builtinFS())
	require.NoError(t, err)

	b, err := st.Open(ctx, "core")
	require.NoError(t, err)
	refs, err := b.Refs(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, refs)
	for _, r := range refs {
		assert.True(t, r.IsBuiltin, "ref %q must be stamped builtin", r.Name)
		assert.False(t, r.IsLocal, "ref %q must NOT be stamped local", r.Name)
		assert.Empty(t, r.RepoURL)
	}
}

// The read-only asymmetry, asserted at the type level rather than trusted. A
// builtin store is embedded in the binary: there is nothing to write to, and a
// Writer that appeared to work would be writing to a discarded in-memory copy.
func TestBuiltinStore_IsNotAWriter(t *testing.T) {
	st, err := NewBuiltinStore(builtinFS())
	require.NoError(t, err)
	_, isWriter := any(st).(Writer)
	assert.False(t, isWriter, "a builtin store must not implement Writer")
}

func TestBuiltinStore_ReadsItemBytesBack(t *testing.T) {
	ctx := context.Background()
	st, err := NewBuiltinStore(builtinFS())
	require.NoError(t, err)
	b, err := st.Open(ctx, "core")
	require.NoError(t, err)

	body, err := b.ReadFile(ctx, "fragments/style.md")
	require.NoError(t, err)
	assert.Equal(t, "STYLE-BODY\n", string(body))
}

func TestBuiltinStore_NilFSIsRefused(t *testing.T) {
	_, err := NewBuiltinStore(nil)
	require.Error(t, err)
}
