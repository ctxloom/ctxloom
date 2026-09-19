package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
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

func (s fixtureSources) TrustPorts(context.Context, *config.Config) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords, error) {
	root, records, retraction := compositetest.Ports()
	return root, records, retraction, nil
}

// fixtureApp opens the process composition over a fixture Config.
func fixtureApp(t *testing.T, cfg *config.Config) *App {
	t.Helper()
	// The gate the fixture already carries survives publication: the Owner
	// binds a Trust over the fake ports, and a test that stated a gate of its
	// own (BindTrustForTesting) meant that one.
	carried := cfg.Trust()
	owner, err := config.Open(context.Background(), fixtureSources{cfg: cfg})
	require.NoError(t, err)
	if carried.Authorizer() != nil {
		owner.Current().Config.BindTrustForTesting(carried)
	}
	return OpenedApp(owner)
}

// realGated binds the gate built over cfg's PRODUCTION adapters — the
// on-disk approvals stores and lockfile — for a test that writes real
// records and expects the gate to read them.
func realGated(cfg *config.Config) *config.Config {
	cfg.BindTrustForTesting(NewExecutableTrustGate(cfg).Trust())
	return cfg
}

// gatedFixture is config.NewFixture with a gate bound that admits by
// locality and withholds what travelled (compositetest.Trust): a fixture that
// exercises an executable surface must state its gate, and this is what a
// test about anything other than trust means.
func gatedFixture(f config.Fixture) *config.Config {
	cfg := config.NewFixture(f)
	cfg.BindTrustForTesting(compositetest.Trust())
	return cfg
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
	// A test's loader hands back a bare value; the generation it stands in
	// for would carry its gate, so state the admitting one unless the loader
	// bound its own.
	if cfg.ExecutableTrustGate() == nil {
		cfg.BindTrustForTesting(compositetest.Trust())
	}
	return cfg
}
