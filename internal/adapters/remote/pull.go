package remote

import (
	"bufio"
	"context"
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// PullOptions configures pull behavior.
//
// A pull only records a dependency pin; it never exposes content to the agent.
// Whether the pulled bytes ever reach the LLM is decided later, per item, by the
// content-hash-keyed trust gate (operations.EffectiveTrust) — so pull carries no
// security-review ceremony of its own (trust-simplify slice 3).
type PullOptions struct {
	// Force skips the retracted-version confirmation prompt.
	Force bool

	// LocalDir overrides the default .ctxloom directory path.
	LocalDir string

	// ItemType specifies what type of item to pull.
	ItemType ItemType

	// RequestedVersion, when non-nil, overrides the version constraint recorded in
	// the lockfile entry (RequestedVersion) instead of deriving it from the pulled
	// ref. `update --apply` uses it to pull a constraint-bounded SHA pin
	// ("<ref>@<sha>") for the CONTENT while preserving the manifest's original
	// constraint in the lock — otherwise pinning the pull would freeze "^1.2" into a
	// concrete SHA. A non-nil pointer to "" preserves a constraint-less entry.
	RequestedVersion *string

	// AllowDowngrade accepts, for THIS pull's ref only, content signed at a
	// version below the lockfile's recorded floor (or no longer signed at all).
	// Callers set it only for refs the operator named: it is never blanket.
	// The lower version becomes the new floor.
	AllowDowngrade bool

	// Stdout and Stdin for output and input (for testing).
	Stdout io.Writer
	Stdin  io.Reader
}

// PullResult contains the result of a pull operation.
type PullResult struct {
	// LocalPath is where the item was saved. For bundles, this is a
	// synthetic "<remote>:<localName>@<sha>" string — bundle content lives
	// in the git clone cache and is read on demand via BundleReader, no
	// fs copy is written. For profiles, this is the real on-disk path.
	LocalPath string

	// SHA is the commit SHA of the fetched content.
	SHA string

	// Overwritten indicates if an existing file was replaced. Always false:
	// remote items are references, never materialized.
	Overwritten bool

	// Content holds the fetched bytes for callers that would otherwise
	// re-read from LocalPath. Populated for bundles (whose LocalPath is
	// synthetic) and also for profiles (where it equals what was written).
	Content []byte

	// Retracted reports whether THIS pull's own confirmRetraction check found
	// the item retracted in its remote manifest — true regardless of whether
	// the pull proceeded (Force / non-interactive) or the user confirmed
	// through the interactive prompt: a pull only fails on retraction when the
	// user is prompted AND declines. Callers (operations.syncItem) use this to
	// report the retraction to the user even though the pull itself succeeded.
	Retracted bool
	// RetractedReason is the publisher's stated reason, set when Retracted.
	RetractedReason string
}

// FetcherFactory creates Fetcher instances. Allows mocking for tests.
type FetcherFactory func(repoURL string, auth AuthConfig) (Fetcher, error)

// DefaultFetcherFactory creates raw API-based fetchers. It is retained for
// tests and for paths that genuinely need forge APIs (SearchRepos, publish).
// Production code paths that read content (FetchFile, ListDir, ResolveRef,
// GetDefaultBranch) must use a cached factory (see operations.NewCachedFetcherFactory)
// so we clone once per repo and never make per-call API requests.
func DefaultFetcherFactory(repoURL string, auth AuthConfig) (Fetcher, error) {
	return NewFetcher(repoURL, auth)
}

// Puller handles pulling items from remotes.
type Puller struct {
	registry        *Registry
	auth            AuthConfig
	lockfileManager *LockfileManager
	fetcherFactory  FetcherFactory
	// now is the clock resolveRetraction stamps fresh retraction verdicts
	// with, and measures persisted-verdict staleness against. A field (not a
	// bare time.Now() call) so a test can fix the clock and reach the
	// stale-verdict arm without sleeping past RetractionStaleAfter; the
	// package's own tests set it in a Puller literal, which is why there is no
	// exported option for it.
	now func() time.Time
	// treeFetch is the pinned-remote tree walker, wired in from above (see
	// TreeFetchFunc). Nil means this Puller cannot fetch a bundle at all.
	treeFetch TreeFetchFunc
	// treeInstall materializes a pinned tree as a git worktree, wired in from
	// above (see TreeInstallFunc). Nil means this Puller cannot materialize a
	// bundle at all.
	treeInstall TreeInstallFunc
	// treeVerify verifies a fetched tree before it is pinned (see
	// TreeVerifyFunc). Nil means this Puller cannot establish what it would be
	// pinning, and it refuses to install rather than pin unverified content.
	treeVerify TreeVerifyFunc
	// manifestVerify verifies the default branch's tip manifest for the
	// retraction check (see CheckRetracted). Nil means no retraction verdict
	// can be established, so every check is Unknown and fails stale.
	manifestVerify ManifestVerifyFunc
}

// PullerOption is a functional option for configuring a Puller.
type PullerOption func(*Puller)

// WithTreeFetcher supplies the pinned-remote tree walker a bundle needs.
// Without it a Puller cannot fetch a bundle, and says so.
func WithTreeFetcher(tf TreeFetchFunc) PullerOption {
	return func(p *Puller) {
		p.treeFetch = tf
	}
}

// WithTreeInstaller supplies the pinned-tree materializer a directory-form
// bundle needs (see TreeInstallFunc). Without it a Puller refuses to install a
// tree rather than recording a pin nothing can read.
func WithTreeInstaller(ti TreeInstallFunc) PullerOption {
	return func(p *Puller) {
		p.treeInstall = ti
	}
}

// WithTreeVerifier supplies the verifier a fetched tree must pass before it is
// pinned (see TreeVerifyFunc). Without it a Puller refuses to install a tree.
func WithTreeVerifier(tv TreeVerifyFunc) PullerOption {
	return func(p *Puller) {
		p.treeVerify = tv
	}
}

// WithManifestVerifier supplies the verifier the retraction check runs over the
// default branch's tip manifest (see CheckRetracted).
func WithManifestVerifier(mv ManifestVerifyFunc) PullerOption {
	return func(p *Puller) {
		p.manifestVerify = mv
	}
}

// WithLockfileManager sets a custom lockfile manager (for testing).
func WithLockfileManager(lm *LockfileManager) PullerOption {
	return func(p *Puller) {
		p.lockfileManager = lm
	}
}

// WithFetcherFactory sets a custom fetcher factory (for testing).
func WithFetcherFactory(ff FetcherFactory) PullerOption {
	return func(p *Puller) {
		p.fetcherFactory = ff
	}
}

// NewPuller creates a new puller.
// reprise:accept-drift — shares the functional-options constructor idiom with publish.go's NewPublishManager (and, in the base scan, the now-removed terminal-checker methods); the trust demolition reshaped only this file, and these are legitimately independent constructors, not co-maintained copies.
func NewPuller(registry *Registry, auth AuthConfig, opts ...PullerOption) *Puller {
	p := &Puller{
		registry:       registry,
		auth:           auth,
		fetcherFactory: DefaultFetcherFactory,
	}

	// Apply options first to allow overrides
	for _, opt := range opts {
		opt(p)
	}

	// Initialize defaults for nil dependencies (allows tests to override)
	if p.lockfileManager == nil {
		p.lockfileManager = NewLockfileManager(".ctxloom")
	}
	if p.now == nil {
		p.now = func() time.Time { return time.Now().UTC() }
	}

	return p
}

// fetchedItem carries everything resolved during the fetch phase of a Pull
// (remote, SHA, on-the-wire content) into the install phase.
type fetchedItem struct {
	rem                 *Remote
	localName           trust.BundleKey // lockfile key: the bundle identity
	sha                 string
	requestedVersion    string       // user-specified version, "" if they took the default
	resolvedVersion     string       // concrete tag a semver constraint resolved to, "" otherwise
	kind                SelectorKind // classified selector kind (sha/tag/version/branch)
	content             []byte
	tree                map[string]TreeFile // non-nil when the bundle was published in DIRECTORY form
	treeRoot            string              // the repo path that tree was fetched from, for diagnostics
	retracted           bool                // this fetch's own confirmRetraction verdict (fresh or fail-stale fallback)
	retractedReason     string              // the publisher's stated reason, when retracted
	retractionCheckedAt time.Time           // when THIS verdict was established (see LockEntry.RetractionCheckedAt)
	// checkRetraction runs confirmRetraction against the entry about to be
	// pinned. It is deferred to install because a retraction names a signed
	// VERSION, and which version is being pinned is known only once the tree
	// has been verified. Nil leaves the three fields above as they are.
	checkRetraction func(pinned LockEntry) (retracted bool, reason string, checkedAt time.Time, err error)
}

// Pull downloads an item from a remote and records its pin. It is the
// orchestrator: fetch (resolve → retraction → SHA → download), then install
// (write → lock). Each phase is a helper below so this stays readable and each
// piece is independently testable. Exposure of the pulled content to the agent
// is gated later, per item, by the content-hash trust gate — pull itself only
// pins.
func (p *Puller) Pull(ctx context.Context, refStr string, opts PullOptions) (*PullResult, error) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}

	ref, err := ParseReference(refStr)
	if err != nil {
		return nil, fmt.Errorf("invalid reference: %w", err)
	}

	item, err := p.fetchForPull(ctx, ref, opts)
	if err != nil {
		return nil, err
	}

	return p.installPulledItem(ctx, ref, opts, item)
}

