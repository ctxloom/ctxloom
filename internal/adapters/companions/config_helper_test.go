package companions

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// companionSources is a config.Sources over a fixture whose readers are what
// the composition root wires — project, builtin and every discovered
// companion's loadout — with the test's own gate.
type companionSources struct {
	cfg  *config.Config
	gate bundles.Authorizer
}

func (s companionSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s companionSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.TrustRoot()
	readers := []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
		bundles.NewBuiltinReader(bundles.WithTrustRoot(root)),
	}
	return append(readers, Prober{}.ReaderSource()(cfg)...), nil
}

func (s companionSources) TrustPorts(context.Context, *config.Config) (bundles.Authorizer, error) {
	if s.gate == nil {
		return bundles.AdmitAll(), nil
	}
	return s.gate, nil
}

// companionConfig publishes f through a real config.Owner whose generation
// carries the companion reader and gate, and returns its Config.
func companionConfig(t *testing.T, f config.Fixture, gate bundles.Authorizer) *config.Config {
	t.Helper()
	owner, err := config.Open(context.Background(), companionSources{cfg: config.NewFixture(f), gate: gate})
	require.NoError(t, err)
	return owner.Current().Config
}

// testAuthorizer admits everything or withholds everything as pending.
func testAuthorizer(admit bool) bundles.Authorizer {
	return bundles.AuthorizerFunc(func(bundles.Exposure) bundles.Verdict {
		if admit {
			return bundles.Verdict{Allow: true, Reason: bundles.ReasonLocal}
		}
		return bundles.Verdict{Reason: bundles.ReasonPending}
	})
}
