package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// testApp opens the process composition over the real reader with opts
// (a pinned app dir, an injected fs) and no remote or companion readers.
func testApp(t *testing.T, opts ...configload.Option) *App {
	t.Helper()
	src, err := configload.New(nil, nil, opts...)
	require.NoError(t, err)
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	return OpenedApp(owner, Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims})
}

// fixtureSources is a config.Sources whose every Read is the same fixture
// value, with the reader set ComposeSources wires: project, the lockfile's
// remote readers and every discovered companion's loadout — or, when a test
// hands it loadouts directly, a companion reader over exactly those (no
// discovery), for a fixture companion under a name discovery would never
// list.
type fixtureSources struct {
	cfg      *config.Config
	loadouts []bundles.CompanionLoadout
}

func (s fixtureSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s fixtureSources) ReadTarget(context.Context) (*config.Config, error) {
	return s.cfg, nil
}

func (s fixtureSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.TrustRoot()
	readers := []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
	}
	readers = append(readers, RemoteBundleReaders(cfg)...)
	if s.loadouts != nil {
		probe := func(context.Context) (bundles.CompanionProbe, error) {
			return bundles.CompanionProbe{Loadouts: s.loadouts}, nil
		}
		return append(readers, bundles.NewCompanionReader(probe, bundles.WithTrustRoot(root))), nil
	}
	return append(readers, companions.Prober{}.ReaderSource()(cfg)...), nil
}

func (s fixtureSources) TrustRoot(context.Context, *config.Config) (trust.TrustRoot, error) {
	return trust.NoSigners{}, nil
}

// fixtureApp opens the process composition over a fixture Config.
func fixtureApp(t *testing.T, cfg *config.Config) *App {
	t.Helper()
	return fixtureAppWith(t, cfg, nil)
}

// fixtureAppWith is fixtureApp with the companion loadouts the generation
// reads handed in directly (see fixtureSources.loadouts).
func fixtureAppWith(t *testing.T, cfg *config.Config, loadouts []bundles.CompanionLoadout) *App {
	t.Helper()
	owner, err := config.Open(context.Background(), fixtureSources{cfg: cfg, loadouts: loadouts})
	require.NoError(t, err)
	return OpenedApp(owner, Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims})
}

// published returns the fixture as the generation the process would hold:
// its catalog resolved through the production reader set.
func published(t *testing.T, cfg *config.Config) *config.Config {
	t.Helper()
	snap, err := fixtureApp(t, cfg).Snapshot(context.Background())
	require.NoError(t, err)
	return snap.Config
}

// publishedWith is published over a generation that read exactly the given
// companion loadouts.
func publishedWith(t *testing.T, cfg *config.Config, loadouts ...bundles.CompanionLoadout) *config.Config {
	t.Helper()
	snap, err := fixtureAppWith(t, cfg, loadouts).Snapshot(context.Background())
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

// ctxloomOwnLoadout is ctxloom's OWN companion loadout as the self-probe
// reads it — the repo's real cmd/ctxloom/loadout.yaml, not a stand-in — so a
// fixture that must carry what every session gets on ctxloom's behalf (its
// MCP server entry, its always-on guidance) pins the shipped content.
func ctxloomOwnLoadout(t *testing.T) bundles.CompanionLoadout {
	t.Helper()
	// The test binary's working directory before the sandbox moves it is
	// this package's directory, three levels below the repo root (-trimpath
	// strips runtime.Caller's path, so the directory is the only anchor).
	require.NotEmpty(t, packageDirAtStart)
	doc, err := os.ReadFile(filepath.Join(packageDirAtStart, "..", "..", "..", "cmd", "ctxloom", "loadout.yaml"))
	require.NoError(t, err)
	return bundles.CompanionLoadout{Bin: "ctxloom", Path: "/opt/build/ctxloom", Document: doc, Self: true}
}

var packageDirAtStart, _ = os.Getwd()

// withCtxloomLoadout publishes cfg over a generation that read ctxloom's own
// loadout — the generation a real ctxloom holds.
func withCtxloomLoadout(t *testing.T, cfg *config.Config) *config.Config {
	t.Helper()
	return publishedWith(t, cfg, ctxloomOwnLoadout(t))
}

// onDiskRoot is the trust root a generation read over appDir holds: the
// embedded signers plus the user's and appDir's allowed_signers, minus any
// distrusted — built by configload exactly as a process builds it.
func onDiskRoot(t *testing.T, appDir string) trust.TrustRoot {
	t.Helper()
	cfg, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	return cfg.TrustRoot()
}

// withOnDiskRoot rebinds cfg's trust root to the one a generation read over
// appDir holds (onDiskRoot): for a fixture whose test trusts a publisher by
// writing its allowed_signers, as a user would.
func withOnDiskRoot(t *testing.T, cfg *config.Config, appDir string) *config.Config {
	t.Helper()
	cfg.BindTrustRootForTesting(onDiskRoot(t, appDir), cfg.SignatureCheckDisabled())
	return cfg
}

// testLaunchFacts is the launch facts a composition root would hand: the
// process registry (so an enginefixture.Install stands in) and the session
// dir's claim store, under the strict default.
func testLaunchFacts() LaunchFacts {
	f, err := NewLaunchFacts(engines.Registry()).Claims(fsstore.SessionClaims).Build()
	if err != nil {
		panic(err)
	}
	return f
}
