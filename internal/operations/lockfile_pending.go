package operations

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/afero"

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

// PromoteTrustedPendingBundles lifts every pending bundle whose source
// remote is marked TrustBundles=true straight into the active lockfile,
// dropping it from pending. Trust means "apply without review", so these
// changes bypass the review gate entirely instead of being orphaned in
// pending (where nothing would ever promote them).
//
// Pin overrides trust: a pending entry whose active counterpart is Pinned
// is left in place — the user has explicitly frozen that bundle at its
// active SHA, and a per-bundle pin is more specific than per-remote trust.
//
// fs is threaded so callers inside SyncDependencies operate on the same
// (possibly in-memory) filesystem as the rest of the sync; pass
// afero.NewOsFs() for real-disk callers. A nil registry trusts no remote,
// making this a no-op. Returns the names promoted.
func PromoteTrustedPendingBundles(cfg *config.Config, registry *remote.Registry, fs afero.Fs) ([]string, error) {
	baseDir := getBaseDir(cfg)
	pendingMgr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs), remote.WithPendingLockfile())
	pending, err := pendingMgr.Load()
	if err != nil {
		return nil, fmt.Errorf("load pending lockfile: %w", err)
	}
	if pending.IsEmpty() {
		return nil, nil
	}

	activeMgr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	active, err := activeMgr.Load()
	if err != nil {
		return nil, fmt.Errorf("load active lockfile: %w", err)
	}

	var promoted []string
	for name, entry := range pending.Bundles {
		remoteName, _, _ := strings.Cut(name, "/")
		if !isTrustedRemote(registry, remoteName) {
			continue
		}
		if old, ok := active.GetEntry(remote.ItemTypeBundle, name); ok && old.Pinned {
			continue
		}
		active.AddEntry(remote.ItemTypeBundle, name, entry)
		pending.RemoveEntry(remote.ItemTypeBundle, name)
		promoted = append(promoted, name)
	}

	if len(promoted) == 0 {
		return nil, nil
	}
	if err := activeMgr.Save(active); err != nil {
		return nil, fmt.Errorf("save active lockfile: %w", err)
	}
	if pending.IsEmpty() {
		return promoted, pendingMgr.Delete()
	}
	return promoted, pendingMgr.Save(pending)
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

// LoadActiveLockfile returns the active lockfile (or an empty one if not
// yet on disk). Mirrors LoadPendingLockfile. Used by pin_bundle to report
// the pinned SHA back to the caller.
func LoadActiveLockfile(cfg *config.Config) (*remote.Lockfile, error) {
	baseDir := getBaseDir(cfg)
	return remote.NewLockfileManager(baseDir).Load()
}

// SetBundlePin flips the Pinned flag on the active lockfile entry for
// name. Returns whether the entry was present. Persists the lockfile on
// success. ErrNotFound (false, nil) is the "no such bundle in active
// lock" signal; callers surface that as a friendly error rather than
// treating it as a failure.
//
// Pinning is metadata-only: the SHA stays where it is, and future syncs
// still pull new SHAs into pending. The pin's effect is that
// DiffLockfiles no longer surfaces the change in the review template,
// so the user stops seeing review prompts for this bundle.
func SetBundlePin(cfg *config.Config, name string, pinned bool) (bool, error) {
	baseDir := getBaseDir(cfg)
	activeMgr := remote.NewLockfileManager(baseDir)
	active, err := activeMgr.Load()
	if err != nil {
		return false, fmt.Errorf("load active lockfile: %w", err)
	}
	entry, ok := active.GetEntry(remote.ItemTypeBundle, name)
	if !ok {
		return false, nil
	}
	if entry.Pinned == pinned {
		// Idempotent: nothing to save.
		return true, nil
	}
	entry.Pinned = pinned
	active.AddEntry(remote.ItemTypeBundle, name, entry)
	if err := activeMgr.Save(active); err != nil {
		return true, fmt.Errorf("save active lockfile: %w", err)
	}
	return true, nil
}
