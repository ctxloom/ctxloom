package companions

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// companionSources is a config.Sources over a fixture whose readers are what
// the composition root wires — project, builtin and every discovered
// companion's loadout.
type companionSources struct {
	cfg *config.Config
}

func (s companionSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s companionSources) ReadTarget(context.Context) (*config.Config, error) {
	return s.cfg, nil
}

func (s companionSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.TrustRoot()
	readers := []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
	}
	return append(readers, Prober{}.ReaderSource()(cfg)...), nil
}

func (s companionSources) TrustRoot(context.Context, *config.Config) (trust.TrustRoot, error) {
	return trust.NoSigners{}, nil
}

// companionConfig publishes f through a real config.Owner whose generation
// carries the companion reader, and returns its Config.
func companionConfig(t *testing.T, f config.Fixture) *config.Config {
	t.Helper()
	owner, err := config.Open(context.Background(), companionSources{cfg: config.NewFixture(f)})
	require.NoError(t, err)
	return owner.Current().Config
}
