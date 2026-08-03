package operations

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Puller interface for pulling remote items (allows mocking in tests).
type Puller interface {
	Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error)
}

// LockDependenciesRequest contains parameters for generating a lockfile.
type LockDependenciesRequest struct {
	// SkipSync skips running sync before generating the lockfile.
	// By default, sync runs first to ensure all dependencies are installed
	// before locking their versions. Set to true to skip this behavior.
	SkipSync bool `json:"skip_sync"`

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
}

// LockDependencies builds lock.yaml from the flattened transitive closure of
// the project's local profiles. Every reference is hash-pinned, so the closure
// is fully determined by what each item references — no resolution to "latest"
// (that is Relock/`update`). If the same item appears at two differing hashes
// anywhere in the closure that is a conflict: surfaced immediately as a hard
// error when FailOnConflict is set, else warned and the conflicted items
// dropped so startup is never blocked (CLAUDE.md).
func LockDependencies(ctx context.Context, cfg *config.Config, req LockDependenciesRequest) (*LockDependenciesResult, error) {
	fs := getFS(req.FS)
	baseDir := getBaseDir(cfg)

	// Run sync first so the clones the closure walk reads are present.
	if !req.SkipSync {
		if _, err := SyncDependencies(ctx, cfg, SyncDependenciesRequest{
			Force: false,
			Lock:  false, // don't recurse back into lock
			FS:    req.FS,
		}); err != nil {
			return nil, fmt.Errorf("failed to sync dependencies before locking: %w", err)
		}
	}

	pins, conflicts, unexpanded := FlattenDependencies(ctx, cfg, nil)
	if len(conflicts) > 0 {
		if req.FailOnConflict {
			return nil, ConflictError(conflicts)
		}
		clidiag.Warn("ctxloom", "%v", ConflictError(conflicts))
		pins = dropConflicted(pins, conflicts)
	}

	lockManager := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	// The previous lockfile anchors three safety nets across the closure
	// rebuild: the per-item Pinned flag (the user's "do not upgrade this" hold
	// must survive a relock), the per-item Retracted flag, and the preserved
	// entries when the closure is incomplete.
	//
	// An UNREADABLE previous lockfile degrades to an empty one here, which
	// means none of those three can be carried forward. That
	// degrade is survivable only because it can no longer be PERSISTED: the
	// Save below reads back what is on disk and refuses to overwrite a file it
	// cannot parse (remote.ErrLockfileUnreadable), so the corrupt file — and
	// the holds and retractions still recorded in it — is left intact for the
	// user to fix or delete. Do not "recover" by writing over it.
	prev, prevErr := lockManager.Load()
	if prevErr != nil {
		prev = &remote.Lockfile{Bundles: map[string]remote.LockEntry{}}
	}
	// prevEntries is the previous lockfile keyed the same way the rebuild keys
	// its pins, so every field that must OUTLIVE a closure rebuild is read from
	// one place. It replaced a set of parallel per-field maps: each new field
	// that had to survive a relock meant another map and another chance to
	// forget one, and forgetting one is silent — the rebuild writes a valid
	// lockfile with the field simply gone.
	//
	// What must survive, and why:
	//   Pinned    — the user's "do not upgrade this" hold is a decision, and a
	//               relock is not entitled to reverse it.
	//   Retracted — this rebuild has no live manifest in hand, so it must never
	//               CLEAR a retraction only a fresh check (sync's own re-check,
	//               or the next Pull) is entitled to lift. Without it a relock
	//               triggered right after syncItem's installed-ref re-check
	//               (operations.checkInstalledRetraction) would drop the flag
	//               that check had just recorded.
	//   Tree      — the SHAPE that was installed. A rebuild has no fetcher and
	//               cannot re-derive it, and losing it sends BundleReader after
	//               a "<name>.yaml" the publisher never wrote, reporting
	//               "remote content not found" for a bundle sitting installed
	//               on disk.
	prevEntries := map[string]remote.LockEntry{}
	for _, e := range prev.AllEntries() {
		prevEntries[string(e.Type)+"\x00"+e.Ref] = e.Entry
	}

	lockfile := &remote.Lockfile{
		Version: 1,
		Bundles: make(map[string]remote.LockEntry),
	}
	for _, p := range pins {
		key := string(p.Type) + "\x00" + p.Identity
		// RequestedVersion records the manifest constraint so a later relock can
		// carry this SHA forward while the constraint is unchanged; Version records
		// the tag a semver constraint chose, for display and satisfaction checks.
		entry := remote.LockEntry{SHA: p.Hash, URL: p.URL, RequestedVersion: p.Constraint, Version: p.Version, Kind: p.Kind}
		if prevEntry, ok := prevEntries[key]; ok {
			entry.Pinned = prevEntry.Pinned
			entry.Retracted = prevEntry.Retracted
			entry.RetractedReason = prevEntry.RetractedReason
			entry.Tree = prevEntry.Tree
		}
		lockfile.AddEntry(p.Type, p.Identity, entry)
	}

	// An INCOMPLETE closure (a remote parent profile could not be expanded)
	// must not erase healthy entries: merge in every previous entry the rebuilt
	// closure no longer reaches, so a transient fetch failure never loses lock
	// state. The next complete relock drops genuinely-removed entries.
	if len(unexpanded) > 0 {
		preserved := 0
		for _, e := range prev.AllEntries() {
			if _, ok := lockfile.GetEntry(e.Type, e.Ref); !ok {
				lockfile.AddEntry(e.Type, e.Ref, e.Entry)
				preserved++
			}
		}
		if preserved > 0 {
			clidiag.Warn("ctxloom", "dependency closure is incomplete (%d parent profile(s) unreachable); preserving %d existing lockfile entry(ies)", len(unexpanded), preserved)
		}
	}

	if lockfile.IsEmpty() {
		return &LockDependenciesResult{
			Status:  "empty",
			Message: "No remote items found",
		}, nil
	}

	if err := lockManager.Save(lockfile); err != nil {
		return nil, err
	}

	return &LockDependenciesResult{
		Status:    "generated",
		Path:      lockManager.Path(),
		ItemCount: len(lockfile.AllEntries()),
	}, nil
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
		if _, isBad := bad[p.Identity]; !isBad {
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
// failures warn and continue — stale data beats aborting. Shared by
// UpgradeDependencies (upgrade.go) and the sync path (sync.go).
func refreshRepoCaches(ctx context.Context, cache RepoUpdater, urls []string) {
	for _, url := range urls {
		forgeType, _, err := remote.DetectForge(url)
		if err != nil {
			clidiag.Warn("ctxloom", "detect forge for %s: %v", url, err)
			continue
		}
		if _, err := cache.UpdateRepo(ctx, url, forgeType); err != nil {
			clidiag.Warn("ctxloom", "fetch %s: %v", url, err)
		}
	}
}
