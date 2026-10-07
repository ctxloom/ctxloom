package operations

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Puller interface for pulling remote items (allows mocking in tests).
type Puller interface {
	Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error)
}

// LockDependenciesRequest contains parameters for generating a lockfile.
type LockDependenciesRequest struct {
	// FailOnConflict makes a dependency hash conflict a hard error, for callers
	// that want strict locking. When false (startup auto-lock) the conflict is
	// warned and the conflicted items are dropped, never blocking the session
	// (CLAUDE.md).
	FailOnConflict bool `json:"-"`

	FS afero.Fs `json:"-"` // Optional filesystem (defaults to OS filesystem if nil)
}

// LockDependenciesResult contains the result of generating a lockfile.
type LockDependenciesResult struct {
	Status    string `json:"status"`
	Path      string `json:"path,omitempty"`
	ItemCount int    `json:"item_count,omitempty"`
	Message   string `json:"message,omitempty"`
	// Incomplete is true when part of the dependency closure could not be
	// reached, so the written lock preserved unreached entries rather than
	// rebuilding them. "generated" alone cannot tell that apart from a
	// complete lock.
	Incomplete bool `json:"incomplete,omitempty"`
	// Unreachable names, sorted, every part of the closure that could not be
	// reached — the items whose previous entries the lock kept.
	Unreachable []string `json:"unreachable,omitempty"`
	// Removed names, sorted, the previous entries the rebuilt closure no
	// longer reaches — dropped by the wholesale write.
	Removed []string `json:"removed,omitempty"`
}

// LockDependencies builds lock.yaml from the flattened transitive closure of
// the project's local profiles. Every reference is hash-pinned, so the closure
// is fully determined by what each item references — no resolution to "latest"
// (that is Relock/`update`). If the same item appears at two differing hashes
// anywhere in the closure that is a conflict: surfaced immediately as a hard
// error when FailOnConflict is set, else warned and the conflicted items
// dropped so startup is never blocked (CLAUDE.md).
//
// A rebuild creates first pins and never moves an existing one: the lock-mode
// resolver carries every pinned SHA, whatever the manifest constraint now says.
// Only `deps upgrade` moves a pin.
func LockDependencies(ctx context.Context, cfg *config.Config, req LockDependenciesRequest) (*LockDependenciesResult, error) {
	fs := getFS(req.FS)
	baseDir := ProjectAppDir(cfg)

	// Run sync first so the clones the closure walk reads are present.
	pins, conflicts, unexpanded, err := FlattenDependencies(ctx, cfg, nil)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		if req.FailOnConflict {
			return nil, ConflictError(conflicts)
		}
		clidiag.Warn("ctxloom", "%v", ConflictError(conflicts))
		pins = dropConflicted(pins, conflicts)
	}

	lockManager := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	// The previous lockfile anchors two safety nets across the closure
	// rebuild: the per-item Held flag (the user's "do not upgrade this" hold
	// must survive a relock), and the preserved entries when the closure is
	// incomplete.
	//
	// An UNREADABLE previous lockfile FAILS the rebuild (ruled). A rebuild that
	// started from an empty lock would treat every pin as a first pin and
	// re-resolve it — moving pins outside `deps upgrade`, decided by a read
	// error. The corrupt file, and the pins and holds still recorded in it, is
	// left for the user to fix or delete.
	prev, prevErr := lockManager.Load()
	if prevErr != nil {
		return nil, fmt.Errorf("%w: the pins and holds in %s would be lost to a rebuild; fix or delete it: %w", remote.ErrLockfileUnreadable, lockManager.Path(), prevErr)
	}
	// prevEntries is the previous lockfile keyed the same way the rebuild keys
	// its pins, so every field that must OUTLIVE a closure rebuild is read from
	// one entry. Forgetting to carry a field is silent — the rebuild writes a
	// valid lockfile with the field simply gone — so they are carried together
	// by relockEntry rather than one lookup per field.
	//
	// What must survive, and why:
	//   Held      — the user's "do not upgrade this" hold is a decision, and a
	//               relock is not entitled to reverse it.
	//   RequestedVersion — what the carried pin was resolved from.
	prevEntries := map[string]remote.LockEntry{}
	for _, e := range prev.AllEntries() {
		prevEntries[relockKey(e.Type, e.Ref)] = e.Entry
	}

	lockfile := &remote.Lockfile{
		Version: remote.LockfileVersion,
		Bundles: make(map[ident.BundleKey]remote.LockEntry),
	}
	for _, p := range pins {
		lockfile.AddEntry(p.Type, p.Identity, relockEntry(p, prevEntries))
	}

	// An INCOMPLETE closure (a remote parent profile could not be expanded)
	// must not erase healthy entries: merge in every previous entry the rebuilt
	// closure no longer reaches, so a transient fetch failure never loses lock
	// state. The next complete relock drops genuinely-removed entries.
	if len(unexpanded) > 0 {
		preserveUnreachedEntries(prev, lockfile, len(unexpanded))
	}

	return saveRelock(lockManager, prev, lockfile, unexpanded)
}

