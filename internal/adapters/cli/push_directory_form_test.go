package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// DIRECTORY-FORM BUNDLES. A bundle exists in two shapes: `<name>.yaml`
// (single-file) and `<name>/bundle.yaml` (directory form, the only shape that
// may carry skills — bundles.Loader refuses `skills:` in a single-file bundle).
//
// Getting there measured a PRE-EXISTING DEFECT, since fixed: publishing a
// directory-form bundle addressed it by the basename of its manifest, so every
// one of them collided at `bundles/bundle.yaml`, AND (fixed later,
// engaged-chivalry) only that manifest ever traveled — the skills/ subtree,
// the entire reason this shape exists, was silently dropped. See the second
// test for what the corrected publish writes now: the whole directory, under
// the bundle's own name.

// writeDirFormBundle hand-builds a directory-form bundle (there is no CLI path
// that creates one — operations.CreateBundle only ever writes `<name>.yaml`)
// and returns its manifest path.
func writeDirFormBundle(t *testing.T, cfg *config.Config, name string) string {
	t.Helper()
	dir := filepath.Join(authoredV1(cfg.GetAppPaths()[0]), name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "fragments"), 0o755))
	manifest := filepath.Join(dir, "bundle.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte("version: 1.0.0\n"), 0o644))
	// The item body: a real file in the tree, and the thing a reader of this
	// test will assume travels with the bundle.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "greet.md"), []byte("# greet\n\nSay hello.\n"), 0o644))
	return manifest
}

// TestPushBundleCfg_DirectoryFormBundle_PublishesTheWholeTree: a
// directory-form bundle publishes as ONE tree — envelope and every item file.
func TestPushBundleCfg_DirectoryFormBundle_PublishesTheWholeTree(t *testing.T) {
	cfg, pub, mgr := pushTestSetup(t)
	manifest := writeDirFormBundle(t, cfg, "dir-form")
	manifestBytes, err := os.ReadFile(manifest)
	require.NoError(t, err)

	cmd, _ := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, mgr, "dir-form", "", false, ""))

	const root = ".ctxloom/content/bundles/v2/dir-form"
	assert.Equal(t, manifestBytes, pub.files[root+"/bundle.yaml"],
		"the envelope travels at the tree's own root, verbatim")
	assert.Contains(t, pub.files, root+"/fragments/greet.md",
		"the whole tree travels — an item left behind is a bundle that loads with it silently missing")
}

// stubPublisher is a minimal remote.Publisher fake that records every file it
// is asked to write, by path.
type stubPublisher struct {
	files map[string][]byte
}

func newStubPublisher() *stubPublisher { return &stubPublisher{files: map[string][]byte{}} }

func (p *stubPublisher) CreateOrUpdateFile(_ context.Context, _, _, path, _, _ string, content []byte) (string, error) {
	p.files[path] = content
	return "deadbeef", nil
}
func (p *stubPublisher) CreateOrUpdateFiles(_ context.Context, _, _, _, _ string, files map[string][]byte) (string, error) {
	for path, content := range files {
		p.files[path] = content
	}
	return "deadbeef", nil
}
func (p *stubPublisher) CreatePullRequest(_ context.Context, _, _, _, _, _, _ string) (string, error) {
	return "https://example.com/pr/1", nil
}
func (p *stubPublisher) CreateBranch(_ context.Context, _, _, _, _ string) error { return nil }
func (p *stubPublisher) GetFileSHA(_ context.Context, _, _, _, _ string) (string, error) {
	return "", nil
}

// pushTestSetup is a project with one registered remote and a publish manager
// whose publisher records what it was handed instead of touching a network.
func pushTestSetup(t *testing.T) (cfg *config.Config, pub *stubPublisher, mgr *remote.PublishManager) {
	t.Helper()
	appDir := t.TempDir()
	require.NoError(t, os.MkdirAll(authoredV1(appDir), 0o755))
	cfg = config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	require.NoError(t, os.WriteFile(filepath.Join(appDir, "remotes.yaml"), []byte(`default: personal
remotes:
  personal:
    url: https://github.com/example/personal-bundles
    version: v1
`), 0o644))

	registry, err := remote.NewRegistry(filepath.Join(appDir, "remotes.yaml"))
	require.NoError(t, err)

	pub = newStubPublisher()
	mockFetcher := &remote.MockFetcher{DefaultBranch: "main"}
	mgr = remote.NewPublishManager(registry, remote.AuthConfig{},
		remote.WithPublisherFactory(func(_ string, _ remote.AuthConfig) (remote.Publisher, error) { return pub, nil }),
		remote.WithPublishFetcherFactory(func(_ string, _ remote.AuthConfig) (remote.Fetcher, error) { return mockFetcher, nil }),
	)
	return cfg, pub, mgr
}
