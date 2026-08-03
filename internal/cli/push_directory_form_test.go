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

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
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
// Getting there measured a PRE-EXISTING DEFECT, characterized here and not
// fixed: publishing a directory-form bundle addresses it by the basename of its
// manifest. See the second test.

// writeDirFormBundle hand-builds a directory-form bundle (there is no CLI path
// that creates one — operations.CreateBundle only ever writes `<name>.yaml`)
// and returns its manifest path.
func writeDirFormBundle(t *testing.T, cfg *config.Config, name string) string {
	t.Helper()
	dir := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "greet"), 0o755))
	manifest := filepath.Join(dir, "bundle.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte(
		"version: 1.0.0\nskills:\n  greet:\n    description: say hello\n"), 0o644))
	// The skill body: a real file in the tree, and the thing a reader of this
	// test will assume travels with the bundle.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "greet", "SKILL.md"),
		[]byte("# greet\n\nSay hello.\n"), 0o644))
	return manifest
}

// signFileOnDisk signs path's exact bytes and writes the `.sig` sibling, as
// `ctxloom bundle sign` does.
func signFileOnDisk(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	armored, err := signing.Sign(data, signer, signing.NamespacePublish)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".sig", armored, 0o644))
	return armored
}

// The CARRY question, answered: a directory-form bundle's sidecar rides the
// same path-based carry as any other, byte for byte. This is the same thing
// `bundle move --to <remote>` does (operations.moveToRemote reads the sidecar
// beside src, which for this shape is the manifest) — matched, not reinvented.
func TestPushBundleCfg_DirectoryFormBundle_CarriesTheManifestSidecar(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	manifest := writeDirFormBundle(t, cfg, "dir-form")
	armored := signFileOnDisk(t, manifest)

	cmd, _ := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "dir-form", "", false, "", false, false))

	var sig []byte
	var main []byte
	for path, content := range pub.files {
		if filepath.Ext(path) == ".sig" {
			sig = content
		} else {
			main = content
		}
	}
	require.NotNil(t, sig, "the directory-form bundle's sidecar is carried")
	assert.Equal(t, armored, sig, "carried byte-for-byte")
	assert.NoError(t, signing.CoversBytes(main, sig, signing.NamespacePublish),
		"and it covers exactly the bytes that were published — the MANIFEST, nothing else")
}

// FOUND DEFECT, characterized not fixed, and NOT caused by this change —
// publishing addresses a bundle by the basename of the file it resolved to,
// and for a directory-form bundle that file is always `bundle.yaml`:
//
//	operations.PushBundle:
//	  bundleName := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
//	  targetPath := path.Join(paths.RepoContentPrefix, "bundles", bundleName+".yaml")
//
// So `ctxloom bundle push dir-form` lands at bundles/bundle.yaml, not
// bundles/dir-form.yaml — every directory-form bundle in a project publishes to
// the SAME remote path and silently overwrites the last one. The skills subtree
// that is the entire reason this shape exists is not published at all.
//
// `bundle move --to <remote>` goes through the same PushBundle and inherits
// both. Pinned so the fix (bundles.ExtractBundleName already computes the right
// name, and the tree publish is excusable-flatness's atomic-publish work) is
// visible as a change rather than a surprise.
func TestPushBundleCfg_DirectoryFormBundle_PublishesAsBundleYamlAndDropsSkills(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	writeDirFormBundle(t, cfg, "dir-form")

	cmd, _ := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "dir-form", "", false, "", false, false))

	_, named := pub.files[".ctxloom/content/bundles/dir-form.yaml"]
	assert.False(t, named,
		"CHARACTERIZED: the bundle does NOT publish under its own name")
	_, collides := pub.files[".ctxloom/content/bundles/bundle.yaml"]
	assert.True(t, collides,
		"CHARACTERIZED: it publishes as bundles/bundle.yaml — every directory-form bundle collides here")

	for path := range pub.files {
		assert.NotContains(t, path, "skills/",
			"CHARACTERIZED: the skills subtree is not published at all")
	}
}
