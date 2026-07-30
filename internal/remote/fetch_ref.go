package remote

import (
	"context"
	"fmt"
)

// FetchRefBytes fetches the file content for a canonical ref at a specific
// commit sha from the local git clone cache (via the cached fetcher factory).
// It is the low-level primitive the bundle/profile readers and the dependency
// -graph walker share: a hash-pinned ref is fully self-describing, so reading
// it needs nothing but the clone at that sha — no lockfile, no registry.
func FetchRefBytes(ctx context.Context, factory FetcherFactory, auth AuthConfig, ref *Reference, sha string) ([]byte, error) {
	if ref == nil || !ref.IsCanonical() {
		return nil, fmt.Errorf("not a canonical reference")
	}
	// U093-F01: same floor as BundleReader.fetchAtLockedSHA — a hash-pinned
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
	data, err := fetcher.FetchFile(ctx, owner, repo, filePath, sha)
	if err != nil {
		return nil, fmt.Errorf("fetch %s@%s: %w", filePath, sha, err)
	}
	return data, nil
}