// CheckRetraction reports whether refStr is CURRENTLY retracted by its
// publisher's newest signed release, without pulling content or writing any
// pin. The version it asks about is the one the lockfile pinned. It is the
// lightweight counterpart to the retraction check a full Pull already runs
// (confirmRetraction) — for a ref operations.syncItem finds ALREADY installed,
// where a full Pull would be needless (re-fetch content that hasn't changed,
// rewrite a SHA that hasn't moved) just to learn whether the publisher
// retracted it since the last sync. See operations.RetractionChecker, the
// seam syncItem consults this through.
func (p *Puller) CheckRetraction(ctx context.Context, refStr string, itemType ItemType) (retracted bool, reason string, checkedAt time.Time, err error) {
	ref, err := ParseReference(refStr)
	if err != nil {
		return false, "", time.Time{}, fmt.Errorf("invalid reference: %w", err)
	}
	repoURL, _, _, err := p.resolveRemoteTarget(ref)
	if err != nil {
		return false, "", time.Time{}, err
	}
	fetcher, err := p.fetcherFactory(repoURL, p.auth)
	if err != nil {
		return false, "", time.Time{}, fmt.Errorf("failed to create fetcher: %w", err)
	}
	owner, repo, err := ParseOwnerRepo(repoURL)
	if err != nil {
		return false, "", time.Time{}, fmt.Errorf("invalid remote URL: %w", err)
	}
	key, err := ref.LockKey()
	if err != nil {
		return false, "", time.Time{}, fmt.Errorf("invalid reference %s: %w", refStr, err)
	}
	var pinned LockEntry
	if lock, lerr := p.lockfileManager.Load(); lerr == nil {
		pinned, _ = lock.GetEntry(itemType, key)
	}
	return p.resolveRetraction(ctx, fetcher, owner, repo, ref, itemType, key, pinned)
}

