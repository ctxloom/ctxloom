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

// stageInstalledTree writes a directory-form bundle where `deps pull` installs
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
		filepath.Join(dir, bundles.DirectoryFormManifest), []byte("version: 1.0.0\ndescription: atelier\n"), 0o644))

	tree, err := store.Open(ctx, content.BundleID(filepath.Base(dir)))
	require.NoError(t, err)

	c := &Config{appPaths: []string{treeBase}}
	c.SetFS(fsys)
	return c, store, tree, fsys
}

// readTreeBundle drives the reader the Config builds for one lockfile tree
// entry, and returns both halves a caller cares about: the bundle document, and
// the read that carries what its attestation turned out to be.
func readTreeBundle(t *testing.T, c *Config, ctx context.Context, canonical string, entry remote.LockEntry, root signing.TrustRoot) (*bundles.Bundle, bundles.BundleRead, error) {
	t.Helper()
	reader, err := c.treeBundleReader(canonical, entry, root)
	if err != nil {
		return nil, bundles.BundleRead{}, err
	}
	reads, err := reader.Read(ctx)
	if err != nil {
		return nil, bundles.BundleRead{}, err
	}
	require.Len(t, reads, 1, "one lockfile entry is one bundle")
	return reads[0].Bundle, reads[0], nil
}

func treeEntry() remote.LockEntry {
	return remote.LockEntry{SHA: "0123456789abcdef", URL: "https://github.com/acme/ctx", Tree: true}
}

