package config

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// Snapshot is Config under its Phase-4 target name: the immutable value every
// read call site holds (Current/Reload return one; every existing Load()/
// LoadFresh() caller already holds one, since Snapshot IS Config — a plain
// type alias, not a new type). Two names for one type, deliberately: renaming
// Config itself would touch every one of the ~85 non-test files across the
// tree that spell out *config.Config as a parameter/field/return type — a
// second Phase-3-sized mechanical rewrite for zero behavioral gain, since a
// type alias makes *config.Config and *config.Snapshot interchangeable at
// every one of those call sites without touching any of them. New code should
// prefer Snapshot; existing code needs no migration.
type Snapshot = Config

// Current returns the ambient project Snapshot — the memoized, read-only
// value every read call site should hold. It is Load() under the Phase-4
// target name (see Load's own doc for the memoization/staleness contract);
// unlike Load, it takes no options, since Current's whole point is "the one
// ambient config a process reads" — an explicit target is a different
// Snapshot, and callers that need one still use Load(WithAppDir(...)) /
// Load(WithFS(...)) directly.
func Current() (*Snapshot, error) {
	return Load()
}

// Reload drops the memoized ambient Snapshot and re-reads it from disk —
// re-running the full bootstrap + layering + override pipeline, so a
// persistent overlay (an env var, a --config-set flag installed via
// SetOverrides) is reapplied to the freshly-read files exactly as it was the
// first time (loadUncached is the single funnel every entry point,
// including this one, resolves overrides through). A Snapshot obtained
// BEFORE Reload is untouched by it: Reload never mutates an existing
// *Snapshot, it only changes what the NEXT Current() call returns, matching
// Invalidate()+Load()'s existing behavior exactly (Reload is that pair,
// named for the Phase-4 API).
func Reload() (*Snapshot, error) {
	Invalidate()
	return Load()
}

// Draft is the mutable view a Manager.Update transaction sees: every
// PERSISTED Config field (the same set configDoc/Fixture carry), exported so
// domain logic in internal/operations can mutate it directly —
// d.Agents[name] = ..., delete(d.MCP.Servers, name), d.Settings.Statusline =
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
// through a Manager (Current()/Load() are sufficient and lock-free); only a
// caller that intends to PERSIST a change constructs one. There is exactly
// one meaningful Manager per project — DefaultManager returns the one for
// the ambient project Load()/Current() already resolve to; NewManager exists
// for explicit-target callers (tests, an --app-dir override) the same way
// WithAppDir/WithFS do for Load.
type Manager struct {
	opts []LoadOption
}

// DefaultManager returns the Manager for the ambient project config — the
// same directory/file Current() and the no-arg Load() resolve to.
func DefaultManager() *Manager {
	return &Manager{}
}

// NewManager returns a Manager targeting an explicit config (a specific
// app dir and/or filesystem), mirroring Load(opts...)'s own explicit-target
// path. Every opts... value is threaded into both the pre-lock path
// resolution and the post-lock fresh reload Update performs, so a test
// Manager built with WithFS(memFS) never touches the real filesystem at any
// point in the transaction.
func NewManager(opts ...LoadOption) *Manager {
	return &Manager{opts: opts}
}

// Update runs fn as ONE serialized write transaction: it acquires the same
// advisory file lock Save() takes (configPath+".lock"), re-reads the config
// FRESH from disk while holding it, hands fn a Draft built from that fresh
// read, applies whatever fn changed, and persists — all before releasing the
// lock. This is what actually closes the lost-update window Save() alone
// does not (see Save's doc): the state fn mutates is never older than the
// moment this call acquired the lock, so two concurrent Update calls
// (same process or different processes) can never silently discard one
// another's change the way two Load()-mutate-Save() sequences already could.
//
//   - fn returning a non-nil error ABANDONS the transaction: nothing is
//     written, and the shared ambient Snapshot every Current()/Load() holder
//     already has is completely untouched (Update never mutates any
//     *Snapshot in place — it builds and discards its own private Config
//     value for the duration of the call).
//   - On success, the ambient memo is invalidated (via saveLocked, the same
//     Save() itself already relies on), so the very next Current() sees the
//     write.
//   - A lock ACQUISITION failure (as opposed to ordinary blocking on
//     contention, which the lock already waits out) fails the whole call
//     closed (U109-F01) — filelock.Lock can only error on a persistent
//     environmental failure, exactly when writing unlocked would be least
//     safe, so this never silently degrades to an unlocked read-modify-write.
func (m *Manager) Update(fn func(*Draft) error) error {
	// Resolve the target path first, UNLOCKED — this only walks directories
	// (findAppDir) to learn WHICH config.yaml is in play, touching none of
	// its bytes, so there is nothing here for a concurrent writer to race.
	pathCfg, err := loadUncached(m.opts...)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	configPath, err := pathCfg.GetConfigFilePath()
	if err != nil {
		return fmt.Errorf("failed to get config path: %w", err)
	}

	fs := pathCfg.getFS()
	// U109-F01: see Save's identical fix — filelock.Lock is BLOCKING, so an
	// error from it is a persistent environmental failure, never transient
	// contention. Proceeding unlocked on that failure is exactly backwards:
	// it discards the read-modify-write serialization this method exists to
	// provide, silently, on every subsequent Update call. Fail closed.
	if !pathCfg.injectedFS {
		unlock, lerr := filelock.Lock(configPath + ".lock")
		if lerr != nil {
			return fmt.Errorf("config: acquiring update lock for %s: %w", configPath, lerr)
		}
		defer unlock()
	}

	// Re-read FRESH now that the lock is held: this is the "read" half of
	// read-modify-write, and it is what makes this safe against a writer
	// that finished just before we acquired the lock — a Load() taken before
	// the lock (like pathCfg above) could not see that writer's change;
	// this one can.
	fresh, err := loadUncached(m.opts...)
	if err != nil {
		return fmt.Errorf("reload config for update: %w", err)
	}

	draft := fresh.toDoc()
	if err := fn(&draft); err != nil {
		// Abandoned: fresh and draft are both local to this call and about to
		// be discarded. Nothing was ever written to disk, and no *Snapshot
		// any other holder has was ever touched.
		return err
	}
	fresh.fromDoc(draft)

	if err := fresh.saveLocked(fs, configPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}
