package remote

import (
	"context"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// PullOptions configures pull behavior.
//
// A pull only records a dependency pin; it carries no review ceremony of its
// own.
type PullOptions struct {
	// LocalDir overrides the default .ctxloom directory path.
	LocalDir string

	// ItemType specifies what type of item to pull.
	ItemType ItemType
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

	// Reinstalled is true when the item already had a lockfile pin, which this
	// pull re-recorded at that same pin (pinFor never moves one); false when it
	// recorded the item's first pin — sync's "reinstalled" vs "installed".
	Reinstalled bool

	// Content holds the fetched bytes for callers that would otherwise
	// re-read from LocalPath. Populated for bundles (whose LocalPath is
	// synthetic) and also for profiles (where it equals what was written).
	Content []byte
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

	return p
}

// fetchedItem carries everything resolved during the fetch phase of a Pull
// (remote, SHA, on-the-wire content) into the install phase.
type fetchedItem struct {
	rem              *Remote
	localName        trust.BundleKey // lockfile key: the bundle identity
	sha              string
	requestedVersion string       // user-specified version, "" if they took the default
	resolvedVersion  string       // concrete tag a semver constraint resolved to, "" otherwise
	kind             SelectorKind // classified selector kind (sha/tag/version/branch)
	content          []byte
	tree             map[string]TreeFile // non-nil when the bundle was published in DIRECTORY form
	treeRoot         string              // the repo path that tree was fetched from, for diagnostics
}

// Pull downloads an item from a remote and records its pin. It is the
// orchestrator: fetch (resolve → SHA → download), then install
// (verify → write → lock). Each phase is a helper below so this stays readable and each
// piece is independently testable.
func (p *Puller) Pull(ctx context.Context, refStr string, opts PullOptions) (*PullResult, error) {
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

// fetchForPull resolves the remote and the SHA and fetches the content —
// everything needed before verifying and writing the pin.
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

	sha, requestedVersion, resolvedVersion, kind, err := p.pinFor(ctx, fetcher, owner, repo, ref, localName, opts)
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
// local-name. It is the fetch chokepoint's registration rule: a repository no
// remote is registered for is refused (NotRegisteredError), never registered.
func (p *Puller) resolveRemoteTarget(ref *Reference) (repoURL string, rem *Remote, localName trust.BundleKey, err error) {
	if !ref.IsCanonical() {
		return "", nil, "", fmt.Errorf("not a canonical reference: %s", ref.String())
	}
	repoURL = ref.URL
	rem, ok := p.registry.LookupURL(repoURL)
	if !ok {
		return "", nil, "", NotRegisteredError(repoURL)
	}
	// The lockfile key is the bundle's identity, while repoURL stays the
	// address as typed, which is the transport the user chose.
	localName, err = ref.LockKey()
	if err != nil {
		return "", nil, "", fmt.Errorf("invalid reference %s: %w", ref.String(), err)
	}
	return repoURL, rem, localName, nil
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

// pinFor is the commit a pull installs: the existing pin when there is one,
// whatever the ref's constraint now says, else the constraint resolved now. A
// pull creates first pins and never moves one — that is `deps upgrade`'s alone.
// It is the carry-forward rule the lock rebuild applies
// (operations.newConstraintResolver), so pull and lock agree on which commit a
// pinned ref names.
func (p *Puller) pinFor(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference, localName trust.BundleKey, opts PullOptions) (sha, requestedVersion, resolvedVersion string, kind SelectorKind, err error) {
	if entry, ok := p.recordedEntry(opts.ItemType, localName); ok && entry.SHA != "" {
		return entry.SHA, entry.RequestedVersion, entry.Version, entry.Kind, nil
	}
	return resolveContentSHA(ctx, fetcher, owner, repo, ref)
}

// installPulledItem records a pulled remote item (synthetic path — nothing is
// materialized) and writes its lockfile entry. Dependencies are NOT cascaded:
// every reference is hash-pinned, so the dependency closure is determined by
// lock walking the refs (operations.FlattenDependencies), not by pull.
//
// A bundle is a tree — a package: multi-file, mode-bearing, and read by
// machinery (skill materialization, hook enumeration) that takes a real
// directory, not bytes. So the checked-out worktree is the only LocalPath a
// bundle ever gets. The checkout lands in the CACHE (gitignored, regenerable):
// the pin in the lockfile stays the authority, and the worktree is checked out
// from it. The commit checked out and the commit recorded are both item.sha,
// which pinFor took from the existing pin when there is one.
func (p *Puller) installPulledItem(ctx context.Context, ref *Reference, opts PullOptions, item *fetchedItem) (*PullResult, error) {
	if err := p.verifyTree(ctx, item); err != nil {
		return nil, err
	}
	localPath, err := p.installTree(ctx, ref, opts, item, item.sha)
	if err != nil {
		return nil, err
	}

	// For bundles, the lockfile is the *only* on-disk record — read sites
	// resolve content via the SHA recorded here — so its write failing means
	// the pull failed, never a printed warning over a reported success.
	// hadExisting reports whether localName already had a lockfile entry
	// BEFORE this write: the signal for "reinstalled" vs "installed".
	hadExisting, err := p.updateLockfile(item, opts.ItemType)
	if err != nil {
		return nil, fmt.Errorf("pulled %s but failed to record its lockfile pin (the only on-disk record of this pull): %w", item.localName, err)
	}

	return &PullResult{
		LocalPath:   localPath,
		SHA:         item.sha,
		Reinstalled: hadExisting,
		Content:     item.content,
	}, nil
}

// verifyTree runs the wired tree verifier over the fetched tree BEFORE
// anything is checked out or written: a tree it refuses is never pinned.
func (p *Puller) verifyTree(ctx context.Context, item *fetchedItem) error {
	if p.treeVerify == nil {
		return fmt.Errorf("refusing to install %q: this puller has no tree verifier wired in, so it cannot establish what it would be pinning", item.localName)
	}
	if _, err := p.treeVerify(ctx, item.tree, item.treeRoot, item.sha, item.rem.URL); err != nil {
		return fmt.Errorf("refusing to install %s at %s: %w", item.localName, item.sha, err)
	}
	return nil
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

// updateLockfile records item's pin in the (active) lockfile. A hold is a
// deliberate decision a re-pull must never clear, so Held carries forward
// from the entry being replaced.
func (p *Puller) updateLockfile(item *fetchedItem, itemType ItemType) (hadExisting bool, err error) {
	lockfile, err := p.lockfileManager.Load()
	if err != nil {
		return false, fmt.Errorf("failed to load lockfile: %w", err)
	}
	existing, hadExisting := lockfile.GetEntry(itemType, item.localName)
	lockfile.AddEntry(itemType, item.localName, LockEntry{
		SHA:              item.sha,
		URL:              item.rem.URL,
		RequestedVersion: item.requestedVersion,
		Version:          item.resolvedVersion,
		Kind:             item.kind,
		FetchedAt:        time.Now().UTC(),
		Held:             hadExisting && existing.Held,
	})
	if err := p.lockfileManager.Save(lockfile); err != nil {
		return hadExisting, fmt.Errorf("failed to save lockfile: %w", err)
	}
	return hadExisting, nil
}
