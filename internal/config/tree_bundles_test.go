package config

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
	"github.com/ctxloom/ctxloom/internal/content/convert"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

const (
	treeBase      = "/proj/.ctxloom"
	treeCanonical = "https://github.com/acme/ctx@bundles/atelier"
)

func treeTestSigner(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return s, s.PublicKey()
}

func treeTrustRoot(principal string, pub ssh.PublicKey) signing.TrustRoot {
	return allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{principal},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  pub,
	})
}

// stageInstalledTree writes a directory-form bundle where `remote pull` installs
// one, and returns the config reading it plus the tree store for signing.
func stageInstalledTree(t *testing.T) (*Config, *content.TreeStore, content.Bundle, afero.Fs) {
	t.Helper()
	ctx := context.Background()
	fsys := afero.NewMemMapFs()

	dir, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	require.NoError(t, fsys.MkdirAll(dir, 0o755))

	store, err := content.NewTreeStore(fsys, filepath.Dir(dir), content.Provenance{RepoURL: "https://github.com/acme/ctx"})
	require.NoError(t, err)

	src := &bundles.Bundle{
		Fragments: map[string]bundles.BundleFragment{"house-style": {Content: "FRAG-BODY"}},
		Hooks: bundles.BundleHooks{
			PostFileEdit: []bundles.BundleHook{
				{Type: "command", Command: "echo stamp"},
				{Type: "command", Command: "echo audit"},
			},
		},
	}
	require.NoError(t, convert.Convert(ctx, store, content.BundleID(filepath.Base(dir)), src, convert.Options{}))
	require.NoError(t, afero.WriteFile(fsys,
		filepath.Join(dir, convert.EnvelopePath), []byte("version: 1.0.0\ndescription: atelier\n"), 0o644))

	tree, err := store.Open(ctx, content.BundleID(filepath.Base(dir)))
	require.NoError(t, err)

	c := &Config{appPaths: []string{treeBase}}
	c.SetFS(fsys)
	return c, store, tree, fsys
}

func treeEntry() remote.LockEntry {
	return remote.LockEntry{SHA: "0123456789abcdef", URL: "https://github.com/acme/ctx", Tree: true}
}

// The point of the whole change: a pulled tree becomes a bundle document, with
// its hooks in DECLARED order rather than the directory walk's alphabetical one.
func TestLoadTreeBundle_ReadsTheInstalledTreeIntoABundle(t *testing.T) {
	c, _, _, _ := stageInstalledTree(t)
	_, pub := treeTestSigner(t)

	b, signer, err := c.loadTreeBundle(context.Background(), treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.NoError(t, err)
	assert.Empty(t, signer, "an unsigned tree is unsigned-to-us, not an error")
	assert.Equal(t, "1.0.0", b.Version)
	require.Contains(t, b.Fragments, "house-style")
	assert.Equal(t, "FRAG-BODY", b.Fragments["house-style"].Content)
	require.Len(t, b.Hooks.PostFileEdit, 2)
	assert.Equal(t, "echo stamp", b.Hooks.PostFileEdit[0].Command)
}

// Skills are the reason the INSTALLED tree is read rather than the clone at the
// pinned SHA: FSDir must resolve to a real directory or a skill package cannot
// be loaded at all.
func TestLoadTreeBundle_PathResolvesToTheInstalledDirectorySoSkillsCanLoad(t *testing.T) {
	c, _, _, _ := stageInstalledTree(t)
	_, pub := treeTestSigner(t)

	b, _, err := c.loadTreeBundle(context.Background(), treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.NoError(t, err)

	dir, err := b.FSDir()
	require.NoError(t, err, "a tree bundle must have a resolvable directory, unlike a single-file remote bundle")
	want, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	assert.Equal(t, want, dir)
}

// A trusted publisher's manifest signature must reach the bundle as a verified
// principal — that is what lifts its content off the review path.
func TestLoadTreeBundle_SignedByATrustedKeyYieldsThePrincipal(t *testing.T) {
	ctx := context.Background()
	c, store, tree, _ := stageInstalledTree(t)
	signer, pub := treeTestSigner(t)
	require.NoError(t, attest.SignBundle(ctx, store, tree, signer))

	b, principal, err := c.loadTreeBundle(ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, "trent@acme.test", principal)
}

// Editing one file in the installed cache after publication must WITHHOLD the
// bundle, never degrade it to unsigned. Degrading would mean an attacker with
// write access to the cache can turn signed content into merely-reviewable
// content — a downgrade dressed as an ordinary review prompt.
func TestLoadTreeBundle_EditedAfterSigningIsWithheldNotDegradedToUnsigned(t *testing.T) {
	ctx := context.Background()
	c, store, tree, fsys := stageInstalledTree(t)
	signer, pub := treeTestSigner(t)
	require.NoError(t, attest.SignBundle(ctx, store, tree, signer))

	dir, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fsys,
		filepath.Join(dir, "fragments", "house-style.md"), []byte("SUBSTITUTED"), 0o644))

	_, _, err = c.loadTreeBundle(ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTreeBundleWithheld)
}

// A file ADDED to a signed tree is the laundering channel the manifest's
// backwards direction exists to close: nothing enumerates it as an item, so
// nothing would ever look at it.
func TestLoadTreeBundle_FileAddedAfterSigningIsWithheld(t *testing.T) {
	ctx := context.Background()
	c, store, tree, fsys := stageInstalledTree(t)
	signer, pub := treeTestSigner(t)
	require.NoError(t, attest.SignBundle(ctx, store, tree, signer))

	dir, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fsys, filepath.Join(dir, "SMUGGLED.txt"), []byte("x"), 0o644))

	_, _, err = c.loadTreeBundle(ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTreeBundleWithheld)
}

// A lockfile that records a tree which is not on disk must say THAT, not
// "the tree read path does not exist" — the fix is a pull, and the message has
// to point at it.
func TestLoadTreeBundle_MissingTreeNamesThePathAndTheFix(t *testing.T) {
	fsys := afero.NewMemMapFs()
	c := &Config{appPaths: []string{treeBase}}
	c.SetFS(fsys)

	_, pub := treeTestSigner(t)
	_, _, err := c.loadTreeBundle(context.Background(), treeCanonical, treeEntry(), treeTrustRoot("t@x", pub))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote pull")
	assert.Contains(t, err.Error(), "atelier")
}

// seedTreeBundles must claim exactly the entries the byte reader refused for
// being tree-shaped, and leave every other failure for the ordinary report.
func TestSeedTreeBundles_ClaimsTreeRefusalsAndLeavesOtherFailuresAlone(t *testing.T) {
	c, _, _, _ := stageInstalledTree(t)
	_, pub := treeTestSigner(t)

	lock := &remote.Lockfile{Bundles: map[string]remote.LockEntry{treeCanonical: treeEntry()}}
	loaded := map[string]*bundles.Bundle{}
	other := assert.AnError
	failures := map[string]error{
		treeCanonical: remote.ErrTreeBundleUnreadable,
		"https://github.com/acme/ctx@bundles/other": other,
	}

	c.seedTreeBundles(context.Background(), lock, treeTrustRoot("trent@acme.test", pub), loaded, failures)

	require.Contains(t, loaded, treeCanonical, "the tree entry must be seeded")
	assert.Equal(t, treeCanonical, loaded[treeCanonical].Name, "the canonical ref is the seed identity")
	assert.NotContains(t, failures, treeCanonical, "a claimed tree is no longer a failure")
	assert.Equal(t, other, failures["https://github.com/acme/ctx@bundles/other"],
		"a failure that is not a tree refusal must be left untouched")
}
