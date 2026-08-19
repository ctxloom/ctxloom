package config

import (
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// Draft is the mutable view a Manager.Update transaction sees: every
// PERSISTED Config field (the same set configDoc/Fixture carry), exported so
// domain logic in internal/operations can mutate it directly —
// d.Agents[name] = ..., d.Settings.Statusline =
// &enabled — exactly the shapes the six production write sites already used
// before Config's fields were unexported (Phase 3). Runtime-only fields
// (AppPaths, Warnings, PendingUpgrade, ...) are deliberately absent: they are
// load-time facts about WHERE a config came from, not persisted values a
// write transaction edits.
//
// Draft is a plain alias for configDoc, not a separate type: they are the
// exact same "exported mirror of Config's persisted fields" shape for the
// exact same structural reason (yaml reflection needs exported fields;
// Update's caller needs exported fields), so there is no reason to maintain
// two copies of an identical field list.
type Draft = configDoc

// Manager mediates every WRITE to one project's config.yaml. Reads never go
// through a Manager (Load() is sufficient and lock-free); only a caller that
// intends to PERSIST a change constructs one. There is exactly one meaningful
// Manager per project.
type Manager struct {
	opts []LoadOption
}

// NewManager returns a Manager targeting the config Load(opts...) resolves to:
// no options means the ambient project the no-arg Load() finds, and
// WithAppDir/WithFS name an explicit target exactly as they do for Load. Every
// opts... value is threaded into both the pre-lock path resolution and the
// post-lock fresh reload Update performs, so a test Manager built with
// WithFS(memFS) never touches the real filesystem at any point in the
// transaction.
func NewManager(opts ...LoadOption) *Manager {
	return &Manager{opts: opts}
}

// Options returns the LoadOption set m was constructed with, so a caller
// that needs to re-Load the SAME target after a write (e.g. to read back a
// fully merged view spanning more than one Manager.Update transaction) can
// do so without duplicating or guessing m's own resolution — a test seam
// (WithFS/WithAppDir) given to m is honored by the re-read too.
func (m *Manager) Options() []LoadOption {
	return append([]LoadOption(nil), m.opts...)
}

// Update runs fn as ONE serialized write transaction: it acquires an advisory
// file lock (filelock.ProjectPathFor(configPath), which puts the sidecar under
// .ctxloom/state/locks/), re-reads the config FRESH from disk while holding it,
// hands fn a Draft built from that fresh read, applies whatever fn changed, and
// persists via saveLocked — all before releasing it.
// This is what closes the lost-update window a bare Load-mutate-write
// sequence does not: the state fn mutates is never older than the moment
// this call acquired the lock, so two concurrent Update calls (same process
// or different processes) can never silently discard one another's change
// the way two Load()-mutate-write sequences could.
//
//   - fn returning a non-nil error ABANDONS the transaction: nothing is
//     written, and the shared ambient Config every Load() holder already has
//     is completely untouched (Update never mutates any existing *Config in
//     place — it builds and discards its own private Config value for the
//     duration of the call).
//   - On success, the ambient memo is invalidated (via saveLocked), so the
//     very next Load() sees the write.
//   - A lock ACQUISITION failure (as opposed to ordinary blocking on
//     contention, which the lock already waits out) fails the whole call
//     closed — filelock.Lock can only error on a persistent environmental
//     failure, exactly when writing unlocked would be least safe, so this
//     never silently degrades to an unlocked read-modify-write.
func (m *Manager) Update(fn func(*Draft) error) error {
	// Resolve the target path first, UNLOCKED — this only walks directories
	// (findAppDir) to learn WHICH config.yaml is in play, touching none of
	// its bytes, so there is nothing here for a concurrent writer to race.
	// This used to be a full loadUncached() — schema validation, every
	// config layer, the upgrade pipeline, bundle-profile seeding — run and
	// discarded, solely to learn a path and an fs. resolveUpdateTarget does
	// only the bootstrap stage-1 resolution that path actually needs.
	fs, injectedFS, configPath, err := resolveUpdateTarget(m.opts...)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	// See Save's identical fix: filelock.Lock is BLOCKING, so an error from
	// it is a persistent environmental failure, never transient contention.
	// Proceeding unlocked on that failure is exactly backwards: it discards
	// the read-modify-write serialization this method exists to provide,
	// silently, on every subsequent Update call. Fail closed.
	if !injectedFS {
		// ProjectPathFor, not PathFor: config.yaml lives in the project's
		// .ctxloom tree, so its sidecar belongs under state/locks rather than
		// beside it, where `.ctxloom/config.yaml.lock` showed up untracked in
		// every freshly initialized project. The derivation is the filelock
		// package's, whole — a lock path composed here is a second opinion
		// about the same resource, which is the one failure that package
		// cannot detect.
		lockPath, lerr := filelock.ProjectPathFor(configPath)
		if lerr != nil {
			return fmt.Errorf("config: locating update lock for %s: %w", configPath, lerr)
		}
		unlock, lerr := filelock.Lock(lockPath)
		if lerr != nil {
			return fmt.Errorf("config: acquiring update lock for %s: %w", configPath, lerr)
		}
		defer unlock()
	}

	// Re-read FRESH now that the lock is held: this is the "read" half of
	// read-modify-write, and it is what makes this safe against a writer
	// that finished just before we acquired the lock — the path resolution
	// above (taken before the lock) could not see that writer's change;
	// this one can.
	fresh, err := loadUncached(m.opts...)
	if err != nil {
		return fmt.Errorf("reload config for update: %w", err)
	}

	draft := fresh.toDoc()
	if err := fn(&draft); err != nil {
		// Abandoned: fresh and draft are both local to this call and about to
		// be discarded. Nothing was ever written to disk, and no *Config any
		// other holder has was ever touched.
		return err
	}
	fresh.fromDoc(draft)

	if err := fresh.saveLocked(fs, configPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// resolveUpdateTarget resolves the filesystem, whether that filesystem was
// explicitly injected, and the config.yaml path Update should lock and
// write — the same bootstrap stage-1 resolution loadUncached performs before
// it ever touches a config layer's bytes (fs default, then options.appDir or
// findAppDir), but WITHOUT running the schema validator, reading any config
// layer, applying the upgrade pipeline, or seeding bundle profiles. Update
// only needs the path and the fs to acquire its lock; the actual content read
// happens separately, under the lock, via loadUncached.
func resolveUpdateTarget(opts ...LoadOption) (fs afero.Fs, injectedFS bool, configPath string, err error) {
	options := &loadOptions{}
	for _, opt := range opts {
		opt(options)
	}

	fs = options.fs
	injectedFS = fs != nil
	if fs == nil {
		fs = afero.NewOsFs()
	}

	var appPath string
	if options.appDir != "" {
		appPath = options.appDir
	} else {
		appPath, _ = findAppDir(fs)
	}
	if appPath == "" {
		return nil, false, "", fmt.Errorf("no .ctxloom directory found; run 'ctxloom init --local' first")
	}

	return fs, injectedFS, paths.ConfigPath(appPath), nil
}
