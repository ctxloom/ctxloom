package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestSearchRemotes_ManifestFallsThroughOnNoMatches is a regression guard:
// searchSingleRemote used to return the manifest search result
// UNCONDITIONALLY whenever manifest.yaml fetched successfully, even when it
// yielded zero matches — so a stale/partial manifest permanently hid bundles
// actually present in the directory, since the directory fallback could then
// never run. This repo ships a manifest.yaml that omits "widget" (as a real
// publisher's manifest might, after an out-of-band bundle add) alongside an
// actual content/bundles/widget.yaml file on disk.
func TestSearchRemotes_ManifestFallsThroughOnNoMatches(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(baseDir, 0755))

	src := filepath.Join(tmpDir, "source")
	// The manifest lists only an unrelated bundle...
	initLocalRepoWithFile(t, src, ".ctxloom/content/manifest.yaml",
		"bundles:\n  - name: unrelated\n    description: something else\n")
	// ...but the directory itself has "widget" on disk too.
	addFileToLocalRepo(t, src, ".ctxloom/content/bundles/widget.yaml",
		"version: 1.0.0\ndescription: a handy widget bundle\n")

	url := "file://" + src
	remotesContent := "remotes:\n  alice:\n    url: " + url + "\n"
	require.NoError(t, os.WriteFile(paths.RemotesPath(baseDir), []byte(remotesContent), 0644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{baseDir}})

	result, err := SearchRemotes(context.Background(), cfg, SearchRemotesRequest{
		Query: "widget",
	})

	require.NoError(t, err)
	require.NotEmpty(t, result.Results,
		"a manifest that omits a bundle must not permanently hide it from search — the directory listing must still be consulted when the manifest yields no matches; warnings=%v", result.Warnings)
	assert.Equal(t, "widget", result.Results[0].Name)
}

// TestSearchRemotes_TagAwareDirectorySearch drives SearchRemotes end-to-end
// against a real git repo served over a file:// URL, with NO manifest.yaml —
// so the directory-listing path runs. It proves that path reads each file's
// own metadata (tags/description), which is what makes tag: queries and the
// Tags column work. A regression guard for the manifest-less local-clone case
// that both `ctxloom search --remote` and the search_remotes MCP tool hit.
func TestSearchRemotes_TagAwareDirectorySearch(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(baseDir, 0755))

	// One source repo (no manifest.yaml) holding a tagged bundle and profile
	// under the flat .ctxloom/content/<type>/ layout.
	src := filepath.Join(tmpDir, "source")
	bundleYAML := "version: 1.0.0\n" +
		"tags:\n  - golang\n  - testing\n" +
		"description: Go development guidance\n" +
		"fragments:\n  intro:\n    content: hi\n"
	initLocalRepoWithFile(t, src, ".ctxloom/content/bundles/go-development.yaml", bundleYAML)

	// Add a profile to the same repo in a second commit.
	profileYAML := "version: \"1.0.0\"\n" +
		"description: Go developer profile\n" +
		"tags:\n  - golang\n  - backend\n" +
		"bundles:\n  - go-development\n"
	addFileToLocalRepo(t, src, ".ctxloom/content/profiles/go-developer.yaml", profileYAML)

	url := "file://" + src
	remotesContent := "remotes:\n  acme:\n    url: " + url + "\n"
	require.NoError(t, os.WriteFile(paths.RemotesPath(baseDir), []byte(remotesContent), 0644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{baseDir}})
	ctx := context.Background()

	t.Run("tag query matches the bundle via file metadata", func(t *testing.T) {
		res, err := SearchRemotes(ctx, cfg, SearchRemotesRequest{Query: "tag:golang"})
		require.NoError(t, err)
		require.NotNil(t, res)

		byName := map[string]SearchRemoteEntry{}
		for _, e := range res.Results {
			byName[e.Name] = e
		}
		// The tagged bundle matches; its tags and install pull_ref are exposed.
		require.Contains(t, byName, "go-development", "tagged bundle should match (warnings=%v)", res.Warnings)
		assert.Contains(t, byName["go-development"].Tags, "golang")
		assert.Equal(t, "ctxloom+file://"+src+"//bundles/go-development", byName["go-development"].PullRef)
		assert.Equal(t, "bundle", byName["go-development"].Type)

		// The top-level profile is NOT surfaced — top-level profile distribution
		// was retired; profiles ship inside bundles.
		assert.NotContains(t, byName, "go-developer", "top-level profiles are no longer discoverable")
	})

	t.Run("non-matching tag yields nothing", func(t *testing.T) {
		res, err := SearchRemotes(ctx, cfg, SearchRemotesRequest{Query: "tag:rust"})
		require.NoError(t, err)
		assert.Empty(t, res.Results)
	})
}
