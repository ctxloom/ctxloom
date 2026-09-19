package remote

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// RefContent is what ONE canonical ref resolved to, and it is a choice of two
// because a bundle has two published shapes: the single file at the ref's own
// path, or the DIRECTORY at that path whose bundle.yaml is its manifest.
//
// The two are kept apart rather than collapsed to bytes because collapsing them
// is precisely the data loss this type exists to remove. A tree's items live in
// files BESIDE its manifest, and internal/core/bundles refuses a tree manifest that
// declares any item inline, so a tree reduced to its bundle.yaml is an envelope
// whose every item map is empty — a bundle that loads, assembles and delivers
// nothing, with no error anywhere. A caller that wants the whole bundle must be
// handed the whole tree; one that genuinely wants manifest bytes asks for them
// by name (TreeManifest).
type RefContent struct {
	// Data is the single-file document's bytes, set when the file form
	// answered. Nil for a tree.
	Data []byte

	// Tree is the complete directory-form bundle, keyed by BUNDLE-ROOT-relative
	// forward-slash path ("bundle.yaml", "profiles/parent.yaml"), with each
	// file's declared executability already resolved. Nil for a file.
	Tree map[string]TreeFile

	// Root is the repository path the tree answered from, for diagnostics and
	// for naming the bundle. Empty for a file.
	Root string
}

// IsTree reports whether this ref resolved to a directory-form bundle.
func (c RefContent) IsTree() bool { return c.Tree != nil }

// FetchRef fetches the content a canonical ref names at a specific commit sha
// from the local git clone cache (via the cached fetcher factory), WITHOUT
// deciding which part of it the caller cares about.
//
// It is the low-level primitive the bundle/profile readers and the dependency
// -graph walker share: a hash-pinned ref is fully self-describing, so reading
// it needs nothing but the clone at that sha — no lockfile, no registry.
//
// A bundle has two published shapes and this reads either, reporting WHICH it
// found rather than flattening both to bytes. treeFetch supplies the
// pinned-remote tree walker for the directory shape, wired from above exactly
// as Puller.treeFetch and BundleReader.treeFetch are and for the same layering
// reason (see TreeFetchFunc) — this package cannot reach the walker's
// implementation. Nil keeps the single-file-only behaviour, which for a bundle
// published as a tree means it cannot be read.
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
	filePath := ref.BuildFilePath(ref.ItemType)
	data, fileErr := fetcher.FetchFile(ctx, owner, repo, filePath, sha)
	switch {
	case fileErr == nil:
		return RefContent{Data: data}, nil
	case !errors.Is(fileErr, errs.ErrRemoteContentNotFound):
		return RefContent{}, fmt.Errorf("fetch %s@%s: %w", filePath, sha, fileErr)
	case ref.ItemType != ItemTypeBundle:
		// Only bundles have a directory form. Anything else that is missing is
		// simply missing, and must say so rather than reporting a tree gap.
		return RefContent{}, fmt.Errorf("fetch %s@%s: %w", filePath, sha, fileErr)
	case treeFetch == nil:
		// No walker was wired in, so this read genuinely cannot tell whether a
		// tree is there. Say that, rather than reporting the file's absence as
		// the whole story — a bare "not found" against a repo that DOES publish
		// the directory form is the diagnostic that cost this capability its
		// first attempt.
		return RefContent{}, fmt.Errorf("fetch %s@%s: %w (and this read has no tree fetcher wired in, so %s could not be checked for a directory-form bundle)",
			filePath, sha, fileErr, strings.Join(BundleTreeRoots(filePath), " or "))
	}

	tree, treeRoot, terr := ProbeBundleTreeRoots(filePath, func(root string) (map[string]TreeFile, error) {
		return treeFetch(ctx, fetcher, owner, repo, root, sha, ref.URL)
	})
	if terr != nil {
		// Quote BOTH failures. Either one alone is misleading: the file error
		// alone hides that a directory form was looked for, and the tree error
		// alone reads as though the directory were the only shape a bundle has.
		return RefContent{}, fmt.Errorf("fetch %s@%s: neither the file (%v) nor the directory-form bundle at %s: %w", filePath, sha, fileErr, treeRoot, terr)
	}
	if _, ok := TreeManifest(tree); !ok {
		return RefContent{}, fmt.Errorf("refusing to read %s: the directory %s exists at %s but carries no %s, so nothing can load it as a bundle (it has %d file(s))",
			ref.String(), treeRoot, sha, BundleManifestName, len(tree))
	}
	return RefContent{Tree: tree, Root: treeRoot}, nil
}