// RecordRetraction persists retracted/reason onto refStr's EXISTING lockfile
// entry (loading, mutating, saving) — the write half of the already-installed
// re-check CheckRetraction reads. A no-op when refStr has no lockfile entry
// yet (nothing pinned, nothing to mark) and when the recorded status already
// matches (no redundant disk write on every sync). This is deliberately NOT
// folded into updateLockfile: that path always has a freshly-fetched SHA to
// write alongside; this one mutates an entry that pull isn't touching at all.
func (p *Puller) RecordRetraction(itemType ItemType, refStr string, retracted bool, reason string, checkedAt time.Time) error {
	ref, err := ParseReference(refStr)
	if err != nil {
		return fmt.Errorf("invalid reference: %w", err)
	}
	localName, err := ref.LockKey()
	if err != nil {
		return fmt.Errorf("invalid reference %s: %w", refStr, err)
	}

	lockfile, err := p.lockfileManager.Load()
	if err != nil {
		return fmt.Errorf("failed to load lockfile: %w", err)
	}
	entry, ok := lockfile.GetEntry(itemType, localName)
	if !ok {
		return nil
	}
	if entry.Retracted == retracted && entry.RetractedReason == reason && entry.RetractionCheckedAt.Equal(checkedAt) {
		return nil
	}
	entry.Retracted = retracted
	entry.RetractedReason = reason
	// A zero checkedAt means the caller had nothing to stamp this with (no
	// fresh check, no prior fallback verdict either) — leave whatever was
	// already on disk alone rather than erasing a real timestamp with a
	// meaningless zero one.
	if !checkedAt.IsZero() {
		entry.RetractionCheckedAt = checkedAt
	}
	lockfile.AddEntry(itemType, localName, entry)
	return p.lockfileManager.Save(lockfile)
}

// fetchForPull resolves the remote and the SHA and fetches the content —
// everything needed before verifying and writing the pin. The retraction check
// is armed here and run at install (see fetchedItem.checkRetraction).
func (p *Puller) fetchForPull(ctx context.Context, ref *Reference, opts PullOptions) (*fetchedItem, error) {
	repoURL, rem, localName, err := p.resolveRemoteTarget(ref)
	if err != nil {
		return nil, err
	}

	fetcher, err := p.fetcherFactory(repoURL, p.auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create fetcher: %w", err)
	}

	owner, repo, err := ParseOwnerRepo(repoURL)
	if err != nil {
		return nil, fmt.Errorf("invalid remote URL: %w", err)
	}

	sha, requestedVersion, resolvedVersion, kind, err := resolveContentSHA(ctx, fetcher, owner, repo, ref)
	if err != nil {
		return nil, err
	}

	filePath := ref.BuildFilePath(opts.ItemType)
	content, tree, treeRoot, err := p.fetchItemBytes(ctx, fetcher, owner, repo, repoURL, ref, filePath, sha)
	if err != nil {
		return nil, err
	}

	return &fetchedItem{
		rem: rem, localName: localName, sha: sha, requestedVersion: requestedVersion,
		resolvedVersion: resolvedVersion, kind: kind, content: content,
		tree: tree, treeRoot: treeRoot,
		checkRetraction: func(pinned LockEntry) (bool, string, time.Time, error) {
			return p.confirmRetraction(ctx, fetcher, owner, repo, ref, localName, opts, pinned)
		},
	}, nil
}

