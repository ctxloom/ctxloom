package remote

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
)

// CheckRetracted checks if a version is retracted in the manifest.
func CheckRetracted(ctx context.Context, fetcher Fetcher, owner, repo, version string, ref *Reference, itemType ItemType) (bool, string, error) {
	// Try to fetch manifest
	branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return false, "", nil // No manifest, not retracted
	}

	manifestPath := fmt.Sprintf("ctxloom/%s/manifest.yaml", version)
	content, err := fetcher.FetchFile(ctx, owner, repo, manifestPath, branch)
	if err != nil {
		return false, "", nil // No manifest, not retracted
	}

	var manifest Manifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return false, "", nil // Invalid manifest, continue
	}

	// Check retracted entries
	for _, r := range manifest.Retracted {
		if r.Type == itemType && r.Name == ref.Path {
			// Check if the version matches
			if r.Version == ref.ContentVersion || ref.ContentVersion == "" {
				return true, r.Reason, nil
			}
		}
	}

	return false, "", nil
}