// The point of the whole change: a pulled tree becomes a bundle document, with
// its hooks in DECLARED order rather than the directory walk's alphabetical one.
func TestLoadTreeBundle_ReadsTheInstalledTreeIntoABundle(t *testing.T) {
	c, _, _, _ := stageInstalledTree(t)
	_, pub := treeTestSigner(t)

	b, read, err := readTreeBundle(t, c, context.Background(), treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.NoError(t, err)
	assert.Empty(t, read.Bundle.Signer(), "an unsigned tree is unsigned-to-us, not an error")
	assert.Empty(t, read.UntrustedSignerFingerprint(), "and it names no key, because there is no signature to name one")
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

	b, _, err := readTreeBundle(t, c, context.Background(), treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
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

	b, read, err := readTreeBundle(t, c, ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, "trent@acme.test", read.Bundle.Signer())
	assert.Empty(t, read.UntrustedSignerFingerprint(),
		"a VERIFIED tree has an identity to show; the display-only fingerprint is for the case that has none")
}

// The state this change exists for, on the tree path: the manifest IS signed,
// and by a key this machine does not trust. The decision is identical to
// unsigned — the content stays on the review path — but the DIAGNOSIS is not,
// so the key is named, display-only, for an out-of-band comparison.
func TestLoadTreeBundle_SignedByAnUntrustedKeyNamesTheKeyWithoutTrustingIt(t *testing.T) {
	ctx := context.Background()
	c, store, tree, _ := stageInstalledTree(t)
	signer, pub := treeTestSigner(t)
	require.NoError(t, attest.SignBundle(ctx, store, tree, signer))

	// A trust root that knows a DIFFERENT key: Carol signed, and nobody here
	// trusts Carol.
	_, strangerPub := treeTestSigner(t)
	b, read, err := readTreeBundle(t, c, ctx, treeCanonical, treeEntry(), treeTrustRoot("someone-else@acme.test", strangerPub))
	require.NoError(t, err, "an untrusted signature is ordinary third-party content, not an error")
	require.NotNil(t, b)
	assert.Empty(t, read.Bundle.Signer(), "nothing verified, so there is no publisher identity")
	assert.Equal(t, ssh.FingerprintSHA256(pub), read.UntrustedSignerFingerprint(),
		"the key that MADE the signature is named for comparison, and named as untrusted")
	assert.Empty(t, b.Signer(), "and naming it must not have granted it anything")
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

	_, _, err = readTreeBundle(t, c, ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundles.ErrTreeBundleWithheld)
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

	_, _, err = readTreeBundle(t, c, ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundles.ErrTreeBundleWithheld)
}

// A lockfile that records a tree which is not on disk must say THAT, not
// "the tree read path does not exist" — the fix is a pull, and the message has
// to point at it.
func TestLoadTreeBundle_MissingTreeNamesThePathAndTheFix(t *testing.T) {
	fsys := afero.NewMemMapFs()
	c := &Config{appPaths: []string{treeBase}}
	c.SetFS(fsys)

	_, pub := treeTestSigner(t)
	_, _, err := readTreeBundle(t, c, context.Background(), treeCanonical, treeEntry(), treeTrustRoot("t@x", pub))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deps pull")
	assert.Contains(t, err.Error(), "atelier")
}

// treeBundleReaders must claim exactly the entries the byte reader refused for
// being tree-shaped, and leave every other failure for the ordinary report.
func TestTreeBundleReaders_ClaimsTreeRefusalsAndLeavesOtherFailuresAlone(t *testing.T) {
	c, _, _, _ := stageInstalledTree(t)
	_, pub := treeTestSigner(t)

	lock := &remote.Lockfile{Bundles: map[string]remote.LockEntry{treeCanonical: treeEntry()}}
	other := assert.AnError
	failures := map[string]error{
		treeCanonical: remote.ErrTreeBundleUnreadable,
		"https://github.com/acme/ctx@bundles/other": other,
	}

	readers := c.treeBundleReaders(lock, treeTrustRoot("trent@acme.test", pub), failures)

	require.Len(t, readers, 1, "the tree entry must get a reader")
	reads, err := readers[0].Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, treeCanonical, reads[0].Ref(), "the canonical ref is the resolution identity")
	assert.Equal(t, treeCanonical, reads[0].Bundle.Name)
	assert.NotContains(t, failures, treeCanonical, "a claimed tree is no longer a failure")
	assert.Equal(t, other, failures["https://github.com/acme/ctx@bundles/other"],
		"a failure that is not a tree refusal must be left untouched")
}

// stageLoaderFormTree writes the RETIRED loader directory form: bundle.yaml
// declares `skills:` inline and the skill's files live in skills/<name>/.
//
// It is staged here only so the REFUSAL can be asserted. This shape is being
// removed, not supported — see TestLoadTreeBundle_RetiredLoaderDirectoryFormIsRefused.
func stageLoaderFormTree(t *testing.T) (*Config, afero.Fs, string) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	dir, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	require.NoError(t, fsys.MkdirAll(filepath.Join(dir, "skills", "good-night"), 0o755))
	require.NoError(t, afero.WriteFile(fsys, filepath.Join(dir, "bundle.yaml"), []byte(
		"version: 1.0.0\ndescription: unattended\nskills:\n  good-night:\n    notes: overnight\n"), 0o644))
	require.NoError(t, afero.WriteFile(fsys, filepath.Join(dir, "skills", "good-night", "SKILL.md"),
		[]byte("---\nname: good-night\ndescription: d\n---\n\nGOOD-NIGHT-BODY\n"), 0o644))

	c := &Config{appPaths: []string{treeBase}}
	c.SetFS(fsys)
	return c, fsys, dir
}

// The loader directory form — bundle.yaml carrying inline items beside a
// skills/ subtree — is RETIRED. A published bundle in that shape is refused
// rather than accommodated, because accommodating it is a backward-compat shim
// and this project's documented upgrade path is to migrate the content.
//
// The refusal has to NAME the migration, or a publisher who has never heard of
// the tree form reads it as a bug in ctxloom rather than as work they owe.
func TestLoadTreeBundle_RetiredLoaderDirectoryFormIsRefused(t *testing.T) {
	c, _, _ := stageLoaderFormTree(t)
	_, pub := treeTestSigner(t)

	_, _, err := readTreeBundle(t, c, context.Background(), treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))
	require.Error(t, err, "the retired shape must not load silently")
	assert.Contains(t, err.Error(), "skills",
		"the refusal must name the inline key that makes it the retired shape")
}
