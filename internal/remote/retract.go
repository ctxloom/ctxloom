package remote

import (
	"context"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// CheckRetracted checks if a version is retracted in the manifest.
func CheckRetracted(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference, itemType ItemType) (bool, string, error) {
	// Try to fetch manifest
	branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return false, "", nil // No manifest, not retracted
	}

	manifestPath := paths.RepoContentPrefix + "/manifest.yaml"
	content, err := fetcher.FetchFile(ctx, owner, repo, manifestPath, branch)
	if err != nil {
		return false, "", nil // No manifest, not retracted
	}

	var manifest Manifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return false, "", nil // Invalid manifest, continue
	}

	// Check retracted entries. A retraction entry with an empty Version retracts
	// the item at every version; an entry pinned to a specific version only fires
	// when the request asks for that exact version. The earlier `ref.ContentVersion
	// == ""` disjunct was wrong: it flagged any unversioned/"latest" install as
	// retracted on the FIRST retracted version of that name, even when the
	// retracted version was not the one being installed.
	for _, r := range manifest.Retracted {
		if r.Type == itemType && r.Name == ref.Path {
			if r.Version == "" || r.Version == ref.ContentVersion {
				return true, r.Reason, nil
			}
		}
	}

	return false, "", nil
}
