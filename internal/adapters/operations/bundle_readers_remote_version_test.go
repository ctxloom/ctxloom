// Tests for the REMOTE half of the bundleVersionResolver seam: a canonical
// "@<commit>" ref must fetch that commit's bundle document out of the local git
// clone cache and turn those exact bytes into a Bundle through the schema
// upgrade pipeline. Its local sibling is covered in local_version_resolver_test.go.
package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/config"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// remoteContentRepo creates a git repo that PUBLISHES bundles/v2/go-tools (no
// extension: format v2 holds only trees, so its leaf is the bundle's own name)
// the way a publisher does — under the repo-relative bundles prefix, in the
// format root, which is the only place a canonical fetch looks. It commits a
// v1 then a v2 and returns the repo directory plus both commit SHAs.
//
// The document declares its command under the LEGACY `prompts:` key on purpose:
// nothing but bundles.ParseBundle's schema upgrade turns that into a command, so
// a resolver that reached the bytes and unmarshalled them raw would serve a
// bundle with no commands at all — silently, which is the failure this asserts
// against.
func remoteContentRepo(t *testing.T) (repoDir, rev1, rev2 string) {
	t.Helper()
	repoDir = filepath.Join(t.TempDir(), "publisher")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	rel := filepath.Join(filepath.FromSlash(paths.RepoBundlesPrefixFor(paths.LayoutV2)), "go-tools")
	commit := func(body, msg string) string {
		full := filepath.Join(repoDir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
		_, err := wt.Add(filepath.ToSlash(rel))
		require.NoError(t, err)
		h, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
		})
		require.NoError(t, err)
		return h.String()
	}

	rev1 = commit("description: v1\nfragments:\n  fmt:\n    content: R1-BODY\nprompts:\n  review:\n    content: RP1-BODY\n", "v1")
	rev2 = commit("description: v2\nfragments:\n  fmt:\n    content: R2-BODY\nprompts:\n  review:\n    content: RP2-BODY\n", "v2")
	return repoDir, rev1, rev2
}

// TestRemoteRev_DocumentFormIsRefused pins what became of this test's subject.
//
// It used to prove the remote arm of bundleVersionResolver served a DOCUMENT's
// bytes at a historical commit, and that they reached the bundle through
// ParseBundle — a legacy `prompts:` key arriving as a command. The document form
// is no longer readable remotely, so that is no longer a thing to prove; what
// has to be proved instead is that its absence is LOUD.
//
// A repository still holding a single-file bundle is the realistic case here —
// it is what every publisher had before the tree migration — so the refusal has
// to name the shape and the remedy rather than failing as "not found", which
// would send a publisher hunting a path problem they do not have.
//
// NOTHING WAS LOST WITH IT. The legacy `prompts:` upgrade is a property of
// ParseBundle, pinned directly in bundles' own upgrade and strict tests, and it
// still runs on every LOCAL versioned read, which is still a document.
// Historical resolution of a remote bundle is pinned by the tree test below.
func TestRemoteRev_DocumentFormIsRefused(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, rev1, _ := remoteContentRepo(t)
	appDir := filepath.Join(t.TempDir(), "consumer", ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}})
	resolve := BundleVersionResolver(cfg)
	require.NotNil(t, resolve, "an app dir must yield a version resolver")

	canonical := "file://" + filepath.ToSlash(repoDir) + "@bundles/go-tools"

	b, err := resolve(canonical, rev1)
	require.Error(t, err, "a single-file remote bundle must not resolve")
	assert.Nil(t, b, "and nothing may come back alongside the refusal")
	assert.Contains(t, err.Error(), "document form is no longer readable",
		"the refusal must name the SHAPE — 'not found' would send a publisher hunting a path problem")
	assert.Contains(t, err.Error(), "republish it as a tree",
		"and it must name the remedy, since the publisher is the only one who can apply it")
}

