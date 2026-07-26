package remote

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// CheckRetracted checks if a version is retracted in the manifest.
//
// "Could not determine" is NOT "not retracted" (U150-F04). A retraction is the
// only channel a publisher has to withdraw content they already SIGNED, so a
// fault on this path that resolves to a clean bill of health lets the
// withdrawal lose to the publisher's own signature. The two answers must not
// share a return value.
//
// The three faults here are not equally knowable, and this function is honest
// about which is which:
//
//   - An unparseable manifest is UNAMBIGUOUS: the file was fetched, it simply
//     does not parse. There is no reading of that under which the publisher
//     retracted nothing, so it is an error.
//   - A fetch failure is AMBIGUOUS at this seam and is still treated as "no
//     manifest". Fetcher returns an undifferentiated error, so a repo that
//     publishes no manifest (the ordinary case — most do not) is
//     indistinguishable from a network fault or a revoked token. Telling them
//     apart needs a not-found sentinel on the Fetcher interface, which is a
//     cross-cutting change to every implementation; until then, failing loud
//     here would break every manifest-less remote. See U150-F04's escalation.
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
		return false, "", fmt.Errorf("retraction manifest %s in %s/%s could not be parsed, so whether this content has been retracted is UNKNOWN: %w",
			manifestPath, owner, repo, err)
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
