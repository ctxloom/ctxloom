package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// fixtureSources is a config.Sources whose every Read is the same fixture
// value, with the project and builtin readers: what a test means by "this
// configuration, for the whole process".
type fixtureSources struct{ cfg *config.Config }

func (s fixtureSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s fixtureSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.Trust().Root()
	return []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
	}, nil
}

func (s fixtureSources) TrustPorts(context.Context, *config.Config) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords, error) {
	root, records, retraction := compositetest.Ports()
	return root, records, retraction, nil
}

// fixtureApp opens the process composition over a fixture Config.
func fixtureApp(t *testing.T, cfg *config.Config) *operations.App {
	t.Helper()
	owner, err := config.Open(context.Background(), fixtureSources{cfg: cfg})
	require.NoError(t, err)
	return operations.OpenedApp(owner, operations.Handed{Engines: engines.Registry()})
}