// remoteTreeContentRepo is remoteContentRepo's DIRECTORY-form twin: it publishes
// bundles/v2/go-tools as a TREE whose bundle.yaml carries the manifest, which is
// the only shape a publisher can produce since the v1 single-file format was
// removed. It commits a v1 then a v2 and returns the repo directory plus both
// commit SHAs.
func remoteTreeContentRepo(t *testing.T) (repoDir, rev1, rev2 string, pub ssh.PublicKey) {
	var signer ssh.Signer
	t.Helper()
	repoDir = filepath.Join(t.TempDir(), "tree-publisher")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	bundleDir := filepath.Join(filepath.FromSlash(paths.RepoBundlesPrefixFor(paths.LayoutV2)), "go-tools")
	// A PUBLISHABLE tree: bundle.yaml carries envelope keys only and each item
	// is a file beside it. An inline `fragments:` key here is the one shape
	// bundles.readEnvelope refuses outright, so a fixture carrying one asserts
	// against a bundle no publisher can publish.
	// Each rev is SIGNED before it is committed, so every commit carries a
	// manifest and signature covering its own bytes. Reading a remote tree's
	// item files means interpreting publisher-supplied bytes, so the resolver
	// verifies the whole bundle before ReadTree and refuses an unsigned one.
	signer, pub = treeTestSigner(t)
	bundlesRoot := filepath.Join(repoDir, filepath.FromSlash(paths.RepoBundlesPrefixFor(paths.LayoutV2)))
	commit := func(files map[string]string, msg string) string {
		for rel, body := range files {
			full := filepath.Join(repoDir, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
			require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
		}
		// Sign through content.TreeStore + attest.SignBundle, the same objects
		// the consumer verifies through, rather than writing the manifest and
		// signature paths by hand.
		store, err := content.NewTreeStore(afero.NewOsFs(), bundlesRoot, content.Provenance{IsLocal: true})
		require.NoError(t, err)
		tree, err := store.Open(context.Background(), content.BundleID("go-tools"))
		require.NoError(t, err)
		require.NoError(t, attest.SignBundle(context.Background(), store, tree, signer))

		require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
		h, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
		})
		require.NoError(t, err)
		return h.String()
	}

	manifest := filepath.Join(bundleDir, paths.BundleManifestName)
	fragment := filepath.Join(bundleDir, "fragments", "fmt.md")
	// The envelope carries a version: with its items in files beside it, a
	// manifest declaring neither version nor items is refused as an empty
	// bundle — the tree form makes `version:` the envelope's real payload.
	rev1 = commit(map[string]string{manifest: "version: 1.0.0\ndescription: v1\n", fragment: "T1-BODY"}, "v1")
	rev2 = commit(map[string]string{manifest: "version: 2.0.0\ndescription: v2\n", fragment: "T2-BODY"}, "v2")
	return repoDir, rev1, rev2, pub
}

// TestRemoteRev_ResolvesHistoricalVersionOfATreeBundle pins the capability
// childlike-failing named as dead: resolving a version constraint against a
// DIRECTORY-form bundle at an arbitrary historical commit. The ref names a
// single file path that, since the v1 removal, exists for no published bundle;
// without the tree read this fails closed and the caller withholds the item, so
// no tree bundle could carry a version constraint at all.
func TestRemoteRev_ResolvesHistoricalVersionOfATreeBundle(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, rev1, rev2, pub := remoteTreeContentRepo(t)
	appDir := filepath.Join(t.TempDir(), "consumer", ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	// The consumer trusts the publisher to PUBLISH. Without this the tree is
	// merely "unsigned to you" and the resolver refuses it before ReadTree,
	// so the item bytes under test are never reached.
	require.NoError(t, os.WriteFile(paths.AllowedSignersPath(appDir),
		[]byte("publisher@example.com namespaces=\""+signing.NamespacePublish+"\" "+
			string(ssh.MarshalAuthorizedKey(pub))), 0o644))

	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}})
	resolve := BundleVersionResolver(cfg)
	require.NotNil(t, resolve, "an app dir must yield a version resolver")

	canonical := "file://" + filepath.ToSlash(repoDir) + "@bundles/go-tools"

	b1, err := resolve(canonical, rev1)
	require.NoError(t, err, "a directory-form bundle must resolve at a historical commit")
	assert.Equal(t, "T1-BODY", b1.Fragments["fmt"].Content,
		"the pinned rev serves the bytes committed at that rev, read out of the tree's bundle.yaml")

	b2, err := resolve(canonical, rev2)
	require.NoError(t, err)
	assert.Equal(t, "T2-BODY", b2.Fragments["fmt"].Content, "a different rev is its own version")
}
