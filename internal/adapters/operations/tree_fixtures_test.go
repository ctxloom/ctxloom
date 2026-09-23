package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// stageSignedTreeOn writes a tree-form bundle named name under bundlesDir (a
// v2 layout root) on fs — one fragment file beside its envelope — signed
// through its ONE signature, the SHA256SUMS manifest and the .sigs/ entry
// signer makes over it. It returns the tree's directory.
func stageSignedTreeOn(t *testing.T, fs afero.Fs, bundlesDir, name string, signer ssh.Signer) string {
	t.Helper()
	require.NoError(t, fs.MkdirAll(bundlesDir, 0o755))
	st, err := content.NewTreeStore(fs, bundlesDir, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	require.NoError(t, st.Put(context.Background(),
		trust.Ref{Bundle: name, Kind: trust.KindFragment, Name: "keeper"},
		signing.FormRaw,
		content.Fragment{Name: "keeper", ItemMeta: content.ItemMeta{Body: "KEEPER-PAYLOAD"}}))
	require.NoError(t, st.PutRootFile(context.Background(), content.BundleID(name), bundles.DirectoryFormManifest, []byte("version: \"1.0.0\"\n")))
	tree, err := st.Open(context.Background(), content.BundleID(name))
	require.NoError(t, err)
	require.NoError(t, attest.SignBundle(context.Background(), st, tree, treeRelease(t, tree), signer))
	return filepath.Join(bundlesDir, name)
}

// staleTree edits the fragment file of a signed tree after signing, so its
// manifest no longer covers its files: the stale-signature row.
func staleTree(t *testing.T, fs afero.Fs, dir string) {
	t.Helper()
	testsupport.WriteFile(t, fs, filepath.Join(dir, "fragments", "keeper.md"), []byte("EDITED AFTER SIGNING\n"), 0o644)
}
