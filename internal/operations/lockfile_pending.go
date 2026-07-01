package operations

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// DropPendingBundle removes one bundle from the pending lockfile (used by
// decline_bundle with a name). Returns whether the bundle was actually
// present. Empties out the pending file when the last entry leaves.
func DropPendingBundle(cfg *config.Config, name string) (bool, error) {
	baseDir := getBaseDir(cfg)
	pendingMgr := remote.NewLockfileManager(baseDir, remote.WithPendingLockfile())

	pending, err := pendingMgr.Load()
	if err != nil {
		return false, fmt.Errorf("load pending lockfile: %w", err)
	}
	// Accept the same "<alias>/<path>" short form as the other review
	// commands; pending lockfile keys are canonical refs.
	name = CanonicalizeRemoteRef(cfg, name, remote.ItemTypeBundle)
	if _, ok := pending.GetEntry(remote.ItemTypeBundle, name); !ok {
		return false, nil
	}
	pending.RemoveEntry(remote.ItemTypeBundle, name)
	if pending.IsEmpty() {
		return true, pendingMgr.Delete()
	}
	return true, pendingMgr.Save(pending)
}

// ClearPendingLockfile drops the pending file outright (decline_bundle
// without a name, after the review flow has been declined wholesale).
func ClearPendingLockfile(cfg *config.Config) error {
	baseDir := getBaseDir(cfg)
	pendingMgr := remote.NewLockfileManager(baseDir, remote.WithPendingLockfile())
	if err := pendingMgr.Delete(); err != nil {
		return fmt.Errorf("delete pending lockfile: %w", err)
	}
	return nil
}

// LoadPendingLockfile returns the current pending file, or nil if absent.
// Used by show_bundle_verbatim to read at the *new* SHA before the user
// has approved.
func LoadPendingLockfile(cfg *config.Config) (*remote.Lockfile, error) {
	baseDir := getBaseDir(cfg)
	pendingMgr := remote.NewLockfileManager(baseDir, remote.WithPendingLockfile())
	lock, err := pendingMgr.Load()
	if err != nil {
		return nil, err
	}
	if lock.IsEmpty() {
		return nil, nil
	}
	return lock, nil
}

// LoadActiveLockfile returns the active lockfile (or an empty one if not
// yet on disk). Mirrors LoadPendingLockfile. Used by pin_bundle to report
// the pinned SHA back to the caller.
func LoadActiveLockfile(cfg *config.Config) (*remote.Lockfile, error) {
	baseDir := getBaseDir(cfg)
	return remote.NewLockfileManager(baseDir).Load()
}

// SetItemPin flips the Pinned flag on the active lockfile entry that ref names,
// trying both item types (bundle then profile) so a directly-referenced profile
// can be frozen too. ref may be a short "<alias>/<path>" or canonical form.
// Returns whether a matching entry was found. Persists on a change.
//
// Pinning is the user's "do not upgrade this" mark: StageUpgrade skips a pinned
// ref, and lock preserves the flag across a closure rebuild, so the pin holds
// the item at its current SHA until unpinned.
func SetItemPin(cfg *config.Config, ref string, pinned bool) (bool, error) {
	baseDir := getBaseDir(cfg)
	activeMgr := remote.NewLockfileManager(baseDir)
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
