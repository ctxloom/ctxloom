package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
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
	require.NoError(t, os.MkdirAll(paths.LocalBundlesPath(appDir), 0o755))
	cfg = config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	require.NoError(t, os.WriteFile(filepath.Join(appDir, "remotes.yaml"), []byte(`default: personal
remotes:
  personal:
    url: https://github.com/example/personal-bundles
    version: v1
`), 0o644))

	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: "for-push",
		Fragments: map[string]operations.BundleFragmentInput{
			"intro": {Content: "hello", NoDistill: true},
		},
	})
	require.NoError(t, err)

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

	main, ok := pub.files[".ctxloom/content/bundles/for-push.yaml"]
	require.True(t, ok)
	sig, ok := pub.files[".ctxloom/content/bundles/for-push.yaml.sig"]
	require.True(t, ok, "--sign must publish a .sig sibling")

	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"me@example.com"},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})
	principal, verr := signing.VerifyPublisher(main, sig, root, time.Now())
	require.NoError(t, verr)
	assert.Equal(t, "me@example.com", principal)
}

func TestPushBundleCfg_NoFlagsMeansUnsignedByDefault(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)

	cmd, _ := testCmd()
	err := pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", false, false)
	require.NoError(t, err)

	_, ok := pub.files[".ctxloom/content/bundles/for-push.yaml.sig"]
	assert.False(t, ok, "no --sign and sign.default unset must never sign")
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
	_, ok := pub.files[".ctxloom/content/bundles/for-push.yaml.sig"]
	assert.True(t, ok, "sign.default: true must sign by default")
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
	_, ok := pub.files[".ctxloom/content/bundles/for-push.yaml.sig"]
	assert.False(t, ok, "--no-sign must suppress sign.default")
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
