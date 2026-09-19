package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// stubPublisher is a minimal remote.Publisher fake that records every
// CreateOrUpdateFile call by path — enough to see whether a ".sig" sibling
// was published alongside the main bundle file.
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

func pushSignTestSetup(t *testing.T) (cfg *config.Config, pub *stubPublisher, mgr *remote.PublishManager) {
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

	// Hand-written rather than CreateBundle(Tree: true): CreateBundle's own
	// tree path refuses to author a zero-item tree (convert.Convert is a
	// no-op for one, so createTreeBundle treats it as a failed write), even
	// though a version-only envelope is a perfectly valid bundle to READ
	// (Bundle.declaresNothing requires no version AND no items) — so this
	// writes the manifest directly, the same "version-only skeleton" shape
	// CreateBundle itself writes for a single-file bundle.
	treeDir := filepath.Join(authoredV1(appDir), "for-push")
	require.NoError(t, os.MkdirAll(treeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "bundle.yaml"), []byte("version: \"1.0.0\"\n"), 0o644))

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

func TestPushBundleCfg_SignFlagPublishesVerifiableSig(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, signer := discovererWithSoleAgentIdentity(t)

	cmd, out := testCmd()
	err := pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", true, false)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Signed: yes")

	_, ok := pub.files[".ctxloom/content/bundles/v2/for-push/bundle.yaml"]
	require.True(t, ok)
	require.True(t, publishedSigned(pub), "--sign must publish the tree's .sigs/ entry")
	_ = signer
}

// publishedSigned reports whether a .sigs/ entry of the for-push tree
// reached the fake remote.
func publishedSigned(pub *stubPublisher) bool {
	for path := range pub.files {
		if strings.HasPrefix(path, ".ctxloom/content/bundles/v2/for-push/"+content.SigDirName+"/") {
			return true
		}
	}
	return false
}

func TestPushBundleCfg_NoFlagsMeansUnsignedByDefault(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)

	cmd, _ := testCmd()
	err := pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", false, false)
	require.NoError(t, err)

	assert.False(t, publishedSigned(pub), "no --sign and sign.default unset must never sign")
}

func TestPushBundleCfg_SignDefaultConfigSignsUnlessNoSign(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	{
		f := cfg.ToFixture()
		f.Settings.Sign = &config.SignConfig{Default: true}
		cfg = config.NewFixture(f)
	}
	discoverer, _ := discovererWithSoleAgentIdentity(t)

	cmd, _ := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", false, false))
	assert.True(t, publishedSigned(pub), "sign.default: true must sign by default")
}

func TestPushBundleCfg_NoSignOverridesSignDefault(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	{
		f := cfg.ToFixture()
		f.Settings.Sign = &config.SignConfig{Default: true}
		cfg = config.NewFixture(f)
	}
	discoverer, _ := discovererWithSoleAgentIdentity(t)

	cmd, _ := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", false, true))
	assert.False(t, publishedSigned(pub), "--no-sign must suppress sign.default")
}

func TestPushBundleCfg_SignAndNoSignTogetherIsUsageError(t *testing.T) {
	cfg, _, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	cmd, _ := testCmd()
	err := pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", true, true)
	require.Error(t, err)
}

// TestPushBundleCfg_SignRequestedButNoKeyIsHardError is the push-path
// version of the same red line runSign already proves: --sign that cannot
// find a key must error, and MUST NOT fall back to an unsigned publish.
func TestPushBundleCfg_SignRequestedButNoKeyIsHardError(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	noKeyDiscoverer := &agentkey.Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return nil, assert.AnError },
		ReadFile:  func(path string) ([]byte, error) { return nil, assert.AnError },
	}

	cmd, _ := testCmd()
	err := pushBundleCfg(cmd, cfg, noKeyDiscoverer, mgr, "for-push", "", false, "", true, false)
	require.Error(t, err)
	var noKeyErr *agentkey.NoKeyError
	require.ErrorAs(t, err, &noKeyErr)

	assert.Empty(t, pub.files, "no file may be published when --sign was requested and no key was found")
}