// fetchItemBytes reads the bundle TREE at filePath at sha, returning its
// manifest's bytes together with the whole tree and the root it answered from.
func (p *Puller) fetchItemBytes(ctx context.Context, fetcher Fetcher, owner, repo, repoURL string, ref *Reference, filePath, sha string) (content []byte, tree map[string]TreeFile, treeRoot string, err error) {
	if p.treeFetch == nil {
		return nil, nil, "", fmt.Errorf("failed to fetch %s: this puller has no tree fetcher wired in", ref.String())
	}
	tree, treeRoot, terr := ProbeBundleTreeRoots(filePath, func(root string) (map[string]TreeFile, error) {
		return p.treeFetch(ctx, fetcher, owner, repo, root, sha, repoURL)
	})
	if terr != nil {
		return nil, nil, "", fmt.Errorf("failed to fetch the bundle tree at %s: %w", treeRoot, terr)
	}
	manifest, ok := TreeManifest(tree)
	if !ok {
		return nil, nil, "", fmt.Errorf("refusing to pull %s: the directory %s exists at %s but carries no %s, so nothing can load it as a bundle (it has %d file(s))",
			ref.String(), treeRoot, sha, BundleManifestName, len(tree))
	}
	if len(manifest) == 0 {
		return nil, nil, "", fmt.Errorf("refusing to pull %s: %s is empty at %s", ref.String(), treeRepoPath(treeRoot, BundleManifestName), sha)
	}
	return manifest, tree, treeRoot, nil
}

// resolveRemoteTarget maps a reference to its repo URL, remote, and lockfile
// local-name. Canonical refs auto-register the remote by URL; plain refs look
// it up in the registry.
func (p *Puller) resolveRemoteTarget(ref *Reference) (repoURL string, rem *Remote, localName trust.BundleKey, err error) {
	if !ref.IsCanonical() {
		return "", nil, "", fmt.Errorf("not a canonical reference: %s", ref.String())
	}
	repoURL = ref.URL
	rem, err = p.registry.GetOrCreateByURL(repoURL)
	if err != nil {
		return "", nil, "", fmt.Errorf("failed to register remote: %w", err)
	}
	// The lockfile key is the bundle's identity — the key a retraction is
	// looked up by — while repoURL stays the address as typed, which is the
	// transport the user chose.
	localName, err = ref.LockKey()
	if err != nil {
		return "", nil, "", fmt.Errorf("invalid reference %s: %w", ref.String(), err)
	}
	return repoURL, rem, localName, nil
}

// confirmRetraction checks whether ref is retracted, warns, and (unless
// forced) prompts for confirmation — a declined prompt cancels the pull.
// Non-interactive callers (sync, batch update) pass Force so a prompt never
// blocks on a stdin nobody answers.
//
// It ALWAYS reports its retraction verdict back to the caller (retracted,
// reason), regardless of which branch below returns: this is what lets
// installPulledItem persist the verdict into the lockfile even on the
// Force=true / non-interactive path, where the warning prints but nothing was
// previously recorded anywhere — the gap that left a forced/sync re-pull of
// retracted content just as exposed as before.
//
// pinned is the entry about to be recorded: its SignedVersion is the version
// the publisher's retractions are matched against.
func (p *Puller) confirmRetraction(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference, localName trust.BundleKey, opts PullOptions, pinned LockEntry) (retracted bool, reason string, checkedAt time.Time, err error) {
	// The determination failure is NOT discarded. CheckRetracted's
	// error slot is useless if the caller drops it: an "I could not determine
	// this" would otherwise carry on indistinguishably from "clean", which is
	// the exposure the retraction channel exists to prevent. A pull whose
	// retraction status is unknown does not proceed — not even under Force,
	// which waives the publisher's WARNING, not the check itself.
	//
	// resolveRetraction handles the fetch-failure half:
	// it falls back to the last verdict this project itself recorded for
	// localName rather than treating an unreachable remote as "clean" —
	// only a genuinely unparseable manifest still reaches err here.
	retracted, reason, checkedAt, err = p.resolveRetraction(ctx, fetcher, owner, repo, ref, opts.ItemType, localName, pinned)
	if err != nil {
		return false, "", time.Time{}, err
	}
	if !retracted {
		return false, "", checkedAt, nil
	}
	_, _ = fmt.Fprintf(opts.Stdout, "\n⚠️  WARNING: This version has been retracted!\n")
	_, _ = fmt.Fprintf(opts.Stdout, "Reason: %s\n\n", reason)
	if opts.Force {
		return true, reason, checkedAt, nil
	}
	confirmed, cerr := promptConfirmation(opts.Stdout, opts.Stdin, "Continue anyway?")
	if cerr != nil {
		return true, reason, checkedAt, cerr
	}
	if !confirmed {
		return true, reason, checkedAt, fmt.Errorf("installation cancelled: version retracted: %w", errs.ErrCancelled)
	}
	return true, reason, checkedAt, nil
}

