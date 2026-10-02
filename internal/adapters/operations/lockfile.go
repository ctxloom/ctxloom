package operations

import (
	"context"
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/trust"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
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
}

// LockDependencies builds lock.yaml from the flattened transitive closure of
// the project's local profiles. Every reference is hash-pinned, so the closure
// is fully determined by what each item references — no resolution to "latest"
// (that is Relock/`update`). If the same item appears at two differing hashes
// anywhere in the closure that is a conflict: surfaced immediately as a hard
// error when FailOnConflict is set, else warned and the conflicted items
// dropped so startup is never blocked (CLAUDE.md).
//
// A rebuild is a writer of LockEntry.SHA like any other, so a pin it MOVES is
// held to the signed version floor its previous entry recorded (verifyAdvance).
// A move below the floor keeps the previous entry: a hard error under
// FailOnConflict, and a warning otherwise, so startup is never blocked by it.
func LockDependencies(ctx context.Context, cfg *config.Config, req LockDependenciesRequest) (*LockDependenciesResult, error) {
	fs := getFS(req.FS)
	baseDir := ProjectAppDir(cfg)

	// Run sync first so the clones the closure walk reads are present.
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
	// An UNREADABLE previous lockfile FAILS the rebuild (ruled). It may record
	// version floors, and a rebuild that started from an empty lock would hand
	// every pin it moves a floor of nothing — the rollback the floor exists to
	// refuse, decided by a read error. The corrupt file, and the holds,
	// retractions and floors still recorded in it, is left for the user to fix
	// or delete.
	prev, prevErr := lockManager.Load()
	if prevErr != nil {
		return nil, fmt.Errorf("%w: the holds, retractions and version floors in %s would be lost to a rebuild; fix or delete it: %w", remote.ErrLockfileUnreadable, lockManager.Path(), prevErr)
	}
	// prevEntries is the previous lockfile keyed the same way the rebuild keys
	// its pins, so every field that must OUTLIVE a closure rebuild is read from
	// one entry. Forgetting to carry a field is silent — the rebuild writes a
	// valid lockfile with the field simply gone — so they are carried together
	// by relockPass.add rather than one lookup per field.
	//
	// What must survive, and why:
	//   Pinned    — the user's "do not upgrade this" hold is a decision, and a
	//               relock is not entitled to reverse it.
	//   SignedVersion/Publisher — the version floor. Carried only while the
	//               pin does not move; a moved pin is re-verified against it.
	//   Retracted — this rebuild has no live manifest in hand, so it must never
	//               CLEAR a retraction only a fresh check (sync's own re-check,
	//               or the next Pull) is entitled to lift. Without it a relock
	//               triggered right after syncItem's installed-ref re-check
	//               (operations.checkInstalledRetraction) would drop the flag
	//               that check had just recorded.
	prevEntries := map[string]remote.LockEntry{}
	for _, e := range prev.AllEntries() {
		prevEntries[relockKey(e.Type, e.Ref)] = e.Entry
	}

	pass := relockPass{
		ctx: ctx, cfg: cfg, baseDir: baseDir, failOnConflict: req.FailOnConflict,
		lockfile: &remote.Lockfile{
			Version: remote.LockfileVersion,
			Bundles: make(map[trust.BundleKey]remote.LockEntry),
		},
	}
	for _, p := range pins {
		if err := pass.add(p, prevEntries); err != nil {
			return nil, err
		}
	}
	lockfile := pass.lockfile

	// An INCOMPLETE closure (a remote parent profile could not be expanded)
	// must not erase healthy entries: merge in every previous entry the rebuilt
	// closure no longer reaches, so a transient fetch failure never loses lock
	// state. The next complete relock drops genuinely-removed entries.
	if len(unexpanded) > 0 {
		preserveUnreachedEntries(prev, lockfile, len(unexpanded))
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

// relockKey keys a lock entry by type and identity.
func relockKey(t remote.ItemType, id trust.BundleKey) string {
	return string(t) + "\x00" + string(id)
}

// relockPass is one LockDependencies rebuild: the lockfile it builds, and the
// fetcher it opens only when a moved pin must be verified against its floor.
type relockPass struct {
	ctx            context.Context
	cfg            *config.Config
	baseDir        string
	failOnConflict bool
	lockfile       *remote.Lockfile
	factory        remote.FetcherFactory
	auth           remote.AuthConfig
}

// add pins p, carrying forward what its previous entry must keep. A pin that
// moves below its previous entry's signed floor keeps the previous entry
// (warned), or fails the rebuild under failOnConflict.
func (r *relockPass) add(p PinnedRef, prevEntries map[string]remote.LockEntry) error {
	// RequestedVersion records the manifest constraint so a later relock can
	// carry this SHA forward while the constraint is unchanged; Version records
	// the tag a semver constraint chose, for display and satisfaction checks.
	entry := remote.LockEntry{SHA: p.Hash, URL: p.URL, RequestedVersion: p.Constraint, Version: p.Version, Kind: p.Kind}
	prevEntry, ok := prevEntries[relockKey(p.Type, p.Identity)]
	if !ok {
		r.lockfile.AddEntry(p.Type, p.Identity, entry)
		return nil
	}
	entry.Held = prevEntry.Held
	entry.Retracted = prevEntry.Retracted
	entry.RetractedReason = prevEntry.RetractedReason
	switch {
	case prevEntry.SHA == p.Hash:
		entry.SignedVersion, entry.Publisher = prevEntry.SignedVersion, prevEntry.Publisher
	case prevEntry.SignedVersion != "":
		v, refusal := r.verify(p, prevEntry)
		if refusal != nil {
			if r.failOnConflict {
				return fmt.Errorf("refusing to move %s from %s to %s: %w", p.Identity, prevEntry.SHA, p.Hash, refusal)
			}
			clidiag.Warn("ctxloom", "keeping %s at %s rather than moving it to %s: %v", p.Identity, prevEntry.SHA, p.Hash, refusal)
			r.lockfile.AddEntry(p.Type, p.Identity, prevEntry)
			return nil
		}
		entry.SignedVersion, entry.Publisher = v.LockFields()
	}
	r.lockfile.AddEntry(p.Type, p.Identity, entry)
	return nil
}

// verify is verifyAdvance for a moved pin, opening the fetcher on first use.
func (r *relockPass) verify(p PinnedRef, prevEntry remote.LockEntry) (remote.Verified, error) {
	if r.factory == nil {
		r.auth = remote.LoadAuth(r.baseDir)
		r.factory = remote.FetcherFactory(NewCachedFetcherFactory(r.cfg))
	}
	return verifyAdvance(r.ctx, r.cfg, r.factory, r.auth, p, prevEntry, false)
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
func refreshRepoCaches(ctx context.Context, cache RepoUpdater, urls []string) (fetchFailed bool) {
	for _, url := range urls {
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
