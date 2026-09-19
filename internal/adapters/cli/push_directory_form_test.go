package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// DIRECTORY-FORM BUNDLES. A bundle exists in two shapes: `<name>.yaml`
// (single-file) and `<name>/bundle.yaml` (directory form, the only shape that
// may carry skills — bundles.Loader refuses `skills:` in a single-file bundle).
//
// The question this file answers for the CARRY work is narrow: WHAT DOES THE
// SIDECAR COVER for a directory-form bundle, and does carry handle it? Answer,
// measured below: the sidecar is `<name>/bundle.yaml.sig` and covers the
// MANIFEST BYTES ONLY — operations.bundleSignable's preimage is
// afero.ReadFile(bundle.Path), and bundle.Path for this shape is the
// bundle.yaml. So carry is exactly the single-file case with a longer path, and
// needs no multi-artifact handling. (Signing a bundle's whole tree — per-file
// .sig plus a signed manifest-of-hashes — is excusable-flatness's job.)
//
// Getting there measured a PRE-EXISTING DEFECT, since fixed: publishing a
// directory-form bundle addressed it by the basename of its manifest, so every
// one of them collided at `bundles/bundle.yaml`, AND (fixed later,
// engaged-chivalry) only that manifest ever traveled — the skills/ subtree,
// the entire reason this shape exists, was silently dropped. See the second
// test for what the corrected publish writes now: the whole directory, under
// the bundle's own name.

// writeDirFormBundle hand-builds a directory-form bundle (there is no CLI path
// that creates one — operations.CreateBundle only ever writes `<name>.yaml`)
// and returns its manifest path.
func writeDirFormBundle(t *testing.T, cfg *config.Config, name string) string {
	t.Helper()
	dir := filepath.Join(authoredV1(cfg.GetAppPaths()[0]), name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "fragments"), 0o755))
	manifest := filepath.Join(dir, "bundle.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte("version: 1.0.0\n"), 0o644))
	// The item body: a real file in the tree, and the thing a reader of this
	// test will assume travels with the bundle.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "greet.md"), []byte("# greet\n\nSay hello.\n"), 0o644))
	return manifest
}

// TestPushBundleCfg_DirectoryFormBundle_PublishesTheWholeTreeAndItsSignature:
// a directory-form bundle publishes as ONE tree — envelope, every item file,
// and the SHA256SUMS manifest with its .sigs/ entry, which ARE the bundle's
// signature and live inside the tree.
func TestPushBundleCfg_DirectoryFormBundle_PublishesTheWholeTreeAndItsSignature(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, signer := discovererWithSoleAgentIdentity(t)
	manifest := writeDirFormBundle(t, cfg, "dir-form")
	_, err := operations.SignBundleFile(cfg, operations.SignBundleRequest{
		Target: operations.SignTarget{BundleName: "dir-form"},
		Signer: signer,
	})
	require.NoError(t, err)
	manifestBytes, err := os.ReadFile(manifest)
	require.NoError(t, err)

	cmd, _ := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "dir-form", "", false, "", false, false))

	const root = ".ctxloom/content/bundles/v2/dir-form"
	assert.Equal(t, manifestBytes, pub.files[root+"/bundle.yaml"],
		"the envelope travels at the tree's own root, verbatim")
	assert.Contains(t, pub.files, root+"/"+content.ManifestPath, "the manifest travels with the tree")
	signed := false
	for path := range pub.files {
		if strings.HasPrefix(path, root+"/"+content.SigDirName+"/") {
			signed = true
		}
	}
	assert.True(t, signed, "the .sigs/ entry signed over the manifest is carried, not dropped")
	assert.Contains(t, pub.files, root+"/fragments/greet.md",
		"the whole tree travels — an item left behind is a bundle that loads with it silently missing")
}
