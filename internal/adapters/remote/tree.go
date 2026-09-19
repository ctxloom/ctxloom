package remote

import (
	"context"
	"path"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TreeFile is one file of a fetched bundle tree: its exact bytes, what the
// package DECLARES about its executability, and what git happened to record.
//
// The two mode fields are separate because they answer different questions and
// only one of them is authoritative. A POSIX mode is not portable and a bundle
// tree's digest deliberately excludes mode bits, so what a publisher SAID —
// the `executable:` list inside the hashed, signed sidecar — is the whole of
// what travels. git's 100755 is transport detail that no signature covers.
//
// An exec bit is carried at all — rather than dropped, which is what a
// map[string][]byte does — because a skill package ships scripts the model runs
// on its own, and a delivered script without its exec bit is content the agent
// cannot use with no error anywhere to say so.
type TreeFile struct {
	// Data is the file's bytes, verbatim.
	Data []byte
	// DeclaredExecutable is the package's own statement that this file's exec
	// bit is load-bearing, resolved from the tree's sidecars by whoever fetched
	// it (see TreeFetchFunc). It is what an installer WRITES, because it is
	// what the manifest the same tree generates will claim.
	DeclaredExecutable bool
	// CommittedExecutable reports that the publisher committed this file as
	// mode 100755. It decides nothing: it exists so an installer can tell the
	// publisher that a file they committed executable is not declared
	// executable and therefore landed 0644 — a divergence that is otherwise
	// silent, and whose symptom is a script the model simply cannot run.
	CommittedExecutable bool
}

// TreeFetchFunc fetches every file of the tree rooted at root in owner/repo at
// the pinned sha, keyed by ROOT-relative forward-slash path, with each file's
// DECLARED executability already resolved from the tree's own sidecars.
//
// Resolving the declaration is the fetcher's job rather than the installer's
// for the same layering reason the seam exists at all: reading a sidecar means
// knowing the tree format, which lives in internal/adapters/content, which imports this
// package. An implementation that reported only git's exec bit would install
// files whose modes disagree with the manifest the tree generates.
//
// It is a seam rather than a direct call because the implementation lives in
// internal/adapters/content/remotetree, which imports this package — the whole content
// layer sits above remote, so remote cannot reach back into it. Wiring the
// implementation in at composition time (operations.NewPuller wiring, see
// WithTreeFetcher) is what lets the pinned-remote walker own the traversal
// while pull owns the pin, with neither package importing the other's job.
//
// A Puller with no TreeFetchFunc keeps exactly the single-file behaviour it had
// before this seam existed: a directory-form bundle stays unfetchable rather
// than half-fetched.
type TreeFetchFunc func(ctx context.Context, f Fetcher, owner, repo, root, sha, repoURL string) (map[string]TreeFile, error)

// BundleManifestName is the file that carries a directory-form bundle's own
// manifest — the tree's counterpart to the whole of a single-file bundle.
//
// It is the layout constant, not a second spelling of it: internal/core/bundles
// imports this package, so it cannot be imported back, and both sides now name
// paths.BundleManifestName instead of agreeing by hand.
const BundleManifestName = paths.BundleManifestName

// TreeManifest returns the bundle.yaml bytes of a fetched tree.
//
// A tree with no manifest is not a bundle: internal/core/bundles reads the name,
// version and item lists from it, and a tree missing it would install as a pile
// of files under a bundle's identity that nothing could ever load. Reporting
// that here — at the fetch, naming the root — is the difference between a
// diagnosable publisher mistake and a bundle that silently resolves to nothing.
func TreeManifest(tree map[string]TreeFile) ([]byte, bool) {
	f, ok := tree[BundleManifestName]
	if !ok {
		return nil, false
	}
	return f.Data, true
}

// treeRepoPath joins a root-relative tree path back onto its repository root.
// Exposed for diagnostics that must name the file as the publisher sees it.
func treeRepoPath(root, rel string) string {
	if root == "" {
		return rel
	}
	return path.Join(root, rel)
}

// TreeInstallFunc materializes the pinned tree at subpath, at commit sha, into
// worktreeDir, and returns the directory the bundle landed in.
//
// It is the WRITE half of the pinned-tree seam TreeFetchFunc reads through, and
// it exists for the same layering reason: the implementation has to resolve
// each file's DECLARED executability from the tree's own sidecars, which means
// knowing the tree format, which lives in internal/adapters/content — a package that
// imports this one. Wiring the implementation in at composition time is what
// lets git own the checkout and the content layer own the format, with neither
// package reaching into the other.
//
// It takes NO fetched tree, and that is the point rather than an omission.
// Everything it needs — the bytes, and the sidecars declaring their modes — is
// in the commit it checks out, so advancing a pin is a call to exactly this
// function with a different sha. An installer that required a fetched tree
// would force every pin move to be accompanied by a fetch, which is what made
// "the pin moved but the tree did not" a reachable state.
//
// A Puller with no TreeInstallFunc cannot materialize a bundle at all and says
// so, rather than recording a pin whose content nothing can read.
type TreeInstallFunc func(ctx context.Context, repoURL, sha, subpath, worktreeDir string) (bundleDir string, err error)