// saveRelock persists a rebuilt lockfile and reports it. unreachable carries
// through to the result so neither "generated" nor "empty" reads as a
// complete, clean lock when part of the closure was never reached; prev is the
// lock being replaced, against which a written lock names what it dropped.
func saveRelock(lockManager *remote.LockfileManager, prev, lockfile *remote.Lockfile, unreachable []string) (*LockDependenciesResult, error) {
	incomplete := len(unreachable) > 0
	if lockfile.IsEmpty() {
		msg := "No remote items found"
		if incomplete {
			msg = "No remote items found among what could be read; part of the dependency closure was unreachable"
		}
		return &LockDependenciesResult{
			Status:      "empty",
			Message:     msg,
			Incomplete:  incomplete,
			Unreachable: unreachable,
		}, nil
	}

	if err := lockManager.Save(lockfile); err != nil {
		return nil, err
	}

	return &LockDependenciesResult{
		Status:      "generated",
		Path:        lockManager.Path(),
		ItemCount:   len(lockfile.AllEntries()),
		Incomplete:  incomplete,
		Unreachable: unreachable,
		Removed:     droppedEntries(prev, lockfile),
	}, nil
}

// relockKey keys a lock entry by type and identity.
func relockKey(t remote.ItemType, id ident.BundleKey) string {
	return string(t) + "\x00" + string(id)
}

// relockEntry is the entry p lands as in a rebuild, carrying what its previous
// entry must keep. The rebuild never moves a pin (the lock-mode resolver
// carries every existing SHA), so a carried pin keeps its constraint; Held
// carries regardless.
func relockEntry(p PinnedRef, prevEntries map[string]remote.LockEntry) remote.LockEntry {
	entry := pinnedEntry(p)
	prevEntry, ok := prevEntries[relockKey(p.Type, p.Identity)]
	if !ok {
		return entry
	}
	entry.Held = prevEntry.Held
	if prevEntry.SHA == p.Hash {
		// The manifest may ask for something else now; only `deps upgrade`
		// applies that (see constraintChanges).
		entry.RequestedVersion = prevEntry.RequestedVersion
	}
	return entry
}

// pinnedEntry is the lock entry p resolves to.
// RequestedVersion records the manifest constraint so a later relock can carry
// this SHA forward while the constraint is unchanged; Version records the tag a
// semver constraint chose, for display and satisfaction checks.
func pinnedEntry(p PinnedRef) remote.LockEntry {
	return remote.LockEntry{SHA: p.Hash, URL: p.URL, RequestedVersion: p.Constraint, Version: p.Version, Kind: p.Kind}
}

// dropConflicted returns the pins whose identity is NOT in conflicts — used by
// the startup auto-lock to degrade past a conflict rather than block the
// session. The result owns its own backing array: filtering into pins[:0] would
// alias and overwrite the caller's slice, so the argument would no longer
// describe the pre-filter closure.
func dropConflicted(pins []PinnedRef, conflicts []DependencyConflict) []PinnedRef {
	bad := make(map[string]struct{}, len(conflicts))
	for _, c := range conflicts {
		bad[c.Item] = struct{}{}
	}
	kept := make([]PinnedRef, 0, len(pins))
	for _, p := range pins {
		if _, isBad := bad[string(p.Identity)]; !isBad {
			kept = append(kept, p)
		}
	}
	return kept
}

// RepoUpdater is the subset of *remote.RepoCache that the per-URL refresh
// pre-pass needs. Production uses *remote.RepoCache directly; tests inject a
// counting mock so the dedup invariant is observable.
type RepoUpdater interface {
	UpdateRepo(ctx context.Context, repoURL string, forgeType remote.ForgeType) (string, error)
}

// refreshRepoCaches advances each unique clone to live HEAD before SHA
// resolution, so a stale shallow clone doesn't pin/report the old SHA. Per-URL
// failures warn and continue rather than abort; it reports whether any fetch
// failed, so a caller that reports currency can say its answer is incomplete.
// Shared by UpgradeDependencies (upgrade.go) and the sync path (sync.go).
//
// A URL registered answers false for is never fetched: it is not a remote, and
// the walk or pull that reaches it refuses it by name.
func refreshRepoCaches(ctx context.Context, cache RepoUpdater, urls []string, registered func(repoURL string) bool) (fetchFailed bool) {
	for _, url := range urls {
		if !registered(url) {
			continue
		}
		forgeType, _, err := remote.DetectForge(url)
		if err != nil {
			clidiag.Warn("ctxloom", "detect forge for %s: %v", url, err)
			continue
		}
		if _, err := cache.UpdateRepo(ctx, url, forgeType); err != nil {
			clidiag.Warn("ctxloom", "fetch %s: %v", url, err)
			fetchFailed = true
		}
	}
	return fetchFailed
}
