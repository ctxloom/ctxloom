package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// lockFileMode and lockDirMode are the modes the config-update lock's
// sidecar and its parent directory are created with, before umask — not
// group- or world-WRITABLE, matching every other lock site in this project.
const (
	lockFileMode = 0o644
	lockDirMode  = 0o755
)

// Draft is the mutable view an Owner.Update transaction hands fn: every
// PERSISTED Config field (the set configDoc carries), exported so the write
// sites in adapters/operations mutate it directly. Runtime-only facts
// (AppPaths, Warnings, PendingUpgrade, ...) are absent: they describe WHERE a
// config came from, not values a write edits. A plain alias for configDoc —
// the same exported mirror of the persisted fields, for the same reason
// (yaml reflection and the caller both need exported fields).
type Draft = configDoc

// Snapshot is one GENERATION of everything that derives from the config files
// and the lockfile: the Config value, the bundle Catalog resolved from the
// sources' readers, and the Trust built from the sources' trust ports. Nothing
// in it re-reads the world; a consumer that holds one sees one state for the
// whole operation it threads it through.
type Snapshot struct {
	Config  *Config
	Catalog bundles.Catalog
	// Trust is built per generation, so a retraction that lands in the
	// lockfile is in force on the next Reload and never retroactively.
	Trust      composite.Trust
	Generation uint64
	LoadedAt   time.Time
	Warnings   []Warning
}

// Sources is the port the reading half of configuration (adapters/configload)
// implements. Built ONCE at the composition root from the process's flags
// and environment; there is no process-global override funnel.
type Sources interface {
	// Read treats an ABSENT layer as the shipped default and refuses only a
	// PRESENT unparsable one, so init on a machine with no config starts.
	Read(ctx context.Context) (*Config, []Warning, error)
	// Readers are the bundle sources a generation's Catalog is resolved from.
	Readers(ctx context.Context, cfg *Config) ([]bundles.Reader, error)
	// TrustPorts is the executable gate for cfg's generation: the value
	// Snapshot.Trust wraps. Until composite carries its own trust-record
	// types this is the bundles.Authorizer the adapters already produce.
	TrustPorts(ctx context.Context, cfg *Config) (bundles.Authorizer, error)
}

// Owner is the one owner of the loaded configuration in a process (the
// originator; the runner has none). Exactly one exists, constructed at the
// composition root by Open, reaching every consumer as a *Snapshot parameter.
type Owner struct {
	src     Sources
	current atomic.Pointer[Snapshot]
	gen     atomic.Uint64
	// writeMu serializes generation builds (Reload, Update): generation
	// numbers are then monotonic with publication order, and an Update's
	// read-modify-write cannot interleave with a concurrent Reload.
	writeMu sync.Mutex
}

// Open constructs the owner and publishes its first generation. A process
// whose configuration cannot be read gets no owner: the error is the one
// Sources.Read returned.
func Open(ctx context.Context, src Sources) (*Owner, error) {
	o := &Owner{src: src}
	if _, err := o.Reload(ctx); err != nil {
		return nil, err
	}
	return o, nil
}

// Current returns the published generation. Never nil after Open.
func (o *Owner) Current() *Snapshot { return o.current.Load() }

// Reload reads the sources once and publishes the result as a new generation.
// It is the only re-read: after a pull, after a scaffold, and once per spawn.
func (o *Owner) Reload(ctx context.Context) (*Snapshot, error) {
	o.writeMu.Lock()
	defer o.writeMu.Unlock()
	return o.reloadLocked(ctx)
}

func (o *Owner) reloadLocked(ctx context.Context) (*Snapshot, error) {
	cfg, warnings, err := o.src.Read(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := o.build(ctx, cfg, warnings)
	if err != nil {
		return nil, err
	}
	o.current.Store(snap)
	return snap, nil
}

// build resolves the generation's Catalog and Trust from the sources and
// binds both to cfg, so a consumer that reaches this generation through its
// *Config sees the same catalog and gate the Snapshot carries.
func (o *Owner) build(ctx context.Context, cfg *Config, warnings []Warning) (*Snapshot, error) {
	readers, err := o.src.Readers(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("config: resolving bundle sources: %w", err)
	}
	cat := bundles.Resolve(ctx, readers...)
	auth, err := o.src.TrustPorts(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("config: resolving trust: %w", err)
	}
	if auth == nil {
		auth = bundles.AdmitAll()
	}
	trust := composite.FromAuthorizer(auth)
	cfg.bindGeneration(cat, auth)
	return &Snapshot{
		Config:     cfg,
		Catalog:    cat,
		Trust:      trust,
		Generation: o.gen.Add(1),
		LoadedAt:   time.Now(),
		Warnings:   warnings,
	}, nil
}

// Update runs fn as ONE serialized write transaction against the config file
// the current generation was read from, then publishes the result as the
// next generation: it takes the cross-process file lock, re-reads the sources
// fresh under it, hands fn a Draft of that read, writes fn's changes through,
// and reloads. fn returning an error abandons the transaction — nothing is
// written and the published generation is untouched.
func (o *Owner) Update(ctx context.Context, fn func(*Draft) error) (*Snapshot, error) {
	o.writeMu.Lock()
	defer o.writeMu.Unlock()

	cur := o.current.Load()
	if cur == nil || cur.Config == nil {
		return nil, fmt.Errorf("config: Update before Open published a generation")
	}
	configPath, err := cur.Config.GetConfigFilePath()
	if err != nil {
		return nil, fmt.Errorf("config: resolve config path for update: %w", err)
	}
	fs := cur.Config.getFS()

	var next *Snapshot
	err = withUpdateLock(cur.Config.injectedFS, configPath, func() error {
		fresh, _, err := o.src.Read(ctx)
		if err != nil {
			return fmt.Errorf("reload config for update: %w", err)
		}
		draft := fresh.toDoc()
		if err := fn(&draft); err != nil {
			return err
		}
		fresh.fromDoc(draft)
		if err := fresh.saveLocked(fs, configPath); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		next, err = o.reloadLocked(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

// withUpdateLock runs fn under the advisory cross-process lock for configPath
// (its sidecar lives under the project's state/locks tree, never beside the
// file). An injected filesystem has no cross-process readers, so it runs fn
// unlocked. A lock ACQUISITION failure fails closed: flock.Lock blocks on
// contention, so an error from it is a persistent environmental failure,
// exactly when an unlocked read-modify-write would be least safe.
func withUpdateLock(injectedFS bool, configPath string, fn func() error) error {
	if injectedFS {
		return fn()
	}
	lockPath, err := paths.ProjectPathFor(configPath)
	if err != nil {
		return fmt.Errorf("config: locating update lock for %s: %w", configPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), lockDirMode); err != nil {
		return fmt.Errorf("config: preparing update lock directory for %s: %w", configPath, err)
	}
	fl := flock.New(lockPath, flock.SetPermissions(lockFileMode))
	stop := lockwait.Watch(lockPath)
	err = fl.Lock()
	stop()
	if err != nil {
		return fmt.Errorf("config: acquiring update lock for %s: %w", configPath, err)
	}
	defer func() { _ = fl.Unlock() }()
	return fn()
}
