package remote

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// PullOptions configures pull behavior.
type PullOptions struct {
	// Force skips the confirmation prompt but still displays content.
	Force bool

	// Blind skips both confirmation prompt AND content display.
	// Use this for automated/batch operations. Implies Force.
	Blind bool

	// StageUntrustedNew redirects the lockfile write of a FIRST INSTALL (an
	// item with no existing active lockfile entry) from a remote without
	// TrustBundles into the pending lockfile (lock.pending.yaml) instead of
	// the active one. SECURITY: Blind pulls skip the interactive security
	// review, so non-interactive callers (startup/auto sync) set this so
	// never-reviewed content cannot reach the active lockfile — and thus its
	// hooks/MCP/context cannot activate — until the user approves it via
	// `ctxloom bundle review` / `ctxloom bundle approve`. Items already in the
	// active lockfile (previously installed or reviewed) are unaffected.
	StageUntrustedNew bool

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

	// Staged reports that the item's lockfile entry was written to the
	// PENDING lockfile for review instead of the active one (see
	// PullOptions.StageUntrustedNew). A staged item is not installed: its
	// content cannot resolve, and its hooks/MCP cannot activate, until the
	// entry is approved into the active lockfile.
	Staged bool
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

// TerminalChecker checks if readers/writers are terminals.
type TerminalChecker interface {
	IsTerminalReader(r io.Reader) bool
	IsTerminalWriter(w io.Writer) bool
}

// defaultTerminalChecker uses os/term for real terminal detection.
type defaultTerminalChecker struct{}

func (d *defaultTerminalChecker) IsTerminalReader(r io.Reader) bool {
	if f, ok := r.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

func (d *defaultTerminalChecker) IsTerminalWriter(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

// Puller handles pulling items from remotes.
type Puller struct {
	registry              *Registry
	auth                  AuthConfig
	lockfileManager       *LockfileManager
	bundleLockfileManager *LockfileManager // optional: when set, bundle pulls write here instead of lockfileManager
	fetcherFactory        FetcherFactory
	terminalChecker       TerminalChecker
	fs                    afero.Fs
}

// PullerOption is a functional option for configuring a Puller.
type PullerOption func(*Puller)

// WithPullerFS sets a custom filesystem implementation (for testing).
func WithPullerFS(fs afero.Fs) PullerOption {
	return func(p *Puller) {
		p.fs = fs
	}
}

// WithLockfileManager sets a custom lockfile manager (for testing).
func WithLockfileManager(lm *LockfileManager) PullerOption {
	return func(p *Puller) {
		p.lockfileManager = lm
	}
}

// WithBundleLockfileTarget redirects bundle-pull lockfile writes to lm
// while leaving profile-pull writes pointed at the main lockfile manager.
// SyncOnStartup uses this so bundle changes land in lock.pending.yaml
// instead of the active lock.yaml (docs/bundle-review-plan.md Phase 2.3).
func WithBundleLockfileTarget(lm *LockfileManager) PullerOption {
	return func(p *Puller) {
		p.bundleLockfileManager = lm
	}
}

// WithFetcherFactory sets a custom fetcher factory (for testing).
func WithFetcherFactory(ff FetcherFactory) PullerOption {
	return func(p *Puller) {
		p.fetcherFactory = ff
	}
}

// WithTerminalChecker sets a custom terminal checker (for testing).
func WithTerminalChecker(tc TerminalChecker) PullerOption {
	return func(p *Puller) {
		p.terminalChecker = tc
	}
}

// NewPuller creates a new puller.
func NewPuller(registry *Registry, auth AuthConfig, opts ...PullerOption) *Puller {
	p := &Puller{
		registry:        registry,
		auth:            auth,
		fetcherFactory:  DefaultFetcherFactory,
		terminalChecker: &defaultTerminalChecker{},
		fs:              afero.NewOsFs(),
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
	localName        string // lockfile key, "remote/path"
	sha              string
	requestedVersion string // user-specified version, "" if they took the default
	resolvedVersion  string // concrete tag a semver constraint resolved to, "" otherwise
	content          []byte
}

// Pull downloads an item from a remote with security review. It is the
// orchestrator: fetch (resolve → retraction → SHA → download → security gate),
// then install (transform → write → lock → cascade). Each phase is a helper
// below so this stays readable and each piece is independently testable.
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

	item, err := p.fetchForPull(ctx, ref, refStr, opts)
	if err != nil {
		return nil, err
	}

	return p.installPulledItem(ctx, ref, opts, item)
}

// fetchForPull resolves the remote, checks retraction, resolves the SHA, fetches
// the content, and runs the security gate — everything needed before writing.
func (p *Puller) fetchForPull(ctx context.Context, ref *Reference, refStr string, opts PullOptions) (*fetchedItem, error) {
	repoURL, rem, localName, err := p.resolveRemoteTarget(ref)
	if err != nil {
		return nil, err
	}

	fetcher, err := p.fetcherFactory(repoURL, p.auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create fetcher: %w", err)
	}

	owner, repo, err := ParseRepoURL(repoURL)
	if err != nil {
		return nil, fmt.Errorf("invalid remote URL: %w", err)
	}

	if err := p.confirmRetraction(ctx, fetcher, owner, repo, ref, opts); err != nil {
		return nil, err
	}

	sha, requestedVersion, resolvedVersion, err := resolveContentSHA(ctx, fetcher, owner, repo, ref)
	if err != nil {
		return nil, err
	}

	filePath := ref.BuildFilePath(opts.ItemType)
	content, err := fetcher.FetchFile(ctx, owner, repo, filePath, sha)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch: %w", err)
	}

	if err := p.securityReview(ref, rem, refStr, opts, sha, filePath, content); err != nil {
		return nil, err
	}

	return &fetchedItem{rem: rem, localName: localName, sha: sha, requestedVersion: requestedVersion, resolvedVersion: resolvedVersion, content: content}, nil
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
	// Lockfile key is the canonical ref — the sole content identity.
	return repoURL, rem, ref.CanonicalString(), nil
}

// confirmRetraction warns and (unless forced) prompts when a version has been
// retracted. A declined prompt cancels the pull. Blind implies force here, as
// in securityReview: blind pulls run non-interactively (MCP startup sync), so
// a prompt would block on a stdin nobody answers.
func (p *Puller) confirmRetraction(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference, opts PullOptions) error {
	retracted, reason, _ := CheckRetracted(ctx, fetcher, owner, repo, ref, opts.ItemType)
	if !retracted {
		return nil
	}
	_, _ = fmt.Fprintf(opts.Stdout, "\n⚠️  WARNING: This version has been retracted!\n")
	_, _ = fmt.Fprintf(opts.Stdout, "Reason: %s\n\n", reason)
	if opts.Force || opts.Blind {
		return nil
	}
	confirmed, err := promptConfirmation(opts.Stdout, opts.Stdin, "Continue anyway?")
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("installation cancelled: version retracted: %w", errs.ErrCancelled)
	}
	return nil
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
func resolveContentSHA(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference) (sha, requestedVersion, resolvedVersion string, err error) {
	requestedVersion = ref.EffectiveContentVersion()
	res, err := ResolveConstraint(ctx, requestedVersion, NewFetcherRepoVersions(fetcher, owner, repo))
	if err != nil {
		return "", "", "", fmt.Errorf("failed to resolve version %q: %w", requestedVersion, err)
	}
	return res.SHA, requestedVersion, res.Version, nil
}

// securityReview enforces the pull's safety gate: blind mode implies force,
// non-forced pulls require an interactive terminal and show the security
// warning plus a confirmation prompt. Returns an error when the gate is not
// satisfied (no terminal, parse failure, or declined prompt).
func (p *Puller) securityReview(ref *Reference, rem *Remote, refStr string, opts PullOptions, sha, filePath string, content []byte) error {
	effectiveForce := opts.Force || opts.Blind

	if opts.Blind {
		_, _ = fmt.Fprintf(opts.Stdout, "⚠️  Blind mode: skipping security review for %s\n", refStr)
	}

	if !effectiveForce && !p.terminalChecker.IsTerminalReader(opts.Stdin) {
		return fmt.Errorf("interactive terminal required for pull; use --force to skip confirmation")
	}

	if !opts.Blind {
		if err := p.displaySecurityReview(ref, rem, opts, sha, filePath, content); err != nil {
			return err
		}
	}

	if effectiveForce {
		return nil
	}
	return confirmInstall(opts)
}

// displaySecurityReview parses the content for its type-specific security
// warning and prints it. Returns an error only when the content cannot be
// parsed.
func (p *Puller) displaySecurityReview(ref *Reference, rem *Remote, opts PullOptions, sha, filePath string, content []byte) error {
	shortSHA := sha
	if len(sha) > 7 {
		shortSHA = sha[:7]
	}
	secure, err := ParseSecureContent(opts.ItemType, content)
	if err != nil {
		return fmt.Errorf("failed to parse content: %w", err)
	}
	displaySecurityWarning(opts.Stdout, ref, rem, shortSHA, filePath, content, secure, p.terminalChecker)
	return nil
}

// confirmInstall prompts for install confirmation, returning ErrCancelled when
// the user declines.
func confirmInstall(opts PullOptions) error {
	confirmed, err := promptConfirmation(opts.Stdout, opts.Stdin, "Install this item?")
	if err != nil {
		return fmt.Errorf("failed to read confirmation: %w", err)
	}
	if !confirmed {
		return fmt.Errorf("installation cancelled: %w", errs.ErrCancelled)
	}
	return nil
}

// installPulledItem records a pulled remote item (synthetic path — nothing is
// materialized) and writes its lockfile entry. Dependencies are NOT cascaded:
// every reference is hash-pinned, so the dependency closure is determined by
// lock walking the refs (operations.FlattenDependencies), not by pull.
func (p *Puller) installPulledItem(ctx context.Context, ref *Reference, opts PullOptions, item *fetchedItem) (*PullResult, error) {
	content := item.content

	localPath, overwritten, err := p.writePulledContent(ref, opts, item.localName, item.sha, content)
	if err != nil {
		return nil, err
	}

	// Update lockfile with provenance (local name as key). For bundles, the
	// lockfile is the *only* on-disk record — read sites resolve content via
	// the SHA recorded here. A lockfile failure warns but does not fail the pull.
	requestedVersion := item.requestedVersion
	if opts.RequestedVersion != nil {
		// Caller pins the content SHA but wants the manifest constraint preserved
		// (see PullOptions.RequestedVersion).
		requestedVersion = *opts.RequestedVersion
	}
	staged, err := p.updateLockfile(item.localName, opts, item.rem, item.sha, requestedVersion, item.resolvedVersion)
	if err != nil {
		_, _ = fmt.Fprintf(opts.Stdout, "Warning: failed to update lockfile: %v\n", err)
	}

	return &PullResult{LocalPath: localPath, SHA: item.sha, Overwritten: overwritten, Content: content, Staged: staged}, nil
}

// writePulledContent records a pulled remote item. Remote bundles AND profiles
// are pure references: the git clone cache + lockfile pair is the storage, and
// reads at the locked SHA go through remote.BundleReader / remote.ProfileReader.
// Nothing is materialized to disk, so this returns a synthetic informational
// LocalPath and never overwrites a local file.
func (p *Puller) writePulledContent(_ *Reference, _ PullOptions, localName, sha string, _ []byte) (localPath string, overwritten bool, err error) {
	return fmt.Sprintf("<remote>:%s@%s", localName, sha), false, nil
}

// displaySecurityWarning shows the security warning and full content.
func displaySecurityWarning(w io.Writer, ref *Reference, rem *Remote, sha, filePath string, content []byte, secure SecureContent, tc TerminalChecker) {
	warning := secure.SecurityWarning()

	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "┌─────────────────────────────────────────────────────────────────┐")
	_, _ = fmt.Fprintf(w, "│  ⚠️  WARNING: %-50s│\n", warning.Title)
	_, _ = fmt.Fprintln(w, "│                                                                 │")
	_, _ = fmt.Fprintf(w, "│  %-62s│\n", warning.Context)
	_, _ = fmt.Fprintln(w, "│  Malicious content can:                                         │")
	for _, risk := range warning.Risks {
		_, _ = fmt.Fprintf(w, "│    • %-58s│\n", risk)
	}
	_, _ = fmt.Fprintln(w, "│                                                                 │")
	_, _ = fmt.Fprintln(w, "│  REVIEW THE FULL CONTENT BELOW BEFORE ACCEPTING                │")
	_, _ = fmt.Fprintln(w, "└─────────────────────────────────────────────────────────────────┘")
	_, _ = fmt.Fprintln(w, "")

	// Source info
	_, _ = fmt.Fprintf(w, "Source: %s @ %s\n", rem.URL, sha)
	_, _ = fmt.Fprintf(w, "Org:    %s\n", ref.LocalRemoteName())
	_, _ = fmt.Fprintf(w, "Name:   %s\n", ref.Path)
	_, _ = fmt.Fprintf(w, "Path:   %s\n", filePath)

	// Display note if present
	if note := secure.Note(); note != "" {
		// Truncate very long notes (max 4K chars)
		const maxNoteLen = 4096
		if len(note) > maxNoteLen {
			note = note[:maxNoteLen-3] + "..."
		}
		_, _ = fmt.Fprintln(w, "")
		_, _ = fmt.Fprintf(w, "Note: %s\n", note)
	}

	_, _ = fmt.Fprintln(w, "")

	// Content with pager for long content
	contentStr := string(content)
	lineCount := strings.Count(contentStr, "\n") + 1

	_, _ = fmt.Fprintln(w, "─────────────────── CONTENT START ───────────────────")

	// Use pager for long content if terminal.
	// Security note: PAGER is user-controlled. This is standard Unix behavior
	// but users should be aware that PAGER could execute arbitrary commands.
	if lineCount > 50 && tc.IsTerminalWriter(w) {
		pager := os.Getenv("PAGER")
		if pager == "" {
			pager = "less"
		}

		cmd := exec.Command(pager)
		cmd.Stdin = strings.NewReader(contentStr)
		cmd.Stdout = w
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			// Fallback to direct output
			_, _ = fmt.Fprint(w, contentStr)
		}
	} else {
		_, _ = fmt.Fprint(w, contentStr)
	}

	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "─────────────────── CONTENT END ─────────────────────")
	_, _ = fmt.Fprintln(w, "")
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

