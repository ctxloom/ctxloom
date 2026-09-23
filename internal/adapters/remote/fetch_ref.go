package remote

import (
	"context"
	"fmt"
)

// RefContent is what ONE canonical ref resolved to: the bundle TREE at the
// ref's path.
//
// It carries the whole tree rather than its manifest's bytes because a tree's
// items live in files BESIDE its manifest, and internal/core/bundles refuses a
// manifest that declares any item inline — so a tree reduced to its
// bundle.yaml is an envelope whose every item map is empty, a bundle that
// loads, assembles and delivers nothing, with no error anywhere. A caller that
// genuinely wants manifest bytes asks for them by name (TreeManifest).
type RefContent struct {
	// Tree is the complete bundle, keyed by BUNDLE-ROOT-relative forward-slash
	// path ("bundle.yaml", "profiles/parent.yaml"), with each file's declared
	// executability already resolved.
	Tree map[string]TreeFile

	// Root is the repository path the tree answered from, for diagnostics and
	// for naming the bundle.
	Root string
}

// FetchRef fetches the content a canonical ref names at a specific commit sha
// from the local git clone cache (via the cached fetcher factory), WITHOUT
// deciding which part of it the caller cares about.
//
// It is the low-level primitive the bundle/profile readers and the dependency
// -graph walker share: a hash-pinned ref is fully self-describing, so reading
// it needs nothing but the clone at that sha — no lockfile, no registry.
//
// treeFetch supplies the pinned-remote tree walker, wired from above exactly
// as Puller.treeFetch and BundleReader.treeFetch are and for the same layering
// reason (see TreeFetchFunc) — this package cannot reach the walker's
// implementation. Nil means the ref cannot be read, and FetchRef says so.
//
// It returns REMOTE types only. Turning a tree into a *bundles.Bundle needs the
// content layer, which sits ABOVE this package and imports it; a sibling here
// returning a bundle would be an import cycle. That composition lives in
// bundles.ReadRemoteRef.
func FetchRef(ctx context.Context, factory FetcherFactory, auth AuthConfig, ref *Reference, sha string, treeFetch TreeFetchFunc) (RefContent, error) {
	if ref == nil || !ref.IsCanonical() {
		return RefContent{}, fmt.Errorf("not a canonical reference")
	}
	// Same floor as BundleReader.fetchAtLockedSHA — a hash-pinned
	// read is the security property this primitive exists to provide (its own
	// doc: "reading it needs nothing but the clone at that sha"). An empty
	// sha is not "no preference"; every Fetcher resolves "" to the default
	// branch tip, silently downgrading a pinned read to a latest read.
	if sha == "" {
		return RefContent{}, fmt.Errorf("refusing to fetch %s: no SHA pinned (a hash-pinned read must never resolve an empty ref to the latest commit)", ref.String())
	}
	fetcher, err := factory(ref.URL, auth)
	if err != nil {
		return RefContent{}, fmt.Errorf("create fetcher for %s: %w", ref.URL, err)
	}
	owner, repo, err := ParseOwnerRepo(ref.URL)
	if err != nil {
		return RefContent{}, fmt.Errorf("parse repo URL %s: %w", ref.URL, err)
	}
	if treeFetch == nil {
		return RefContent{}, fmt.Errorf("fetch %s@%s: this read has no tree fetcher wired in", ref.String(), sha)
	}
	filePath := ref.BuildFilePath(ref.ItemType)
	tree, treeRoot, terr := ProbeBundleTreeRoots(filePath, func(root string) (map[string]TreeFile, error) {
		return treeFetch(ctx, fetcher, owner, repo, root, sha, ref.URL)
	})
	if terr != nil {
		return RefContent{}, fmt.Errorf("fetch %s@%s: the bundle tree at %s: %w", ref.String(), sha, treeRoot, terr)
	}
	if _, ok := TreeManifest(tree); !ok {
		return RefContent{}, fmt.Errorf("refusing to read %s: the directory %s exists at %s but carries no %s, so nothing can load it as a bundle (it has %d file(s))",
			ref.String(), treeRoot, sha, BundleManifestName, len(tree))
	}
	return RefContent{Tree: tree, Root: treeRoot}, nil
}
