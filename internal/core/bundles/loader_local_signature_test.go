package bundles

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
)

// A bundle in the project's own content tree is delivered whatever its
// signature says: no filesystem load path verifies a publisher signature for
// local content, so a stale, corrupt or absent sibling signature never
// withholds it. The author learns about a stale signature when they publish
// (`bundle push` / `bundle move --to` refuse, ErrStaleSignature).

// deliverKeeper resolves the fixture bundle's one fragment through a pipeline
// and returns the delivered content.
func deliverKeeper(t *testing.T, fsys afero.Fs, bundleName string) *LoadedContent {
	t.Helper()
	pipe := admitAllPipe(NewLoader(NewProjectReader(fsys, []string{"/bundles"}, WithReaderReporter(ledger()))).WithReporter(ledger()), false)
	lc, err := pipe.GetFragment(bundleName + "#fragments/keeper")
	require.NoError(t, err, "a signature fact about LOCAL content must never withhold it")
	return lc
}

// signBytesFor signs data under the publish namespace with a throwaway key and
// returns the armored detached signature — real crypto, so "covers these bytes"
// is a fact rather than a fixture convention.
// localTreeFixture stages a tree-form bundle named name, signed through its
// ONE signature (the SHA256SUMS manifest and its .sigs/ entry) when signed is
// true, and returns the filesystem and the bundle directory.
func localTreeFixture(t *testing.T, name string, signed bool) (afero.Fs, string) {
	t.Helper()
	mem := afero.NewMemMapFs()
	v2 := paths.BundlesLayoutRoot("/bundles", paths.LayoutV2)
	require.NoError(t, mem.MkdirAll(v2, 0o755))
	st, err := content.NewTreeStore(mem, v2, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	require.NoError(t, st.Put(context.Background(),
		trust.Ref{Bundle: name, Kind: trust.KindFragment, Name: "keeper"},
		signing.FormRaw,
		content.Fragment{Name: "keeper", ItemMeta: content.ItemMeta{Body: "KEEPER-PAYLOAD"}}))
	require.NoError(t, st.PutRootFile(context.Background(), content.BundleID(name), DirectoryFormManifest,
		[]byte("name: "+name+"\nversion: 2.0.0\n")))
	if signed {
		signer, _ := testSkillSigner(t)
		b, err := st.Open(context.Background(), content.BundleID(name))
		require.NoError(t, err)
		require.NoError(t, attest.SignBundle(context.Background(), st, b, treeRelease(t, b), signer))
	}
	return mem, filepath.Join(v2, name)
}

// editItemFile changes the keeper fragment's file after signing — the
// author's edit that stales the manifest.
func editItemFile(t *testing.T, mem afero.Fs, dir string) {
	t.Helper()
	require.NotEmpty(t, mutateAnItemFile(t, mem, dir))
}

func TestLoader_LoadFile_StaleLocalSignature_Delivers(t *testing.T) {
	mem, dir := localTreeFixture(t, "stale-tools", true)
	editItemFile(t, mem, dir)

	lc := deliverKeeper(t, mem, "stale-tools")

	require.NotNil(t, lc)
	assert.Equal(t, "KEEPER-PAYLOAD\nTAMPERED\n", lc.Content,
		"the content must be delivered — it is local")
}

func TestLoader_LoadFile_ValidLocalSignature_Delivers(t *testing.T) {
	mem, _ := localTreeFixture(t, "valid-tools", true)

	assert.Equal(t, "KEEPER-PAYLOAD", deliverKeeper(t, mem, "valid-tools").Content)
}

func TestLoader_LoadFile_UnsignedLocalBundle_Delivers(t *testing.T) {
	mem, _ := localTreeFixture(t, "plain-tools", false)

	assert.Equal(t, "KEEPER-PAYLOAD", deliverKeeper(t, mem, "plain-tools").Content)
}

func TestLoader_LoadFile_CorruptLocalSignature_Delivers(t *testing.T) {
	mem, dir := localTreeFixture(t, "corrupt-tools", true)
	entries, err := afero.ReadDir(mem, filepath.Join(dir, content.SigDirName))
	require.NoError(t, err)
	require.NotEmpty(t, entries, "the fixture signed the manifest")
	testsupport.WriteFile(t, mem, filepath.Join(dir, content.SigDirName, entries[0].Name()), []byte("not a signature\n"), 0o644)

	assert.Equal(t, "KEEPER-PAYLOAD", deliverKeeper(t, mem, "corrupt-tools").Content)
}
