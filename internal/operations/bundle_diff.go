package operations

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// BundleChange describes one bundle that was added or modified since the
// active lockfile snapshot. Size is the raw byte length of the new YAML
// reported by the fetcher; left zero when unknown (the diff does not fetch
// content just to compute size — that's the BundleReader's job at
// show_bundle_verbatim time).
type BundleChange struct {
	// Name is the lockfile key ("remoteName/path").
	Name string
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

// DiffLockfiles returns the bundle-only delta between prev (active) and curr
// (pending). Entries whose source remote carries TrustBundles=true are
// filtered out — trusted remotes auto-apply without review.
//
// A nil prev is treated as "everything in curr is new." A nil curr produces
// an empty change set: removed bundles aren't shown for review (the user
// removed them on purpose by editing the lockfile or removing the remote).
//
// registry may be nil; in that case trust filtering is a no-op (no remote
// is considered trusted).
func DiffLockfiles(prev, curr *remote.Lockfile, registry *remote.Registry) *BundleChangeSet {
	cs := &BundleChangeSet{}
	if curr == nil {
		return cs
	}

	prevBundles := map[string]remote.LockEntry{}
	if prev != nil {
		prevBundles = prev.Bundles
	}

	for name, entry := range curr.Bundles {
		remoteName, _, _ := strings.Cut(name, "/")
		if isTrustedRemote(registry, remoteName) {
			continue
		}
		old, existed := prevBundles[name]
		switch {
		case !existed:
			cs.Added = append(cs.Added, BundleChange{
				Name:   name,
				Remote: remoteName,
				NewSHA: entry.SHA,
			})
		case old.SHA != entry.SHA:
			cs.Modified = append(cs.Modified, BundleChange{
				Name:   name,
				Remote: remoteName,
				OldSHA: old.SHA,
				NewSHA: entry.SHA,
			})
		}
	}
	return cs
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
