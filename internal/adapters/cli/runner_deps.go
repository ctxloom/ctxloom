package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// runnerDepsFor composes the runner's ports for the one engine this process
// hosts: the two package transports (the claim store rooted at this
// process's sessions root — the mounted one inside a container), the ONE
// static writer over the home-rooted ownership record, the runner MCP
// standup as the dynamic half, the engine's configure seam over the label
// body the Launch carries, and the engine host as the driver.
func runnerDepsFor(backend agent.Backend, backendName string, host *runner.EngineHost, dynamic delivery.Dynamic) (runner.Deps, error) {
	ctxHome, err := paths.HomeConfigDir()
	if err != nil {
		return runner.Deps{}, fmt.Errorf("runner: sessions root: %w", err)
	}
	kind, ok := engines.Registry().Lookup(engine.Name(backendName))
	if !ok {
		return runner.Deps{}, fmt.Errorf("runner: no engine kind %q is composed", backendName)
	}
	records, err := operations.OwnershipRecords()
	if err != nil {
		return runner.Deps{}, err
	}
	deps := runner.Deps{
		Kind:       kind,
		Inline:     composite.Inline{Max: composite.DefaultInlineMax},
		ClaimCheck: composite.ClaimCheck{Store: fsstore.PackageStore{Root: filepath.Join(ctxHome, paths.SessionsDir)}},
		Static:     fsstatic.New(afero.NewOsFs()),
		Records:    records,
		Dynamic:    dynamic,
		Driver:     host,
		Unsetenv:   os.Unsetenv,
		EngineVersion: func(ctx context.Context) (string, error) {
			return App().ProbeEngineVersion(ctx, backendName)
		},
	}
	if c, ok := backend.(agent.Configurable); ok {
		deps.Configure = func(body map[string]any) error {
			bc, err := operations.DecodeEngineConfig(App().Engines(), backendName, body)
			if err != nil {
				return err
			}
			c.Configure(bc)
			return nil
		}
	}
	return deps, nil
}
