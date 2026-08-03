package remote

import (
	"context"
	"path"
	"strings"
)

// TreeFile is one file of a fetched bundle tree: its exact bytes plus the one
// bit of POSIX mode a git tree actually records.
//
// Only the exec bit is carried because only the exec bit EXISTS upstream: git
// stores a blob as 100644 or 100755 and nothing else, so a richer mode type
// here would be inventing information the publisher never sent. It is carried
// at all — rather than dropped, which is what a map[string][]byte does —
// because a skill package ships scripts the model runs on its own, and a
// delivered script without its exec bit is content the agent cannot use with
// no error anywhere to say so.
type TreeFile struct {
	// Data is the file's bytes, verbatim.
	Data []byte
	// Executable reports that the publisher committed this file as mode 100755.
	Executable bool
}

// TreeFetchFunc fetches every file of the tree rooted at root in owner/repo at
// the pinned sha, keyed by ROOT-relative forward-slash path.
//
// It is a seam rather than a direct call because the implementation lives in
// internal/content/remotetree, which imports this package — the whole content
// layer sits above remote, so remote cannot reach back into it. Wiring the
// implementation in at composition time (operations.NewPuller wiring, see
// WithTreeFetcher) is what lets the pinned-remote walker own the traversal
// while pull owns the pin, with neither package importing the other's job.
//
// A Puller with no TreeFetchFunc keeps exactly the single-file behaviour it had
// before this seam existed: a directory-form bundle stays unfetchable rather
// than half-fetched.
type TreeFetchFunc func(ctx context.Context, f Fetcher, owner, repo, root, sha, repoURL string) (map[string]TreeFile, error)

// BundleTreeRoot maps the single-file repo path of a bundle to the repository
// path of its DIRECTORY form: ".ctxloom/content/bundles/<name>.yaml" becomes
// ".ctxloom/content/bundles/<name>".
//
// The two forms are deliberately the same path modulo the extension. That is
// what makes the fallback in fetchForPull a probe rather than a search: there
// is exactly one other place a bundle of this name could be, and a publisher
// who wrote a directory wrote it there.
func BundleTreeRoot(filePath string) string {
	return strings.TrimSuffix(filePath, ".yaml")
}

// BundleManifestName is the file that carries a directory-form bundle's own
// manifest — the tree's counterpart to the whole of a single-file bundle.
const BundleManifestName = "bundle.yaml"

// TreeManifest returns the bundle.yaml bytes of a fetched tree.
//
// A tree with no manifest is not a bundle: internal/bundles reads the name,
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
