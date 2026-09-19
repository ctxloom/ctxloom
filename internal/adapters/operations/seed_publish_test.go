package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedReader presents authored bundle VALUES as what they are — project
// bundles on a filesystem — as a reader over them.
//
// There is deliberately no exported way to hand a loader a finished bundle with
// a provenance attached: a constructor that took one would let any caller mint
// local-context content. So a test supplies BYTES and lets the project reader
// establish the facts, which is also what a real project does.
func seedReader(t *testing.T, seed map[string]*bundles.Bundle) bundles.Reader {
	t.Helper()
	fsys := afero.NewMemMapFs()
	// seedRoot is the SEARCH root the reader is given; it expands that into the
	// format root beneath it and never reads the root itself, so the bytes go
	// there rather than at seedRoot directly.
	const seedRoot = "/seed"
	bundlesRoot := paths.BundlesLayoutRoot(seedRoot, paths.LayoutV2)
	for name, b := range seed {
		data, err := yaml.Marshal(b)
		require.NoError(t, err)
		testsupport.WriteFile(t, fsys, filepath.Join(bundlesRoot, name+".yaml"), data, 0o644)
	}
	return bundles.NewProjectReader(fsys, []string{seedRoot})
}

// withSeed publishes cfg through a config.Owner whose readers include one
// over seed, returning the generation's Config.
func withSeed(t *testing.T, cfg *config.Config, seed map[string]*bundles.Bundle) *config.Config {
	t.Helper()
	return publish(t, cfg, seededSources{extra: seedReader(t, seed)})
}

// withCompanions publishes cfg through a config.Owner whose readers include
// every discovered (faked) companion's loadout, as the composition root's
// Sources would.
func withCompanions(t *testing.T, cfg *config.Config) *config.Config {
	t.Helper()
	return publish(t, cfg, seededSources{companions: true})
}

// withSeedAndCompanions is withSeed plus the companion reader.
func withSeedAndCompanions(t *testing.T, cfg *config.Config, seed map[string]*bundles.Bundle) *config.Config {
	t.Helper()
	return publish(t, cfg, seededSources{companions: true, extra: seedReader(t, seed)})
}

func publish(t *testing.T, cfg *config.Config, src seededSources) *config.Config {
	t.Helper()
	src.cfg = cfg
	// The gate the fixture already carries survives publication: the Owner
	// binds a Trust over the fake ports, and a test that stated a gate of its
	// own (BindTrustForTesting) meant that one.
	carried := cfg.Trust()
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	out := owner.Current().Config
	if carried.Authorizer() != nil {
		out.BindTrustForTesting(carried)
	} else {
		out.BindTrustForTesting(compositetest.Trust())
	}
	return out
}

// seededSources publishes a fixture with the project and builtin readers,
// optionally the companion reader and one extra; the gate is the fixture's
// own (publish), else a Trust that admits by locality.
type seededSources struct {
	cfg        *config.Config
	extra      bundles.Reader
	companions bool
}

func (s seededSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s seededSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.TrustRoot()
	readers := []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
		bundles.NewBuiltinReader(bundles.WithTrustRoot(root)),
	}
	if s.companions {
		readers = append(readers, companions.Prober{}.ReaderSource()(cfg)...)
	}
	if s.extra != nil {
		readers = append(readers, s.extra)
	}
	return readers, nil
}

func (s seededSources) TrustPorts(context.Context, *config.Config) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords, error) {
	root, records, retraction := compositetest.Ports()
	return root, records, retraction, nil
}

// withResolver returns cfg's value with the pinned-version resolver bound,
// as the reader binds it for a real generation.
func withResolver(cfg *config.Config, r bundles.BundleVersionResolver) *config.Config {
	f := cfg.ToFixture()
	f.VersionResolver = r
	out := gatedFixture(f)
	if fs := cfg.FS(); fs != nil {
		out.SetFS(fs)
	}
	out.BindTrustForTesting(cfg.Trust())
	return out
}
