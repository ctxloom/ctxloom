package remote

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// FetchRefBytes fetches the manifest content for a canonical ref at a specific
// commit sha from the local git clone cache (via the cached fetcher factory).
// It is the low-level primitive the bundle/profile readers and the dependency
// -graph walker share: a hash-pinned ref is fully self-describing, so reading
// it needs nothing but the clone at that sha — no lockfile, no registry.
//
// A bundle has two published shapes, and this reads either: the single file at
// the ref's own path, or the DIRECTORY at that path whose bundle.yaml is the
// manifest. treeFetch supplies the pinned-remote tree walker for the second
// shape, wired from above exactly as Puller.treeFetch and BundleReader.treeFetch
// are and for the same layering reason (see TreeFetchFunc) — this package
// cannot reach the walker's implementation. Nil keeps the single-file-only
// behaviour, which for a bundle published as a tree means it cannot be read.
func FetchRefBytes(ctx context.Context, factory FetcherFactory, auth AuthConfig, ref *Reference, sha string, treeFetch TreeFetchFunc) ([]byte, error) {
	if ref == nil || !ref.IsCanonical() {
		return nil, fmt.Errorf("not a canonical reference")
	}
	// Same floor as BundleReader.fetchAtLockedSHA — a hash-pinned
	// read is the security property this primitive exists to provide (its own
	// doc: "reading it needs nothing but the clone at that sha"). An empty
	// sha is not "no preference"; every Fetcher resolves "" to the default
	// branch tip, silently downgrading a pinned read to a latest read.
	if sha == "" {
		return nil, fmt.Errorf("refusing to fetch %s: no SHA pinned (a hash-pinned read must never resolve an empty ref to the latest commit)", ref.String())
	}
	fetcher, err := factory(ref.URL, auth)
	if err != nil {
		return nil, fmt.Errorf("create fetcher for %s: %w", ref.URL, err)
	}
	owner, repo, err := ParseOwnerRepo(ref.URL)
	if err != nil {
		return nil, fmt.Errorf("parse repo URL %s: %w", ref.URL, err)
	}
	filePath := ref.BuildFilePath(ref.ItemType)
	data, fileErr := fetcher.FetchFile(ctx, owner, repo, filePath, sha)
	switch {
	case fileErr == nil:
		return data, nil
	case !errors.Is(fileErr, errs.ErrRemoteContentNotFound):
		return nil, fmt.Errorf("fetch %s@%s: %w", filePath, sha, fileErr)
	case ref.ItemType != ItemTypeBundle:
		// Only bundles have a directory form. Anything else that is missing is
		// simply missing, and must say so rather than reporting a tree gap.
		return nil, fmt.Errorf("fetch %s@%s: %w", filePath, sha, fileErr)
	case treeFetch == nil:
		// No walker was wired in, so this read genuinely cannot tell whether a
		// tree is there. Say that, rather than reporting the file's absence as
		// the whole story — a bare "not found" against a repo that DOES publish
		// the directory form is the diagnostic that cost this capability its
		// first attempt.
		return nil, fmt.Errorf("fetch %s@%s: %w (and this read has no tree fetcher wired in, so %s could not be checked for a directory-form bundle)",
			filePath, sha, fileErr, strings.Join(BundleTreeRoots(filePath), " or "))
	}

	tree, treeRoot, terr := ProbeBundleTreeRoots(filePath, func(root string) (map[string]TreeFile, error) {
		return treeFetch(ctx, fetcher, owner, repo, root, sha, ref.URL)
	})
	if terr != nil {
		// Quote BOTH failures. Either one alone is misleading: the file error
		// alone hides that a directory form was looked for, and the tree error
		// alone reads as though the directory were the only shape a bundle has.
		return nil, fmt.Errorf("fetch %s@%s: neither the file (%v) nor the directory-form bundle at %s: %w", filePath, sha, fileErr, treeRoot, terr)
	}
	manifest, ok := TreeManifest(tree)
	if !ok {
		return nil, fmt.Errorf("refusing to read %s: the directory %s exists at %s but carries no %s, so nothing can load it as a bundle (it has %d file(s))",
			ref.String(), treeRoot, sha, BundleManifestName, len(tree))
	}
	return manifest, nil
}
