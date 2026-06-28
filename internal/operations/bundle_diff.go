package operations

import (
	"sort"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// BundleChange describes one item (bundle or profile) that was added or
// modified since the active lockfile snapshot. Size is the raw byte length of
// the new YAML reported by the fetcher; left zero when unknown (the diff does
// not fetch content just to compute size — that's the BundleReader's job at
// show_bundle_verbatim time).
type BundleChange struct {
	// Name is the lockfile key ("remoteName/path").
	Name string
	// Kind is the lockfile entry type (bundle or profile). Staged remote
	// PROFILE changes are applied by ApproveUpgrade exactly like bundle
	// changes, so they must surface in review too.
	Kind remote.ItemType
	// Remote is the registered remote this change originates from.
	Remote string
	// OldSHA is the SHA recorded in the active lockfile; empty for new bundles.
	OldSHA string
	// NewSHA is the SHA in the pending lockfile.
	NewSHA string
	// Size is the new bundle's YAML byte length, when known.
	Size int64
}

// BundleChangeSet is what handleInitialize hands to bundleReviewState.
// Empty Added and Modified means there is nothing to review.
type BundleChangeSet struct {
	Added    []BundleChange
	Modified []BundleChange
}

// IsEmpty returns true when there are no changes to review.
func (c *BundleChangeSet) IsEmpty() bool {
	return c == nil || (len(c.Added) == 0 && len(c.Modified) == 0)
}

// All returns Added followed by Modified, useful for callers that want a
// single iteration order (e.g. the rendered review template).
func (c *BundleChangeSet) All() []BundleChange {
	if c == nil {
		return nil
	}
	out := make([]BundleChange, 0, len(c.Added)+len(c.Modified))
	out = append(out, c.Added...)
	out = append(out, c.Modified...)
	return out
}

// DiffLockfiles returns the bundle and profile delta between prev (active) and
// curr (pending). Entries whose source remote carries TrustBundles=true are
// filtered out — trusted remotes auto-apply without review. Profile entries
// are covered because ApproveUpgrade merges ALL pending entries into the
// active lock — a staged profile change the review never showed would
// otherwise be applied sight-unseen.
//
// A nil prev is treated as "everything in curr is new." A nil curr produces
// an empty change set: removed entries aren't shown for review (approval
// never removes — the user removes them on purpose by editing the lockfile or
// removing the remote).
//
// registry may be nil; in that case trust filtering is a no-op (no remote
// is considered trusted).
func DiffLockfiles(prev, curr *remote.Lockfile, registry *remote.Registry) *BundleChangeSet {
	cs := &BundleChangeSet{}
	if curr == nil {
		return cs
	}

	prevBundles := map[string]remote.LockEntry{}
	prevProfiles := map[string]remote.LockEntry{}
	if prev != nil {
		prevBundles = prev.Bundles
		prevProfiles = prev.Profiles
	}

	diffEntryMaps(cs, registry, remote.ItemTypeBundle, prevBundles, curr.Bundles)
	diffEntryMaps(cs, registry, remote.ItemTypeProfile, prevProfiles, curr.Profiles)

	// diffEntryMaps appends in Go map-iteration order, which is randomized;
	// sort so the review output (and All()) is deterministic run to run.
	sortBundleChanges(cs.Added)
	sortBundleChanges(cs.Modified)
	return cs
}

// sortBundleChanges orders changes by Name, then Kind, in place — giving the
// review a stable, deterministic order independent of map iteration.
func sortBundleChanges(changes []BundleChange) {
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Name != changes[j].Name {
			return changes[i].Name < changes[j].Name
		}
		return changes[i].Kind < changes[j].Kind
	})
}

// diffEntryMaps appends curr's added/modified entries of one item type to cs,
// applying the trust filter and the pinned-entry suppression.
func diffEntryMaps(cs *BundleChangeSet, registry *remote.Registry, kind remote.ItemType, prevEntries, currEntries map[string]remote.LockEntry) {
	for name, entry := range currEntries {
		remoteName := remoteNameForKey(registry, name)
		if remoteName != "" && isTrustedRemote(registry, remoteName) {
			continue
		}
		if remoteName == "" {
			remoteName = displayRemoteForKey(name)
		}
		old, existed := prevEntries[name]
		// Pinned entries on the active side never surface a change:
		// the user has explicitly frozen this item at the active SHA
		// and doesn't want to see review noise for it. The pending
		// lockfile still records the new SHA so an unpin + re-review
		// flow remains possible later.
		if existed && old.Pinned {
			continue
		}
		switch {
		case !existed:
			cs.Added = append(cs.Added, BundleChange{
				Name:   name,
				Kind:   kind,
				Remote: remoteName,
				NewSHA: entry.SHA,
			})
		case old.SHA != entry.SHA:
			cs.Modified = append(cs.Modified, BundleChange{
				Name:   name,
				Kind:   kind,
				Remote: remoteName,
				OldSHA: old.SHA,
				NewSHA: entry.SHA,
			})
		}
	}
}

// remoteNameForKey resolves the registry remote alias for a canonical lockfile
// key by matching the key's repo URL. Returns "" when the key is not canonical,
// the registry is nil, or the URL is not a registered remote.
func remoteNameForKey(registry *remote.Registry, key string) string {
	if registry == nil {
		return ""
	}
	ref, err := remote.ParseReference(key)
	if err != nil || ref.URL == "" {
		return ""
	}
	name, ok := registry.FindByURL(ref.URL)
	if !ok {
		return ""
	}
	return name
}

// displayRemoteForKey returns a human-facing remote label for a canonical
// lockfile key (host/owner/repo), falling back to the raw key.
func displayRemoteForKey(key string) string {
	ref, err := remote.ParseReference(key)
	if err != nil || ref.URL == "" {
		return key
	}
	return ref.LocalRemoteName()
}

// isTrustedRemote reports whether the registry has TrustBundles=true for
// remoteName. Unknown remotes are treated as untrusted.
func isTrustedRemote(registry *remote.Registry, remoteName string) bool {
	if registry == nil || remoteName == "" {
		return false
	}
	rem, err := registry.Get(remoteName)
	if err != nil || rem == nil {
		return false
	}
	return rem.TrustBundles
}
