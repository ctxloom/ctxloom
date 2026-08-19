package remote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
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
	// bare time.Now() call) so tests can inject a fixed/advancing clock
	// instead of sleeping past RetractionStaleAfter (see WithPullerClock).
	now func() time.Time
	// treeFetch is the pinned-remote tree walker, wired in from above (see
	// TreeFetchFunc). Nil means this Puller can fetch only single-file bundles,
	// which is what every Puller could do before the seam existed.
	treeFetch TreeFetchFunc
}

// PullerOption is a functional option for configuring a Puller.
type PullerOption func(*Puller)

// WithTreeFetcher supplies the pinned-remote tree walker a directory-form
// bundle needs. Without it a Puller fetches single-file bundles only — the
// behaviour every Puller had before this seam — so omitting it degrades to the
// old capability rather than to a half-installed tree.
func WithTreeFetcher(tf TreeFetchFunc) PullerOption {
	return func(p *Puller) {
		p.treeFetch = tf
	}
}

// WithLockfileManager sets a custom lockfile manager (for testing).
func WithLockfileManager(lm *LockfileManager) PullerOption {
	return func(p *Puller) {
		p.lockfileManager = lm
	}
}

// WithPullerClock sets the clock resolveRetraction uses to stamp fresh
// verdicts and measure persisted-verdict staleness (for testing — production
// always takes the default, real time.Now().UTC()).
func WithPullerClock(now func() time.Time) PullerOption {
	return func(p *Puller) {
		p.now = now
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
	localName           string // lockfile key, "remote/path"
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

// CheckRetraction reports whether refStr is CURRENTLY retracted in its
// remote's manifest, without pulling content or writing any pin. It is the
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
	return p.resolveRetraction(ctx, fetcher, owner, repo, ref, itemType, ref.LockKey())
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
	localName := ref.LockKey()

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

// fetchForPull resolves the remote, checks retraction, resolves the SHA, and
// fetches the content — everything needed before writing the pin.
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

	retracted, retractedReason, retractionCheckedAt, err := p.confirmRetraction(ctx, fetcher, owner, repo, ref, localName, opts)
	if err != nil {
		return nil, err
	}

	sha, requestedVersion, resolvedVersion, kind, err := resolveContentSHA(ctx, fetcher, owner, repo, ref)
	if err != nil {
		return nil, err
	}

	filePath := ref.BuildFilePath(opts.ItemType)
	content, tree, treeRoot, err := p.fetchItemBytes(ctx, fetcher, owner, repo, repoURL, ref, filePath, sha, opts)
	if err != nil {
		return nil, err
	}

	return &fetchedItem{
		rem: rem, localName: localName, sha: sha, requestedVersion: requestedVersion,
		resolvedVersion: resolvedVersion, kind: kind, content: content,
		tree: tree, treeRoot: treeRoot,
		retracted: retracted, retractedReason: retractedReason, retractionCheckedAt: retractionCheckedAt,
	}, nil
}

// fetchItemBytes reads the item at filePath, falling back to its DIRECTORY form
// when the single file is absent.
//
// The single file is tried FIRST and the tree only on a genuine
// not-found. That ordering is not a preference between the two shapes: it is
// what makes the addition invisible to every publisher who already ships single
// files. A tree probe in front would issue an extra listing on every pull in the
// world to serve the rarer case, and — worse — would let a stray directory
// beside a real bundle.yaml decide which of the two a pull installed.
//
// A fetcher error that is NOT "not found" propagates unchanged. Falling through
// to a tree probe on an auth failure or a transport error would convert one
// diagnosable error into a second, more confusing one about a directory nobody
// asked for.
func (p *Puller) fetchItemBytes(ctx context.Context, fetcher Fetcher, owner, repo, repoURL string, ref *Reference, filePath, sha string, opts PullOptions) (content []byte, tree map[string]TreeFile, treeRoot string, err error) {
	content, fileErr := fetcher.FetchFile(ctx, owner, repo, filePath, sha)
	switch {
	case fileErr == nil:
		// A zero-byte remote file must never be pinned as a successful install:
		// the lockfile entry installPulledItem is about to write would
		// otherwise report a real SHA and "installed" status for content that
		// is empty — a silent no-op indistinguishable from a genuine pull.
		if len(content) == 0 {
			return nil, nil, "", fmt.Errorf("refusing to pull %s: remote file %s is empty at %s", ref.String(), filePath, sha)
		}
		return content, nil, "", nil
	case !errors.Is(fileErr, errs.ErrRemoteContentNotFound):
		return nil, nil, "", fmt.Errorf("failed to fetch: %w", fileErr)
	case opts.ItemType != ItemTypeBundle:
		// Only bundles have a directory form. Anything else that is missing is
		// simply missing, and must say so rather than reporting a tree gap.
		return nil, nil, "", fmt.Errorf("failed to fetch: %w", fileErr)
	case p.treeFetch == nil:
		// No walker was wired in, so this Puller genuinely cannot tell whether a
		// tree is there. Say that, rather than reporting the file's absence as
		// the whole story — a bare "not found" against a repo that DOES publish
		// the directory form is the diagnostic that cost this capability its
		// first attempt.
		return nil, nil, "", fmt.Errorf("failed to fetch: %w (and this puller has no tree fetcher wired in, so %s could not be checked for a directory-form bundle)",
			fileErr, BundleTreeRoot(filePath))
	}

	treeRoot = BundleTreeRoot(filePath)
	tree, terr := p.treeFetch(ctx, fetcher, owner, repo, treeRoot, sha, repoURL)
	if terr != nil {
		// Quote BOTH failures. Either one alone is misleading: the file error
		// alone hides that a directory form was looked for, and the tree error
		// alone reads as though the directory were the only shape a bundle has.
		return nil, nil, "", fmt.Errorf("failed to fetch: neither %s (%v) nor the directory-form bundle at %s: %w", filePath, fileErr, treeRoot, terr)
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
func (p *Puller) resolveRemoteTarget(ref *Reference) (repoURL string, rem *Remote, localName string, err error) {
	if !ref.IsCanonical() {
		return "", nil, "", fmt.Errorf("not a canonical reference: %s", ref.String())
	}
	repoURL = ref.URL
	rem, err = p.registry.GetOrCreateByURL(repoURL)
	if err != nil {
		return "", nil, "", fmt.Errorf("failed to register remote: %w", err)
	}
	// Lockfile key is the fetch address: which repository, which path.
	return repoURL, rem, ref.LockKey(), nil
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
func (p *Puller) confirmRetraction(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference, localName string, opts PullOptions) (retracted bool, reason string, checkedAt time.Time, err error) {
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
	retracted, reason, checkedAt, err = p.resolveRetraction(ctx, fetcher, owner, repo, ref, opts.ItemType, localName)
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
func (p *Puller) resolveRetraction(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference, itemType ItemType, localName string) (retracted bool, reason string, checkedAt time.Time, err error) {
	verdict, reason, err := CheckRetracted(ctx, fetcher, owner, repo, ref, itemType)
	if err != nil {
		return false, "", time.Time{}, err
	}
	if verdict != RetractionUnknown {
		return verdict == RetractionRetracted, reason, p.now(), nil
	}

	lockfile, lerr := p.lockfileManager.Load()
	if lerr != nil {
		// Can't even read the local fallback source — nothing to go on.
		return false, "", time.Time{}, nil
	}
	entry, ok := lockfile.GetEntry(itemType, localName)
	if !ok {
		// Never previously checked at all: there is no verdict to fall back
		// to, and this is far more often "this remote has no manifest" than a
		// first-pull outage. See the doc above.
		return false, "", time.Time{}, nil
	}

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
	return entry.Retracted, entry.RetractedReason, entry.RetractionCheckedAt, nil
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

	// Remote bundles AND profiles are pure references: the git clone cache +
	// lockfile pair is the storage, and reads at the locked SHA go through
	// remote.BundleReader / remote.ProfileReader. Nothing is materialized to
	// disk, so LocalPath is a synthetic informational string and a pull never
	// overwrites a local file (this used to be a separate
	// writePulledContent method whose only real parameters were localName and
	// sha).
	localPath := fmt.Sprintf("<remote>:%s@%s", item.localName, item.sha)

	// A DIRECTORY-form bundle is the one exception to "nothing is
	// materialized". A single-file bundle is one blob a reader can pull out of
	// the clone's object store on demand; a tree is a package — multi-file,
	// mode-bearing, and read by machinery (skill materialization, hook
	// enumeration) that takes a real directory, not bytes. Serving that from an
	// object store would mean re-deriving a filesystem on every read. The
	// install root is the CACHE (gitignored, regenerable): the pin in the
	// lockfile stays the authority, and this tree is derived from it.
	if item.tree != nil {
		dir, werr := p.installTree(ref, opts, item)
		if werr != nil {
			return nil, werr
		}
		localPath = dir
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
	requestedVersion := item.requestedVersion
	if opts.RequestedVersion != nil {
		// Caller pins the content SHA but wants the manifest constraint preserved
		// (see PullOptions.RequestedVersion).
		requestedVersion = *opts.RequestedVersion
	}
	// hadExisting reports whether localName already had a lockfile entry
	// BEFORE this write — i.e. this pull replaced an existing pin rather than
	// creating a new one. It is the real signal for "updated" vs "installed"
	// (PullResult.Overwritten used to be hard-coded false, making
	// operations/sync.go's "updated" status unreachable).
	hadExisting, err := p.updateLockfile(item.localName, opts, item.rem, item.sha, requestedVersion, item.resolvedVersion, item.kind, item.retracted, item.retractedReason, item.retractionCheckedAt, item.tree != nil)
	if err != nil {
		return nil, fmt.Errorf("pulled %s but failed to record its lockfile pin (the only on-disk record of this pull): %w", item.localName, err)
	}

	return &PullResult{
		LocalPath:       localPath,
		SHA:             item.sha,
		Overwritten:     hadExisting,
		Content:         content,
		Retracted:       item.retracted,
		RetractedReason: item.retractedReason,
	}, nil
}

// installTree writes a fetched directory-form bundle into the cache and returns
// the directory it landed in.
//
// The destination is REPLACED, not merged. A merge would leave a file the
// publisher deleted upstream sitting in the consumer's tree forever, still
// enumerated by every directory walk that reads the bundle — and, for hooks and
// MCP servers, still applied. "What arrived is what is there" is the only
// property that makes a re-pull mean anything.
func (p *Puller) installTree(ref *Reference, opts PullOptions, item *fetchedItem) (string, error) {
	baseDir := opts.LocalDir
	if baseDir == "" {
		baseDir = p.lockfileManager.BaseDir()
	}
	fs := p.lockfileManager.FS()
	dir := ref.LocalTreePath(baseDir)

	if err := fs.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clear the previous %s tree at %s: %w", item.localName, dir, err)
	}
	// Sorted, so a failure part-way through names a deterministic file and two
	// runs over the same tree fail identically.
	rels := make([]string, 0, len(item.tree))
	for rel := range item.tree {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		file := item.tree[rel]
		warnUndeclaredExecutable(treeRepoPath(item.treeRoot, rel), file)
		if err := writeTreeFile(fs, dir, rel, file); err != nil {
			return "", fmt.Errorf("install %s into %s: %w", treeRepoPath(item.treeRoot, rel), dir, err)
		}
	}
	return dir, nil
}

// warnUndeclaredExecutable reports a file the publisher committed 100755 that
// the package does not DECLARE executable.
//
// It is the only place the divergence is visible. Downstream everything is
// consistent and quiet: the file lands 0644, the manifest the tree generates
// says 0644, verification passes, and the model is handed a script it cannot
// run — the silent no-op, arriving as delivered content rather than as an
// error. Saying it here names the repository path, the declaration that is
// missing, and the file that will not run.
func warnUndeclaredExecutable(repoPath string, file TreeFile) {
	if !file.CommittedExecutable || file.DeclaredExecutable {
		return
	}
	clidiag.Warn("ctxloom", "%s is committed executable upstream but the package does not declare it executable, "+
		"so it was installed DECLARED NON-EXECUTABLE (mode 0644) and will not run. "+
		"A mode bit is not portable and is not covered by the signature, so the declaration is what travels: "+
		"add it to the executable: list in the package's .meta.yaml sidecar and re-publish.", repoPath)
}

// writeTreeFile writes one file of a bundle tree at its DECLARED mode.
//
// Not at the mode git recorded. The two can disagree, and when they do the
// declaration is the one that has to reach disk: the manifest this same tree
// generates (bundles.ReadTree, via the sidecar) is built from the declaration,
// and bundles.VerifyExtractedManifest compares that manifest against the files
// on disk. Installing at git's mode is what made a published-0755-but-
// undeclared script arrive as a whole package the consumer refused.
//
// iox.WriteFileAtomicFs applies perm EXACTLY via its own explicit Chmod on
// the temp file (see its doc) — the same fix this function used to hand-roll
// with the trailing fs.Chmod call afero.WriteFile's umask-masked create left
// necessary. Migrating to it drops that now-redundant second Chmod for free.
//
// AllowEmpty: an intentionally empty file is a legitimate member of a bundle
// tree (a placeholder, a deliberately-emptied config), so the default
// empty-over-existing refusal would wrongly block a legitimate re-pull —
// unlike this package's OTHER writers (git_publisher, the lockfile/registry
// stores), nothing here can distinguish "the tree really has a 0-byte file"
// from "something upstream went wrong", so the guard is opted out rather than
// guessed at.
func writeTreeFile(fs afero.Fs, dir, rel string, file TreeFile) error {
	mode := os.FileMode(0o644)
	if file.DeclaredExecutable {
		mode = 0o755
	}
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := fs.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return iox.WriteFileAtomicFs(fs, full, file.Data, mode, iox.AllowEmpty())
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
func (p *Puller) updateLockfile(localName string, opts PullOptions, remote *Remote, sha string, requestedVersion, resolvedVersion string, kind SelectorKind, retracted bool, retractedReason string, retractionCheckedAt time.Time, tree bool) (hadExisting bool, err error) {
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
		// Which SHAPE was installed, so the reader does not have to guess (see
		// LockEntry.Tree).
		Tree: tree,
	}

	// A hold ("do not upgrade this") is a deliberate decision; a content re-pull
	// must never silently clear it. Always carry the flag forward, and on a
	// blanket pull (no explicit version requested, e.g. `deps pull --force`)
	// keep the entry's frozen SHA/Version too — force repairs a clone, it does
	// not advance past a hold (see LockEntry.Pinned).
	if hadExisting && existing.Held {
		entry.Held = true
		if requestedVersion == "" {
			entry.SHA = existing.SHA
			entry.Version = existing.Version
			entry.RequestedVersion = existing.RequestedVersion
			entry.Kind = existing.Kind
		}
	}

	lockfile.AddEntry(itemType, localName, entry)

	if err := target.Save(lockfile); err != nil {
		return hadExisting, fmt.Errorf("failed to save lockfile: %w", err)
	}

	return hadExisting, nil
}
