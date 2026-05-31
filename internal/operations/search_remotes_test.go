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
	// under the flat ctxloom/<type>/ layout.
	src := filepath.Join(tmpDir, "source")
	bundleYAML := "version: 1.0.0\n" +
		"tags:\n  - golang\n  - testing\n" +
		"description: Go development guidance\n" +
		"fragments:\n  intro:\n    content: hi\n"
	initLocalRepoWithFile(t, src, "ctxloom/bundles/go-development.yaml", bundleYAML)

	// Add a profile to the same repo in a second commit.
	profileYAML := "version: \"1.0.0\"\n" +
		"description: Go developer profile\n" +
		"tags:\n  - golang\n  - backend\n" +
		"bundles:\n  - go-development\n"
	addFileToLocalRepo(t, src, "ctxloom/profiles/go-developer.yaml", profileYAML)

	url := "file://" + src
	remotesContent := "remotes:\n  acme:\n    url: " + url + "\n"
	require.NoError(t, os.WriteFile(paths.RemotesPath(baseDir), []byte(remotesContent), 0644))

	cfg := &config.Config{AppPaths: []string{baseDir}}
	ctx := context.Background()

	t.Run("tag query matches via file metadata", func(t *testing.T) {
		res, err := SearchRemotes(ctx, cfg, SearchRemotesRequest{Query: "tag:golang"})
		require.NoError(t, err)
		require.NotNil(t, res)

		byName := map[string]SearchRemoteEntry{}
		for _, e := range res.Results {
			byName[e.Name] = e
		}
		// Both the bundle and the profile carry tag:golang.
		require.Contains(t, byName, "go-development", "tagged bundle should match (warnings=%v)", res.Warnings)
		require.Contains(t, byName, "go-developer", "tagged profile should match")

		// The matched entries expose their tags (not name-only stubs) and a
		// pull_ref the caller can install.
		assert.Contains(t, byName["go-development"].Tags, "golang")
		assert.Equal(t, "acme/go-development", byName["go-development"].PullRef)
		assert.Equal(t, "bundle", byName["go-development"].Type)
		assert.Equal(t, "profile", byName["go-developer"].Type)
	})

	t.Run("item_type narrows to profiles", func(t *testing.T) {
		res, err := SearchRemotes(ctx, cfg, SearchRemotesRequest{Query: "tag:golang", ItemType: "profile"})
		require.NoError(t, err)
		for _, e := range res.Results {
			assert.Equal(t, "profile", e.Type, "item_type=profile must exclude bundles")
		}
	})

	t.Run("non-matching tag yields nothing", func(t *testing.T) {
		res, err := SearchRemotes(ctx, cfg, SearchRemotesRequest{Query: "tag:rust"})
		require.NoError(t, err)
		assert.Empty(t, res.Results)
	})
}