// resolveRetraction determines refStr/ref's retraction status for THIS call:
// CheckRetracted's live verdict when the remote answered, or — when it could
// not (RetractionUnknown) — the LAST verdict this project itself recorded for
// localName, if any. This is the fail-stale fix for the fetch-failure half
// (the parse-failure half was already fixed and is
// untouched here: a hard error from CheckRetracted still propagates as an
// error, never falls back).
//
// checkedAt reports when the RETURNED verdict was actually established: p.now()
// for a fresh verdict, or the persisted entry's own RetractionCheckedAt for a
// fallback — NEVER p.now() for a fallback, since bumping it would erase the
// staleness signal the next fallback needs (see LockEntry.RetractionCheckedAt
// and RecordRetraction, which treats a zero checkedAt as "leave the persisted
// timestamp alone"). checkedAt is the zero time when there is nothing to fall
// back to at all (no existing lockfile entry for localName).
//
// Falling back to a STALE verdict — older than RetractionStaleAfter, or with
// no recorded check time at all (unknown age: an entry written before this
// field existed, or one that has simply never had a manifest read
// successfully) — warns via clidiag, matching the rest of this package's
// fault-tolerant-but-not-silent diagnostics. Falling back with NOTHING
// recorded resolves to Clean, un-warned: that is overwhelmingly the ordinary
// "this remote publishes no manifest" case (see CheckRetracted's doc), not
// evidence of an outage, and there is no verdict whose age could even be
// reported.
//
// Two more rules keep a signed retraction from being undone by whoever controls
// the repository:
//
//   - STICKY: a Clean tip never lifts a retraction already recorded for the
//     same pinned version. A retraction names an exact signed version and is
//     permanent; a tip that no longer lists it — a rewind to the release that
//     preceded it, which is still genuinely signed — is not the publisher
//     changing their mind. Only moving the pin (a new entry) resets it.
//   - ROLLBACK: a signed tip below the pinned version is reported and treated
//     like Unknown: the recorded verdict stands.
func (p *Puller) resolveRetraction(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference, itemType ItemType, localName trust.BundleKey, pinned LockEntry) (retracted bool, reason string, checkedAt time.Time, err error) {
	verdict, reason, err := CheckRetracted(ctx, fetcher, owner, repo, ref, pinned, p.manifestVerify)
	if err != nil {
		return false, "", time.Time{}, err
	}
	recorded, hasRecorded := p.recordedEntry(itemType, localName)
	if isRetracted, why, answered := freshRetraction(verdict, reason, recorded, hasRecorded, pinned); answered {
		return isRetracted, why, p.now(), nil
	}
	if verdict == RetractionRollback {
		clidiag.Warn("ctxloom", "%s: %s — the repository may have been rolled back; keeping the retraction verdict last recorded for it", localName, reason)
	}

	if !hasRecorded {
		// Never previously checked at all (or the lockfile is unreadable):
		// there is no verdict to fall back to, and this is far more often
		// "this remote publishes no signed release" than a first-pull outage.
		// See the doc above.
		return false, "", time.Time{}, nil
	}
	entry := recorded
	p.warnStaleFallback(entry, localName, owner, repo)
	return entry.Retracted, entry.RetractedReason, entry.RetractionCheckedAt, nil
}

// recordedEntry is the lockfile's entry for the item; false when there is
// none or the lockfile cannot be read.
func (p *Puller) recordedEntry(itemType ItemType, localName trust.BundleKey) (LockEntry, bool) {
	lockfile, err := p.lockfileManager.Load()
	if err != nil {
		return LockEntry{}, false
	}
	return lockfile.GetEntry(itemType, localName)
}

// freshRetraction is the verdict when the check answered: retracted as
// found, or clean — unless the same pin was already recorded retracted,
// which a clean re-check never lifts. answered is false for a rollback or
// an unknown, which fall back to the recorded verdict.
func freshRetraction(verdict RetractionVerdict, reason string, recorded LockEntry, hasRecorded bool, pinned LockEntry) (retracted bool, why string, answered bool) {
	switch verdict {
	case RetractionRetracted:
		return true, reason, true
	case RetractionClean:
		samePin := hasRecorded && recorded.SignedVersion == pinned.SignedVersion
		if samePin && recorded.Retracted {
			return true, recorded.RetractedReason, true
		}
		return false, "", true
	}
	return false, "", false
}

