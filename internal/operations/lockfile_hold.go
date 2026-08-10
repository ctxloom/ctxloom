package operations

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// activeLockfileManager builds the lockfile manager for cfg's active lockfile
// over cfg's OWN filesystem. The FS must be threaded (as lockfile.go and
// trust.go do): without it the manager falls back to the real OS filesystem and
// reads/writes a DIFFERENT lock.yaml than the rest of the run resolves against.
func activeLockfileManager(cfg *config.Config) *remote.LockfileManager {
	baseDir := getBaseDir(cfg)
	if fs := cfgFS(cfg); fs != nil {
		return remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	}
	return remote.NewLockfileManager(baseDir)
}

// LoadActiveLockfile returns the active lockfile (or an empty one if not yet on
// disk). Used by `deps hold` to report the held SHA back to the caller.
func LoadActiveLockfile(cfg *config.Config) (*remote.Lockfile, error) {
	return activeLockfileManager(cfg).Load()
}

// SetItemPin flips the Pinned ("hold") flag on the active lockfile entry that
// ref names. ref may be a short "<alias>/<path>" or canonical form. Returns
// whether a matching entry was found. Persists on a change.
//
// A hold is the user's "do not upgrade this" mark: UpgradeDependencies skips a
// held ref, and a lock rebuild preserves the flag across a closure rebuild, so
// the hold freezes the item at its current SHA until released.
func SetItemPin(cfg *config.Config, ref string, pinned bool) (bool, error) {
	activeMgr := activeLockfileManager(cfg)
	active, err := activeMgr.Load()
	if err != nil {
		return false, fmt.Errorf("load active lockfile: %w", err)
	}
	// Only bundles are locked now (top-level profile distribution was retired).
	canonical := CanonicalizeRemoteRef(cfg, ref, remote.ItemTypeBundle)
	entry, ok := active.GetEntry(remote.ItemTypeBundle, canonical)
	if !ok {
		return false, nil
	}
	if entry.Pinned == pinned {
		return true, nil // idempotent
	}
	entry.Pinned = pinned
	active.AddEntry(remote.ItemTypeBundle, canonical, entry)
	if err := activeMgr.Save(active); err != nil {
		return true, fmt.Errorf("save active lockfile: %w", err)
	}
	return true, nil
}
