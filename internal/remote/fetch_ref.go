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
	fetcher, err := factory(ref.URL, auth)
	if err != nil {
		return nil, fmt.Errorf("create fetcher for %s: %w", ref.URL, err)
	}
	owner, repo, err := ParseRepoURL(ref.URL)
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