// warnStaleFallback warns when the recorded verdict being fallen back to is
// of unknown age or older than the freshness window.
func (p *Puller) warnStaleFallback(entry LockEntry, localName trust.BundleKey, owner, repo string) {
	unknownAge := entry.RetractionCheckedAt.IsZero()
	age := p.now().Sub(entry.RetractionCheckedAt)
	if unknownAge || age > RetractionStaleAfter {
		// Do NOT assert unreachability here. An Unknown verdict is ambiguous by
		// construction (see CheckRetracted's doc): "this remote publishes no
		// manifest" — the ordinary case, most do not — is indistinguishable at
		// that seam from a genuine outage. Naming only the outage sent users
		// hunting a network fault that did not exist: a fresh init emitted one
		// of these per lock entry while `git ls-remote` reached both remotes
		// fine, and the production fetcher reads a local clone with no network
		// I/O at all, so the claimed cause could not even apply on that path.
		if unknownAge {
			clidiag.Warn("ctxloom",
				"could not re-check whether %s is retracted against %s/%s (that remote may publish no retraction manifest, or it could not be read); falling back to a previously recorded verdict of UNKNOWN AGE (recorded before this project tracked check times) — its retraction status may be out of date",
				localName, owner, repo)
		} else {
			clidiag.Warn("ctxloom",
				"could not re-check whether %s is retracted against %s/%s (that remote may publish no retraction manifest, or it could not be read); falling back to the verdict last confirmed %s ago (older than the %s freshness window) — its retraction status may be out of date",
				localName, owner, repo, age.Round(time.Hour), RetractionStaleAfter)
		}
	}
}

// resolveContentSHA resolves the commit SHA to fetch through the constraint
// resolver: a semver range ("^1.2") resolves to the highest satisfying tag, a
// branch/tag/SHA to itself, and an empty version to the default branch's tip.
// Feeding the raw constraint to ResolveRef instead treated a semver range as a
// literal git ref (go-git even reads a leading "^" as a parent operator), so a
// range that resolved fine through the lock/upgrade path failed plain pull.
// requestedVersion echoes what the user asked for ("" if they took the
// default), recorded in the lockfile for export reconstruction. resolvedVersion
// is the concrete tag a semver constraint matched (e.g. "v1.3.0"), empty for a
// branch/tag/SHA/default pull; it is recorded as LockEntry.Version, matching the
// lock/upgrade paths.
func resolveContentSHA(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference) (sha, requestedVersion, resolvedVersion string, kind SelectorKind, err error) {
	requestedVersion = ref.ContentVersion
	res, err := ResolveConstraint(ctx, requestedVersion, NewFetcherRepoVersions(fetcher, owner, repo))
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to resolve version %q: %w", requestedVersion, err)
	}
	return res.SHA, requestedVersion, res.Version, res.Kind, nil
}

// installPulledItem records a pulled remote item (synthetic path — nothing is
// materialized) and writes its lockfile entry. Dependencies are NOT cascaded:
// every reference is hash-pinned, so the dependency closure is determined by
// lock walking the refs (operations.FlattenDependencies), not by pull.
func (p *Puller) installPulledItem(ctx context.Context, ref *Reference, opts PullOptions, item *fetchedItem) (*PullResult, error) {
	content := item.content

	// A bundle is a tree — a package: multi-file, mode-bearing, and read by
	// machinery (skill materialization, hook enumeration) that takes a real
	// directory, not bytes. So the checked-out worktree is the only LocalPath a
	// bundle ever gets. The
	// checkout lands in the CACHE (gitignored, regenerable): the pin in the
	// lockfile stays the authority, and the worktree is checked out from it.
	// The commit that is CHECKED OUT and the commit that is RECORDED must be
	// one commit. Resolving the hold here rather than only at the lockfile write
	// is what stops a forced pull from advancing the bytes past a pin the hold
	// is successfully defending.
	requestedVersion := item.requestedVersion
	if opts.RequestedVersion != nil {
		// Caller pins the content SHA but wants the manifest constraint preserved
		// (see PullOptions.RequestedVersion).
		requestedVersion = *opts.RequestedVersion
	}
	installSHA := item.sha
	var signed Verified
	pinned := LockEntry{}
	if frozen, ok := p.heldPin(opts.ItemType, item.localName, requestedVersion); ok {
		// A held pin installs its OWN commit, which was admitted when it was
		// pinned; the freshly fetched tree is not what lands, so it is not what
		// is judged.
		installSHA = frozen.SHA
		pinned = frozen
	} else {
		v, err := p.admitTree(ctx, opts, item)
		if err != nil {
			return nil, err
		}
		signed = v
		pinned.SignedVersion, pinned.Publisher = v.LockFields()
	}
	pinned.SHA = installSHA
	if item.checkRetraction != nil {
		r, why, at, err := item.checkRetraction(pinned)
		if err != nil {
			return nil, err
		}
		item.retracted, item.retractedReason, item.retractionCheckedAt = r, why, at
	}

	localPath, werr := p.installTree(ctx, ref, opts, item, installSHA)
	if werr != nil {
		return nil, werr
	}

	// Update lockfile with provenance (local name as key). For bundles, the
	// lockfile is the *only* on-disk record — read sites resolve content via
	// the SHA recorded here, and this is also where THIS pull's own fresh
	// retraction verdict gets persisted. A failed write here used
	// to be demoted to a printed warning while Pull still returned success —
	// so a pull whose sole persistent record failed to write reported a SHA
	// and LocalPath for a pin that does not exist on disk, and on a retracted
	// item silently dropped the Retracted verdict, leaving
	// operations.EffectiveTrust with nothing to withhold against. The lockfile
	// is the only record; its write failing means the pull failed.
	// hadExisting reports whether localName already had a lockfile entry
	// BEFORE this write — i.e. this pull replaced an existing pin rather than
	// creating a new one. It is the real signal for "updated" vs "installed"
	// (PullResult.Overwritten used to be hard-coded false, making
	// operations/sync.go's "updated" status unreachable).
	hadExisting, err := p.updateLockfile(item.localName, opts, item.rem, installSHA, requestedVersion, item.resolvedVersion, item.kind, item.retracted, item.retractedReason, item.retractionCheckedAt, signed)
	if err != nil {
		return nil, fmt.Errorf("pulled %s but failed to record its lockfile pin (the only on-disk record of this pull): %w", item.localName, err)
	}

	return &PullResult{
		LocalPath:       localPath,
		SHA:             installSHA,
		Overwritten:     hadExisting,
		Content:         content,
		Retracted:       item.retracted,
		RetractedReason: item.retractedReason,
	}, nil
}

