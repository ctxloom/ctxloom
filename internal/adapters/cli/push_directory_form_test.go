package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
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
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "greet"), 0o755))
	manifest := filepath.Join(dir, "bundle.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte(
		"version: 1.0.0\nskills:\n  greet:\n    notes: say hello\n"), 0o644))
	// The skill body: a real file in the tree, and the thing a reader of this
	// test will assume travels with the bundle.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "greet", "SKILL.md"),
		[]byte("# greet\n\nSay hello.\n"), 0o644))
	return manifest
}

// TestPushBundleCfg_DirectoryFormBundle_PublishesTheWholeTreeAndCarriesTheSidecar
// REPLACES the refusal this shape used to get: PushBundle's treeForm branch
// (runTreePush) now publishes a directory-form bundle instead of refusing it
// (format v2 holds only trees), so a refusal here would assert something false
// about current production. What survives from this file's own stated
// purpose — "what does the sidecar cover for a directory-form bundle, and
// does carry handle it?" — is proven directly: sign the manifest on disk
// (exactly what `ctxloom bundle sign` leaves behind), push, and check that the
// signature travels alongside the WHOLE tree, not just the manifest.
func TestPushBundleCfg_DirectoryFormBundle_PublishesTheWholeTreeAndCarriesTheSidecar(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	manifest := writeDirFormBundle(t, cfg, "dir-form")

	manifestBytes, err := os.ReadFile(manifest)
	require.NoError(t, err)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sidecar, err := signing.Sign(manifestBytes, signer, signing.NamespacePublish)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifest+".sig", sidecar, 0o644))

	cmd, _ := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "dir-form", "", false, "", false, false))

	const root = ".ctxloom/content/bundles/v2/dir-form"
	assert.Equal(t, manifestBytes, pub.files[root+"/bundle.yaml"],
		"the manifest travels at the tree's own root, verbatim")
	assert.Equal(t, sidecar, pub.files[root+"/bundle.yaml.sig"],
		"the sidecar signed over the manifest is carried, not dropped")
	assert.Contains(t, pub.files, root+"/skills/greet/SKILL.md",
		"the whole tree travels — a skill left behind is a bundle that loads with an item silently missing")
}
