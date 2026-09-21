package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// resetApp drops the process composition so the next command composes it
// afresh from the test's environment and working directory: the per-test
// isolation the memoized loader's Invalidate used to provide. It also runs
// at every Isolate/ChangeDir, so a test that re-roots the process never
// inherits the composition a previous test opened.
func resetApp() { theApp = nil }

// testComposition stands in for cmd/ctxloom's root in this package's tests:
// a command driven without Run composes over it. The constructors are the
// bare ones — a test process opens many owners and coordinators.
func testComposition() Composition {
	return Composition{
		Reporter:       strictness.Sink("ctxloom"),
		OpenConfig:     config.Open,
		NewCoordinator: func(_ *operations.App, opts coord.Options) (*coord.Coordinator, error) { return coord.New(opts) },
	}
}

func init() {
	theComposition = testComposition()
	taskstest.RegisterIsolateHook(resetApp)
}

// testApp opens a composition over the real reader with opts and installs
// it as the process's for the test's duration.
func testApp(t *testing.T, opts ...configload.Option) *operations.App {
	t.Helper()
	src, err := operations.ComposeSources(operations.Compose{Options: opts})
	require.NoError(t, err)
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	app := operations.OpenedApp(owner)
	t.Cleanup(SetAppForTesting(app))
	return app
}

// probeSources publishes a fixture with the project and builtin readers plus
// a companion reader over the given probe, under the fixture's own gate.
type probeSources struct {
	cfg   *config.Config
	probe bundles.CompanionProber
}

func (s probeSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s probeSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.TrustRoot()
	return []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
		bundles.NewCompanionReader(s.probe, bundles.WithTrustRoot(root)),
	}, nil
}

func (s probeSources) TrustPorts(context.Context, *config.Config) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords, error) {
	root, records, retraction := compositetest.Ports()
	return root, records, retraction, nil
}

// withCompanionProbe returns cfg as the generation a process would hold when
// companion discovery answers with probe — what the composition root's
// companion reader would have read.
func withCompanionProbe(t *testing.T, cfg *config.Config, probe bundles.CompanionProber) *config.Config {
	t.Helper()
	owner, err := config.Open(context.Background(), probeSources{cfg: cfg, probe: probe})
	require.NoError(t, err)
	return owner.Current().Config
}

// chdir is t.Chdir plus the composition reset a re-rooted process needs.
func chdir(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
	resetApp()
	t.Cleanup(resetApp)
}