// admitTree verifies the fetched tree and holds it to the entry's version
// floor, BEFORE anything is checked out or written.
//
// Verification used to belong only to readers, so a pull pinned whatever the
// ref resolved to and the check came later, at a read that could at most
// withhold. That let whoever controls a repository move a ref back to an older
// tree its publisher really did sign — every signature verifies — and the pin
// followed it. The floor is what refuses that: a trusted publisher's signed
// version, recorded at the last pin, below which this ref does not move unless
// the operator names it (PullOptions.AllowDowngrade).
//
// An unreadable lockfile refuses too: it may hold the floor, and a floor that
// cannot be read is not one that can be honoured.
func (p *Puller) admitTree(ctx context.Context, opts PullOptions, item *fetchedItem) (Verified, error) {
	if p.treeVerify == nil {
		return Verified{}, fmt.Errorf("refusing to install %q: this puller has no tree verifier wired in, so it cannot establish what it would be pinning", item.localName)
	}
	v, err := p.treeVerify(ctx, item.tree, item.treeRoot, item.sha, item.rem.URL)
	if err != nil {
		return Verified{}, fmt.Errorf("refusing to install %s at %s: %w", item.localName, item.sha, err)
	}
	lock, err := p.lockfileManager.Load()
	if err != nil {
		return Verified{}, fmt.Errorf("refusing to install %s: the lockfile that records its version floor cannot be read: %w", item.localName, err)
	}
	prior, ok := lock.GetEntry(opts.ItemType, item.localName)
	if !ok {
		return v, nil
	}
	if err := AdmitSignedVersion(opts.Stdout, string(item.localName), prior, v, opts.AllowDowngrade); err != nil {
		return Verified{}, fmt.Errorf("refusing to install %s at %s: %w", item.localName, item.sha, err)
	}
	return v, nil
}

// installTree materializes the pinned directory-form bundle as a git worktree
// and returns the directory it landed in.
//
// NOTHING IS COPIED. The tree is checked out by git, detached at the pinned
// commit, so the pin and the bytes are a single fact rather than two states an
// interleaving can pull apart. The fetched tree is still what DECIDES the pull
// — its manifest is what was verified above — it is simply not what gets
// written.
func (p *Puller) installTree(ctx context.Context, ref *Reference, opts PullOptions, item *fetchedItem, sha string) (string, error) {
	if p.treeInstall == nil {
		return "", fmt.Errorf("refusing to install %q: this puller has no tree installer wired in, so the bundle would be pinned in the lockfile with no tree any reader could reach", item.localName)
	}
	baseDir := opts.LocalDir
	if baseDir == "" {
		baseDir = p.lockfileManager.BaseDir()
	}
	// The root the fetch PROBE answered from and the one every reader COMPUTES
	// must name one directory, or the tree is checked out where nothing looks —
	// the silent half-install this refuses to create. They can only differ while
	// two bundle formats are live at once (see BundleTreeRoots), and then it is
	// the publisher, not the consumer, who can end it.
	if want := ref.TreeRepoPath(); item.treeRoot != want {
		return "", fmt.Errorf("refusing to install %q: its tree was found at %s in the repository but every reader resolves it at %s, "+
			"so a checkout of the found path would land where nothing looks; the publisher must republish it at %s",
			item.localName, item.treeRoot, want, want)
	}
	worktree, err := ref.LocalWorktreePath(baseDir)
	if err != nil {
		return "", fmt.Errorf("install %s: %w", item.localName, err)
	}
	dir, err := p.treeInstall(ctx, item.rem.URL, sha, item.treeRoot, worktree)
	if err != nil {
		return "", fmt.Errorf("install %s at %s: %w", item.localName, sha, err)
	}
	return dir, nil
}

