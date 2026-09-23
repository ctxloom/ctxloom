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
		require.NoError(t, attest.SignBundle(context.Background(), store, tree, treeRelease(t, tree), signer))

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

	b1, err := resolve(canonical, rev1, onDiskRoot(t, appDir))
	require.NoError(t, err, "a directory-form bundle must resolve at a historical commit")
	assert.Equal(t, "T1-BODY", b1.Fragments["fmt"].Content,
		"the pinned rev serves the bytes committed at that rev, read out of the tree's bundle.yaml")

	b2, err := resolve(canonical, rev2, onDiskRoot(t, appDir))
	require.NoError(t, err)
	assert.Equal(t, "T2-BODY", b2.Fragments["fmt"].Content, "a different rev is its own version")
}
