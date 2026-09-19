package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// testApp opens the process composition over the real reader with opts
// (a pinned app dir, an injected fs) and no remote or companion readers.
func testApp(t *testing.T, opts ...configload.Option) *App {
	t.Helper()
	src, err := configload.New(nil, nil, opts...)
	require.NoError(t, err)
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	return OpenedApp(owner)
}

// fixtureSources is a config.Sources whose every Read is the same fixture
// value, with the reader set ComposeSources wires: project, builtin, the
// lockfile's remote readers and every discovered companion's loadout.
type fixtureSources struct{ cfg *config.Config }

func (s fixtureSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s fixtureSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.TrustRoot()
	readers := []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
		bundles.NewBuiltinReader(bundles.WithTrustRoot(root)),
	}
	readers = append(readers, RemoteBundleReaders(cfg)...)
	return append(readers, companions.Prober{}.ReaderSource()(cfg)...), nil
}

func (s fixtureSources) TrustPorts(_ context.Context, cfg *config.Config) (bundles.Authorizer, error) {
	return cfg.ExecutableTrustGate(), nil
}

// fixtureApp opens the process composition over a fixture Config.
func fixtureApp(t *testing.T, cfg *config.Config) *App {
	t.Helper()
	owner, err := config.Open(context.Background(), fixtureSources{cfg: cfg})
	require.NoError(t, err)
	return OpenedApp(owner)
}

// published returns the fixture as the generation the process would hold:
// its catalog resolved through the production reader set.
func published(t *testing.T, cfg *config.Config) *config.Config {
	t.Helper()
	snap, err := fixtureApp(t, cfg).Snapshot(context.Background())
	require.NoError(t, err)
	return snap.Config
}

// loaded unwraps a config loader's result for a request field.
func loaded(t *testing.T, load func() (*config.Config, error)) *config.Config {
	t.Helper()
	cfg, err := load()
	require.NoError(t, err)
	return cfg
}
