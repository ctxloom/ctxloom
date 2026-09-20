package main

import (
	"context"
	"errors"
	"sync"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
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
		OpenConfig: func(ctx context.Context, src config.Sources, opts ...config.Option) (*config.Owner, error) {
			owner, err := (*config.Owner)(nil), errSecondOwner
			owners.Do(func() {
				owner, err = config.Open(ctx, src, append(opts, config.WithReporter(sink))...)
			})
			return owner, err
		},
		NewCoordinator: func(opts coord.Options) (*coord.Coordinator, error) {
			c, err := (*coord.Coordinator)(nil), errSecondCoordinator
			coordinators.Do(func() {
				opts.Reporter = sink
				c, err = coord.New(opts)
			})
			return c, err
		},
	}
}