// updateLockfile records provenance in the lockfile. Bundles route to
// bundleLockfileManager when one was configured (see WithBundleLockfileTarget),
// so SyncOnStartup can land changes in lock.pending.yaml without touching
// the active lock.yaml. Profiles always go to the main lockfile manager.
//
// With opts.StageUntrustedNew, a first install (no existing entry in the
// target lockfile) from a remote without TrustBundles is staged into the
// pending lockfile instead — the security gate for non-interactive pulls.
// Returns staged=true when that redirect happened.
func (p *Puller) updateLockfile(localName string, opts PullOptions, remote *Remote, sha string, requestedVersion, resolvedVersion string) (staged bool, err error) {
	itemType := opts.ItemType
	target := p.lockfileTargetFor(itemType)
	writingToPending := p.bundleLockfileManager != nil && target == p.bundleLockfileManager
	lockfile, err := target.Load()
	if err != nil {
		return false, fmt.Errorf("failed to load lockfile: %w", err)
	}

	existing, hadExisting := lockfile.GetEntry(itemType, localName)

	entry := LockEntry{
		SHA:              sha,
		URL:              remote.URL,
		RequestedVersion: requestedVersion,
		Version:          resolvedVersion,
		FetchedAt:        time.Now().UTC(),
	}

	// SECURITY: an untrusted first install never reaches the active lockfile
	// from a non-interactive pull — it is staged for `bundle review` instead.
	// Trust mirrors StageUpgrade's routing: TrustBundles means "apply without
	// review". Only applies when writing to the main (active) manager.
	if opts.StageUntrustedNew && !hadExisting && !remote.TrustBundles &&
		target == p.lockfileManager && !target.IsPending() {
		pendingMgr := target.PendingCounterpart()
		pending, perr := pendingMgr.Load()
		if perr != nil {
			return false, fmt.Errorf("failed to load pending lockfile: %w", perr)
		}
		pending.AddEntry(itemType, localName, entry)
		if serr := pendingMgr.Save(pending); serr != nil {
			return false, fmt.Errorf("failed to save pending lockfile: %w", serr)
		}
		return true, nil
	}

	// A pin is a deliberate "do not upgrade" decision; a content re-pull must
	// never silently clear it. Always carry the flag forward. On the ACTIVE
	// lockfile a blanket pull (no explicit version requested, e.g.
	// `remote pull --force`) also keeps the entry's frozen SHA/Version — force
	// repairs a clone, it does not advance past a pin. The pending lockfile
	// still receives the new SHA so the user can unpin and review it later
	// (see LockEntry.Pinned).
	if hadExisting && existing.Pinned {
		entry.Pinned = true
		if !writingToPending && requestedVersion == "" {
			entry.SHA = existing.SHA
			entry.Version = existing.Version
			entry.RequestedVersion = existing.RequestedVersion
		}
	}

	lockfile.AddEntry(itemType, localName, entry)

	if err := target.Save(lockfile); err != nil {
		return false, fmt.Errorf("failed to save lockfile: %w", err)
	}

	return false, nil
}

// lockfileTargetFor picks the manager that should own a write for itemType.
// Bundles use bundleLockfileManager when set; everything else uses the main
// manager.
func (p *Puller) lockfileTargetFor(itemType ItemType) *LockfileManager {
	if itemType == ItemTypeBundle && p.bundleLockfileManager != nil {
		return p.bundleLockfileManager
	}
	return p.lockfileManager
}
