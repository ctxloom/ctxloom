package main

import (
	"context"
	"errors"
	"sync"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

var (
	errSecondOwner       = errors.New("ctxloom: the configuration is already open in this process; one config owner per process")
	errSecondCoordinator = errors.New("ctxloom: a coordinator is already constructed in this process; one coordinator per process")
)

// compose is the composition root: the one place in this binary that opens
// the config owner and constructs the runtime coordinator, and the one
// Reporter both — and everything the CLI composes from them — report
// through. Each constructor answers once; a second request is refused rather
// than minting a second owner or a second coordinator the rest of the
// process would not know about. The CLI parses flags and renders; the
// application services compose over what this hands them.
func compose(sink report.Sink) cli.Composition {
	var owners, coordinators sync.Once
	return cli.Composition{
		Reporter: sink,
		Loadout:  embeddedLoadout(),
		// The ONE composed registry: the same value the cli's own engine
		// readers resolve through, so the App and the runner cannot disagree
		// about which engines exist.
		Engines: engines.Registry(),
		// The claim store is rooted per session, so the root hands a
		// constructor: the session dir's package store, once the harp exists.
		SessionClaims: func(sessionsRoot, harp string) composite.Transport {
			return composite.ClaimCheck{Store: fsstore.PackageStore{Root: sessionsRoot, Harp: harp}}
		},
		OpenConfig: func(ctx context.Context, src config.Sources, opts ...config.Option) (*config.Owner, error) {
			owner, err := (*config.Owner)(nil), errSecondOwner
			owners.Do(func() {
				owner, err = config.Open(ctx, src, append(opts, config.WithReporter(sink))...)
			})
			return owner, err
		},
		NewCoordinator: func(app *operations.App, opts coord.Options) (*coord.Coordinator, error) {
			c, err := (*coord.Coordinator)(nil), errSecondCoordinator
			coordinators.Do(func() {
				opts.Reporter = sink
				// The production launch seam: the spawn adapter over the one
				// App, starting real runners. A caller that injected its own
				// (a test double) keeps it.
				if opts.Spawner == nil {
					opts.Spawner = spawn.New(sink, app, opts.ProjectDir, nil)
				}
				c, err = coord.New(opts)
			})
			return c, err
		},
	}
}
