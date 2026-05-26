package operations

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// MergePendingLockfile copies every bundle entry from lock.pending.yaml into
// lock.yaml, then deletes the pending file. Profiles are left alone because
// sync never routes them to pending (profiles bypass the review flow).
// No-op when pending is absent or empty.
//
// Returns the number of bundles merged.
func MergePendingLockfile(cfg *config.Config) error {
	_, err := MergePendingLockfileCount(cfg)
	return err
}

// MergePendingLockfileCount is like MergePendingLockfile but reports how
// many bundles moved across. Useful for tool responses.
func MergePendingLockfileCount(cfg *config.Config) (int, error) {
	baseDir := getBaseDir(cfg)
	activeMgr := remote.NewLockfileManager(baseDir)
	pendingMgr := remote.NewLockfileManager(baseDir, remote.WithPendingLockfile())

	pending, err := pendingMgr.Load()
	if err != nil {
		return 0, fmt.Errorf("load pending lockfile: %w", err)
	}
	if pending.IsEmpty() {
		return 0, pendingMgr.Delete()
	}

	active, err := activeMgr.Load()
	if err != nil {
		return 0, fmt.Errorf("load active lockfile: %w", err)
	}

	merged := 0
	for name, entry := range pending.Bundles {
		active.AddEntry(remote.ItemTypeBundle, name, entry)
		merged++
	}

	if err := activeMgr.Save(active); err != nil {
		return 0, fmt.Errorf("save active lockfile: %w", err)
	}
	if err := pendingMgr.Delete(); err != nil {
		return merged, fmt.Errorf("delete pending lockfile: %w", err)
	}
	return merged, nil
}

// PromotePendingBundles moves the named bundles from pending into active and
// drops them from pending. Names not present in pending are silently
// skipped. Used by trust_remote when an entire remote's pending entries
// get auto-approved.
func PromotePendingBundles(cfg *config.Config, names []string) error {
	if len(names) == 0 {
		return nil
	}
	baseDir := getBaseDir(cfg)
	activeMgr := remote.NewLockfileManager(baseDir)
	pendingMgr := remote.NewLockfileManager(baseDir, remote.WithPendingLockfile())

	pending, err := pendingMgr.Load()
	if err != nil {
		return fmt.Errorf("load pending lockfile: %w", err)
	}
	active, err := activeMgr.Load()
	if err != nil {
		return fmt.Errorf("load active lockfile: %w", err)
	}

	moved := 0
	for _, name := range names {
		entry, ok := pending.GetEntry(remote.ItemTypeBundle, name)
		if !ok {
			continue
		}
		active.AddEntry(remote.ItemTypeBundle, name, entry)
		pending.RemoveEntry(remote.ItemTypeBundle, name)
		moved++
	}

	if moved == 0 {
		return nil
	}
	if err := activeMgr.Save(active); err != nil {
		return fmt.Errorf("save active lockfile: %w", err)
	}
	if pending.IsEmpty() {
		return pendingMgr.Delete()
	}
	return pendingMgr.Save(pending)
}

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
	_ = os.Stderr // placeholder to keep os imported when other callers shrink
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