// promptConfirmation asks the user for yes/no confirmation.
// Default is NO - user must explicitly type 'y' or 'yes'.
func promptConfirmation(w io.Writer, r io.Reader, prompt string) (bool, error) {
	_, _ = fmt.Fprintf(w, "%s [y/N]: ", prompt)

	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		return false, nil // EOF = no
	}

	response := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return response == "y" || response == "yes", nil
}

// heldPin reports the entry a HELD dependency freezes this pull to.
//
// A hold says "do not advance this", and it has to be answered BEFORE the tree
// is checked out — not only when the lockfile is written. The checkout is what
// a consumer actually reads, so a hold that defends the recorded SHA while the
// worktree moves to the freshly resolved one leaves the lockfile and the bytes
// disagreeing: the hold protects the pin and not the content the pin names,
// which is the one thing a hold exists to do.
//
// A hold is only frozen on a BLANKET pull. An explicitly requested version is
// the user naming a target for this invocation, which a hold does not override.
func (p *Puller) heldPin(itemType ItemType, localName trust.BundleKey, requestedVersion string) (LockEntry, bool) {
	if requestedVersion != "" {
		return LockEntry{}, false
	}
	lockfile, err := p.lockfileManager.Load()
	if err != nil {
		return LockEntry{}, false
	}
	existing, ok := lockfile.GetEntry(itemType, localName)
	if !ok || !existing.Held {
		return LockEntry{}, false
	}
	return existing, true
}

// updateLockfile records provenance in the (active) lockfile. Every pull writes
// straight to the active lock — there is no pending-review split anymore;
// whether the pulled content ever reaches the agent is decided per item by the
// content-hash trust gate, not by which lockfile the pin lives in.
//
// retracted/retractedReason/retractionCheckedAt are THIS pull's own
// confirmRetraction verdict — a FRESH read of the live manifest when the
// remote answered, or a fail-stale FALLBACK to the previously persisted
// verdict (unchanged, timestamp and all) when it did not (see
// resolveRetraction) — persisted here so operations.EffectiveTrust can
// withhold exposure later without a network call of its own (see
// operations.RetractionRecords).
//
// hadExisting reports whether localName already had a lockfile entry before
// this write — the caller (installPulledItem) surfaces it as
// PullResult.Overwritten.
func (p *Puller) updateLockfile(localName trust.BundleKey, opts PullOptions, remote *Remote, sha string, requestedVersion, resolvedVersion string, kind SelectorKind, retracted bool, retractedReason string, retractionCheckedAt time.Time, signed Verified) (hadExisting bool, err error) {
	itemType := opts.ItemType
	target := p.lockfileManager
	lockfile, err := target.Load()
	if err != nil {
		return false, fmt.Errorf("failed to load lockfile: %w", err)
	}

	existing, hadExisting := lockfile.GetEntry(itemType, localName)

	entry := LockEntry{
		SHA:                 sha,
		URL:                 remote.URL,
		RequestedVersion:    requestedVersion,
		Version:             resolvedVersion,
		Kind:                kind,
		FetchedAt:           time.Now().UTC(),
		Retracted:           retracted,
		RetractedReason:     retractedReason,
		RetractionCheckedAt: retractionCheckedAt,
	}
	entry.SignedVersion, entry.Publisher = signed.LockFields()

	// A hold ("do not upgrade this") is a deliberate decision; a content re-pull
	// must never silently clear it. Always carry the flag forward, and on a
	// blanket pull (no explicit version requested, e.g. `deps pull --force`)
	// keep the entry's frozen SHA/Version too — force repairs a clone, it does
	// not advance past a hold (see LockEntry.Pinned).
	if hadExisting && existing.Held {
		entry.Held = true
		if frozen, ok := p.heldPin(itemType, localName, requestedVersion); ok {
			entry.SHA = frozen.SHA
			entry.Version = frozen.Version
			entry.RequestedVersion = frozen.RequestedVersion
			entry.Kind = frozen.Kind
			entry.SignedVersion = frozen.SignedVersion
			entry.Publisher = frozen.Publisher
		}
	}

	lockfile.AddEntry(itemType, localName, entry)

	if err := target.Save(lockfile); err != nil {
		return hadExisting, fmt.Errorf("failed to save lockfile: %w", err)
	}

	return hadExisting, nil
}
