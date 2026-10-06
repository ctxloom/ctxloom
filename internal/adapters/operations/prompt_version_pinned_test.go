// Name-addressed prompt version-pinning tests verify that operations.GetPrompt
// (the `ctxloom run --run-prompt` and ctxloom://prompts/<name> surfaces) honors
// a trailing "@<commit>" pin: the pinned historical version assembles, gated by
// ITS OWN content hash, while an unversioned ref keeps resolving the
// lockfile-pinned default and a fetch failure fails closed.
package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// promptVersions builds a def bundle (lockfile default) and a per-commit version
// map carrying prompts, for reuse across the pinning cases.
func promptVersions(defBody string, commitBodies map[string]string) (*bundles.Bundle, map[string]*bundles.Bundle) {
	def := &bundles.Bundle{Commands: map[string]bundles.BundleCommand{"review": {
		ItemBody: bundles.ItemBody{
			Content: defBody,
		},
	}}}
	versions := make(map[string]*bundles.Bundle, len(commitBodies))
	for commit, body := range commitBodies {
		versions[commit] = &bundles.Bundle{Commands: map[string]bundles.BundleCommand{"review": {
			ItemBody: bundles.ItemBody{
				Content: body,
			},
		}}}
	}
	return def, versions
}

// TestGetPrompt_Pinned_ResolvesHistoricalVersion proves a "@<commit>" trailing
// the name-addressed prompt ref resolves that commit's content,
// distinct from the lockfile default.
func TestGetPrompt_Pinned_ResolvesHistoricalVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def, versions := promptVersions("DEFAULT-REVIEW", map[string]string{"c1": "V1-REVIEW"})
	loader, _ := versionPinnedLoader(t, def, versions)

	res, err := GetCommand(context.Background(), nil, GetCommandRequest{
		Name:     cqVersionRef + "#commands/review@c1",
		Pipeline: loader,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Content, "V1-REVIEW", "pinned ref resolves the historical prompt version")
	assert.NotContains(t, res.Content, "DEFAULT-REVIEW", "the lockfile default must NOT be used when pinned")
}

// TestGetPrompt_Unversioned_Unchanged proves an unversioned ref keeps resolving
// the lockfile-pinned default (today's GetPrompt path, untouched).
func TestGetPrompt_Unversioned_Unchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def, versions := promptVersions("DEFAULT-REVIEW", map[string]string{"c1": "V1-REVIEW"})
	loader, _ := versionPinnedLoader(t, def, versions)

	res, err := GetCommand(context.Background(), nil, GetCommandRequest{
		Name:     cqVersionRef + "#commands/review",
		Pipeline: loader,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Content, "DEFAULT-REVIEW", "an unversioned ref resolves the lockfile default")
	assert.NotContains(t, res.Content, "V1-REVIEW")
}

// TestGetPrompt_Pinned_FetchFailureFailsClosed proves a per-version fetch
// failure withholds the item (fail-closed) rather than silently defaulting.
func TestGetPrompt_Pinned_FetchFailureFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def, versions := promptVersions("DEFAULT-REVIEW", map[string]string{"c1": "V1-REVIEW"})
	// "broken" is intentionally absent ⇒ the fake resolver errors.
	loader, _ := versionPinnedLoader(t, def, versions)

	_, err := GetCommand(context.Background(), nil, GetCommandRequest{
		Name:     cqVersionRef + "#commands/review@broken",
		Pipeline: loader,
	})
	require.Error(t, err, "a fetch failure must fail closed (withhold), never silently default")
}
