package operations

import (
	"context"
	"sync"

	"github.com/spf13/pflag"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// App is the process's composition: the ONE config.Owner, opened on first
// need from the Sources the composition root built, and the per-invocation
// switches every operation reads alongside it. Every consumer takes a
// *config.Snapshot (or the App) as a parameter; nothing re-reads the world.
type App struct {
	// NoCompanions is the --no-companions / CTXLOOM_NO_COMPANIONS switch: no
	// companion binary is executed and none contributes to a generation.
	NoCompanions bool

	src    config.Sources
	once   sync.Once
	mu     sync.Mutex
	opened bool
	owner  *config.Owner
	err    error
}

// Compose describes one invocation to ComposeSources: the parsed flag set
// (for --config-set), the environment (os.Environ() in production; nil is no
// environment), and the companion switch.
type Compose struct {
	Flags        *pflag.FlagSet
	Environ      []string
	NoCompanions bool
	// Options extend the Sources: a pinned app dir, an injected filesystem.
	Options []configload.Option
}

// ComposeSources builds the process's config.Sources: the file/env/flag
// reader with the remote readers behind the lockfile, the companion prober,
// the profile-ref canonicalizer, the bundle version resolver and the
// executable trust gate wired in. The ONE place these adapters meet.
// A flag or env override that cannot be bound is returned alongside a
// usable Sources; the root degrades it to a warning.
func ComposeSources(c Compose) (config.Sources, error) {
	prober := companions.Prober{Disabled: c.NoCompanions}
	opts := []configload.Option{
		configload.WithProfileRefCanonicalizer(func(shell *config.Config, ref string) string {
			return remote.CanonicalizeProfileShortRef(ref, shell.ProfileRemoteURLResolver())
		}),
		configload.WithVersionResolver(BundleVersionResolver),
		configload.WithReaderSource(RemoteBundleReaders),
		configload.WithReaderSource(prober.ReaderSource()),
		configload.WithTrustGate(func(cfg *config.Config) bundles.Authorizer {
			return NewExecutableTrustGate(cfg).Authorizer()
		}),
	}
	opts = append(opts, c.Options...)
	return configload.New(c.Flags, c.Environ, opts...)
}

// NewApp holds src as the process's sources; the owner opens on the first
// Owner/Snapshot/Config call, so a command that never reads configuration
// never reads the files either.
func NewApp(src config.Sources, noCompanions bool) *App {
	return &App{NoCompanions: noCompanions, src: src}
}

// OpenedApp wraps an owner a test already opened.
func OpenedApp(owner *config.Owner) *App {
	a := &App{owner: owner, opened: true}
	a.once.Do(func() {})
	return a
}

// Prober is the companion-probing adapter for this invocation, carrying its
// companion switch.
func (a *App) Prober() companions.Prober { return companions.Prober{Disabled: a.NoCompanions} }

// Opened reports whether the owner has been opened: a composition may be
// replaced (init pinning its target directory) only before that.
func (a *App) Opened() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opened
}

// Owner returns the process's one config.Owner, opening it on first call. A
// configuration that cannot be read yields the refusal every later call
// repeats: there is no owner to hand out.
func (a *App) Owner(ctx context.Context) (*config.Owner, error) {
	a.once.Do(func() {
		a.mu.Lock()
		a.opened = true
		a.mu.Unlock()
		a.owner, a.err = config.Open(ctx, a.src)
	})
	return a.owner, a.err
}

// Snapshot is the published generation.
func (a *App) Snapshot(ctx context.Context) (*config.Snapshot, error) {
	owner, err := a.Owner(ctx)
	if err != nil {
		return nil, err
	}
	return owner.Current(), nil
}

// Config is the published generation's value.
func (a *App) Config(ctx context.Context) (*config.Config, error) {
	snap, err := a.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Config, nil
}

// Reload reads the sources once more and publishes the next generation: the
// call after a pull, after a scaffold, and once per spawn.
func (a *App) Reload(ctx context.Context) (*config.Snapshot, error) {
	owner, err := a.Owner(ctx)
	if err != nil {
		return nil, err
	}
	return owner.Reload(ctx)
}

// Update writes fn's changes through to the config file and publishes the
// next generation.
func (a *App) Update(ctx context.Context, fn func(*config.Draft) error) (*config.Snapshot, error) {
	owner, err := a.Owner(ctx)
	if err != nil {
		return nil, err
	}
	return owner.Update(ctx, fn)
}
