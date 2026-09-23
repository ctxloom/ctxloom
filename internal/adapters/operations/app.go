package operations

import (
	"context"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"

	"github.com/spf13/pflag"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// App is the process's composition: the ONE config.Owner, opened on first
// need from the Sources the composition root built, and the per-invocation
// switches every operation reads alongside it. Every consumer takes a
// *config.Snapshot (or the App) as a parameter; nothing re-reads the world.
type App struct {
	// NoCompanions is the --no-companions / CTXLOOM_NO_COMPANIONS switch: no
	// companion binary is executed and none contributes to a generation.
	NoCompanions bool
	// SelfLoadout mirrors Compose.SelfLoadout for the probers this App hands out.
	SelfLoadout func() string
	// Strictness is the posture this composition runs under — the program
	// its findings render as and whether --degraded waives the ordinary
	// ones. A value: two Apps in one process may differ.
	Strictness strictness.Mode
	// Reporter is the sink the composition root chose; every component this
	// App composes reports through it.
	Reporter report.Sink

	src     config.Sources
	open    ConfigOpener
	engines engine.Registry
	claims  launch.SessionClaims

	proberOnce sync.Once
	prober     *engineversion.Prober
	once       sync.Once
	mu         sync.Mutex
	opened     bool
	owner      *config.Owner
	err        error
}

// Compose describes one invocation to ComposeSources: the parsed flag set
// (for --config-set), the environment (os.Environ() in production; nil is no
// environment), and the companion switch.
type Compose struct {
	Flags        *pflag.FlagSet
	Environ      []string
	NoCompanions bool
	// SelfLoadout arms ctxloom's self-probe (companions.Prober.Self): the
	// running binary's path resolver, set only for a process composed with
	// an embedded loadout to emit; nil is unarmed.
	SelfLoadout func() string
	// Options extend the Sources: a pinned app dir, an injected filesystem.
	Options []configload.Option
}

// ComposeSources builds the process's config.Sources: the file/env/flag
// reader with the remote readers behind the lockfile, the companion prober,
// the profile-ref canonicalizer and the bundle version resolver wired in.
// The ONE place these adapters meet. The trust ports are the Sources' own
// (configload.Sources.TrustPorts): nothing here can leave a generation
// ungated.
// A flag or env override that cannot be bound is returned alongside a
// usable Sources; the root degrades it to a warning.
func ComposeSources(c Compose) (config.Sources, error) {
	prober := companions.Prober{Disabled: c.NoCompanions, Self: c.SelfLoadout}
	opts := []configload.Option{
		configload.WithProfileRefCanonicalizer(func(shell *config.Config, ref string) string {
			return remote.CanonicalizeProfileShortRef(ref, shell.ProfileRemoteURLResolver())
		}),
		configload.WithVersionResolver(BundleVersionResolver),
		configload.WithReaderSource(RemoteBundleReaders),
		configload.WithReaderSource(prober.ReaderSource()),
	}
	opts = append(opts, c.Options...)
	return configload.New(c.Flags, c.Environ, opts...)
}

// ConfigOpener opens the process's one config owner. The composition root
// (cmd/*) supplies it, so config.Open is called only there — the
// one-mint-one-owner rule.
type ConfigOpener func(ctx context.Context, src config.Sources, opts ...config.Option) (*config.Owner, error)

// Handed is what the composition root (cmd/*) hands the App: the config
// opener, the one Reporter, the shipped engine registry every by-name
// resolution reads, and the per-session claim-store constructor launches
// root at their minted session.
type Handed struct {
	Open          ConfigOpener
	Reporter      report.Sink
	Engines       engine.Registry
	SessionClaims launch.SessionClaims
}

// NewApp holds src as the process's sources; the owner opens on the first
// Owner/Snapshot/Config call, so a command that never reads configuration
// never reads the files either.
func NewApp(src config.Sources, noCompanions bool, selfLoadout func() string, mode strictness.Mode, h Handed) *App {
	return &App{NoCompanions: noCompanions, SelfLoadout: selfLoadout, Strictness: mode, Reporter: h.Reporter, src: src, open: h.Open, engines: h.Engines, claims: h.SessionClaims}
}

// OpenedApp wraps an owner a test already opened, with what a composition
// root would have handed it.
func OpenedApp(owner *config.Owner, h Handed) *App {
	a := &App{owner: owner, opened: true, Reporter: h.Reporter, open: h.Open, engines: h.Engines, claims: h.SessionClaims}
	a.once.Do(func() {})
	return a
}

// Prober is the companion-probing adapter for this invocation, carrying its
// companion switch.
func (a *App) Prober() companions.Prober {
	return companions.Prober{Disabled: a.NoCompanions, Self: a.SelfLoadout}
}

// Engines is the engine registry the composition root handed this App: the
// one every by-name engine resolution in operations reads.
func (a *App) Engines() engine.Registry { return a.engines }

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
		a.owner, a.err = a.open(ctx, a.src, config.WithEngines(a.engines), config.WithReporter(a.Reporter))
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
