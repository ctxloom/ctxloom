package remote

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// Publisher handles publishing items to remote repositories.
type Publisher interface {
	// CreateOrUpdateFile creates or updates a file in a repository.
	// Returns the commit SHA of the change.
	CreateOrUpdateFile(ctx context.Context, owner, repo, path, branch, message string, content []byte) (string, error)

	// CreatePullRequest creates a pull request from a branch.
	// Returns the PR URL.
	CreatePullRequest(ctx context.Context, owner, repo, title, body, head, base string) (string, error)

	// CreateBranch creates a new branch from a base ref.
	CreateBranch(ctx context.Context, owner, repo, branchName, baseSHA string) error

	// GetFileSHA gets the blob SHA of an existing file (needed for updates).
	// Returns empty string if file doesn't exist.
	GetFileSHA(ctx context.Context, owner, repo, path, ref string) (string, error)
}

// PublisherFactory creates Publisher instances. Allows mocking for tests.
type PublisherFactory func(repoURL string, auth AuthConfig) (Publisher, error)

// defaultPublisherFactory is the production implementation.
func defaultPublisherFactory(repoURL string, auth AuthConfig) (Publisher, error) {
	return NewPublisher(repoURL, auth)
}

// PublishManager handles publish operations with injectable dependencies.
type PublishManager struct {
	registry         *Registry
	auth             AuthConfig
	publisherFactory PublisherFactory
	fetcherFactory   FetcherFactory
	fs               afero.Fs

	// confirmed is the record of remotes a human already approved as publish
	// destinations; nil means the production store
	// (~/.ctxloom/publish-remotes). ask puts the question to a human, and nil
	// — the DEFAULT — means there is nobody to ask, so an unconfirmed remote
	// is refused rather than assumed. See publish_confirm.go.
	confirmed *ConfirmedRemotes
	ask       PublishRemoteAsk
}

// PublishManagerOption configures a PublishManager.
type PublishManagerOption func(*PublishManager)

// WithPublishFS sets a custom filesystem for the publish manager.
func WithPublishFS(fs afero.Fs) PublishManagerOption {
	return func(pm *PublishManager) {
		pm.fs = fs
	}
}

// WithPublisherFactory sets a custom publisher factory (for testing).
func WithPublisherFactory(pf PublisherFactory) PublishManagerOption {
	return func(pm *PublishManager) {
		pm.publisherFactory = pf
	}
}

// WithPublishFetcherFactory sets a custom fetcher factory (for testing).
func WithPublishFetcherFactory(ff FetcherFactory) PublishManagerOption {
	return func(pm *PublishManager) {
		pm.fetcherFactory = ff
	}
}

// WithRemoteAsk attaches the human who answers "is this the remote you meant?"
// the first time content is published to a given non-GitHub remote.
//
// A frontend supplies this ONLY when it actually has an interactive terminal.
// Leaving it unset is the fail-closed default and the correct state for an
// agent, an MCP tool call, an editor session, a CI job or a piped command:
// those get a refusal that names the remote and says how to confirm it, never
// a prompt written into a pipe and never an assumed yes.
func WithRemoteAsk(ask PublishRemoteAsk) PublishManagerOption {
	return func(pm *PublishManager) {
		pm.ask = ask
	}
}

// WithConfirmedRemotes overrides the store of already-confirmed publish
// remotes (tests point it at a temp directory instead of the user's home).
func WithConfirmedRemotes(store *ConfirmedRemotes) PublishManagerOption {
	return func(pm *PublishManager) {
		pm.confirmed = store
	}
}

// NewPublishManager creates a new publish manager.
func NewPublishManager(registry *Registry, auth AuthConfig, opts ...PublishManagerOption) *PublishManager {
	pm := &PublishManager{
		registry:         registry,
		auth:             auth,
		publisherFactory: defaultPublisherFactory,
		fetcherFactory:   DefaultFetcherFactory,
		fs:               afero.NewOsFs(),
	}

	// Apply options
	for _, opt := range opts {
		opt(pm)
	}

	return pm
}

// PublishOptions configures publish behavior.
type PublishOptions struct {
	// CreatePR creates a pull request instead of pushing directly.
	CreatePR bool

	// Branch is the target branch (or base branch for PR).
	Branch string

	// Title is the PR title (used as commit subject too). If empty, the first
	// line of Message is used; if Message has no first line either, a default
	// is generated.
	Title string

	// Message is the commit body / PR description body.
	Message string

	// ItemType specifies what type of item to publish.
	ItemType ItemType

	// RemotePath is the repo-relative path to write, e.g.
	// ".ctxloom/content/bundles/security.yaml". REQUIRED — an empty value is a
	// hard error, never a guess.
	//
	// It is supplied by the caller rather than derived here because the caller
	// also REPORTS it (operations.PushBundleResult.TargetPath, which the CLI
	// prints and which operations.moveToRemote turns into SigDest). This used to
	// be computed twice — once there, once here — from two copies of the same
	// expression over the local file's basename, with nothing binding them. They
	// agreed only by coincidence of spelling, so a change to either alone would
	// have made `bundle push` report one path and publish to another, and
	// `bundle move` would have deleted the local source after naming a SigDest
	// nobody wrote. Now there is one value: the caller computes it with
	// PublishPath and hands the same string to both.
	//
	// It is also why a directory-form bundle publishes correctly. Deriving the
	// name from filepath.Base(localPath) made every `<name>/bundle.yaml` publish
	// as "bundle"; the caller uses bundles.ExtractBundleName, which package
	// remote cannot call itself (bundles imports remote).
	RemotePath string

	// SignPayload, when non-nil, is called with the EXACT bytes about to be
	// written to remotePath — the local file's bytes, verbatim, which are also
	// the bytes that will sit in the remote tree (spec §3.1: a publisher
	// signature covers the raw bundle FILE bytes, unframed and unmodified) —
	// and must return an armored PROTOCOL.sshsig blob over them. The result is
	// written as a detached sibling "<remotePath>.sig" in the SAME
	// commit/branch (spec §4.1).
	//
	// This is a callback rather than a concrete signer type so package
	// remote stays decoupled from package signing/agentkey — key discovery
	// and signing are the caller's (operations/CLI) responsibility; this
	// package only knows "given bytes, produce a signature or fail". A
	// non-nil SignPayload that returns an error aborts the ENTIRE publish
	// before any network write — signing failure must never degrade to an
	// unsigned publish (spec §7A.4, normative).
	SignPayload func(payload []byte) ([]byte, error)
}

// PublishResult contains the result of a publish operation.
type PublishResult struct {
	// Path is the remote path where the item was published.
	Path string

	// SHA is the commit SHA of the change.
	SHA string

	// PRURL is the pull request URL (if CreatePR was true).
	PRURL string

	// Created indicates if a new file was created (vs updated).
	Created bool

	// Signed reports whether a detached "<Path>.sig" sibling was written
	// alongside Path (PublishOptions.SignPayload was set and succeeded).
	Signed bool
}

// publishPrep holds everything resolved before the push/PR strategy runs.
type publishPrep struct {
	publisher   Publisher
	repoURL     string
	owner, repo string
	itemName    string
	remotePath  string
	branch      string
	content     []byte // the local file's bytes, verbatim (spec §3.0, §3.1)
	// signature is the armored sshsig blob over content, computed by
	// PublishOptions.SignPayload when set; nil means "not signing".
	signature     []byte
	title, body   string
	commitMessage string
	created       bool
}

// Publish publishes a local item to a remote repository.
func (pm *PublishManager) Publish(ctx context.Context, localPath string, remoteName string, opts PublishOptions) (*PublishResult, error) {
	prep, err := pm.preparePublish(ctx, localPath, remoteName, opts)
	if err != nil {
		return nil, err
	}
	defer closePublisher(prep.publisher)
	if opts.CreatePR {
		return pm.publishViaPR(ctx, prep, opts)
	}
	return pm.publishDirect(ctx, prep)
}

// closePublisher releases a publisher that holds resources — GitPublisher's
// working clone is a temp directory that must not outlive the publish. It is
// an OPTIONAL capability: the GitHub publisher holds nothing and does not
// implement it, so nothing about that path changes.
//
// A close failure is deliberately swallowed: it means a temp directory
// survived, which is untidy, and turning it into a publish error would report
// a successful push as a failure.
func closePublisher(p Publisher) {
	if c, ok := p.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

// loadPublishContent reads the local file's bytes. It does not transform them
// in any way, and must not: the bytes it returns are the bytes that get signed
// and the bytes that land in the remote (spec §3.0, §3.1). The filesystem is
// the manager-level seam (WithPublishFS) — PublishOptions used to carry a
// SECOND, always-empty FS field with a silent precedence rule over this one;
// it was deleted since nothing, in production or in tests, ever set it.
func (pm *PublishManager) loadPublishContent(localPath string) ([]byte, error) {
	content, err := afero.ReadFile(pm.fs, localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read local file: %w", err)
	}
	// An empty local file must never publish. Without this floor a
	// 0-byte file overwrote whatever real content already existed at the
	// remote path with nothing, reported success, and — before signing.Sign
	// gained its own floor — could even produce a "valid" publisher
	// signature over zero bytes. Reject here, before any network write and
	// before SignPayload is ever called, so both the signed and unsigned
	// publish paths are covered by one guard.
	if len(content) == 0 {
		return nil, fmt.Errorf("refusing to publish empty file %s: a 0-byte file would overwrite the remote with nothing", localPath)
	}
	return content, nil
}

// defaultBrancher is the optional Publisher capability of answering its own
// repository's default branch.
//
// It exists because the generic git adapter has NO API fetcher at all
// (NewFetcher says so outright), so the fetcher route below cannot answer for
// a non-GitHub remote — while GitPublisher, which is about to clone the
// repository anyway, can answer for free by reading what git checked out.
//
// Unexported on purpose: it adds no exported surface, and GitHubPublisher does
// not implement it, so the GitHub path keeps going through the fetcher exactly
// as before.
type defaultBrancher interface {
	defaultBranch(ctx context.Context) (string, error)
}

// pullRequestRefuser is the optional Publisher capability of declaring, up
// front, that it cannot open pull requests. Unexported for the same reason as
// defaultBrancher: it adds no exported surface, and GitHubPublisher does not
// implement it, so the PR strategy is unchanged there.
type pullRequestRefuser interface {
	pullRequestSupport() error
}

// resolvePublishBranch returns opts.Branch, or the repo's default branch when
// the caller didn't pin one — asking the publisher itself when it can answer,
// otherwise the forge fetcher.
func (pm *PublishManager) resolvePublishBranch(ctx context.Context, publisher Publisher, repoURL, owner, repo, optBranch string) (string, error) {
	if optBranch != "" {
		return optBranch, nil
	}
	if db, ok := publisher.(defaultBrancher); ok {
		branch, err := db.defaultBranch(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get default branch: %w", err)
		}
		return branch, nil
	}
	fetcher, err := pm.fetcherFactory(repoURL, pm.auth)
	if err != nil {
		return "", fmt.Errorf("failed to create fetcher: %w", err)
	}
	branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("failed to get default branch: %w", err)
	}
	return branch, nil
}

// preparePublish resolves the publisher, repo coordinates, target branch,
// content (the local file's bytes, verbatim), and commit subject/body shared by
// both push strategies.
func (pm *PublishManager) preparePublish(ctx context.Context, localPath, remoteName string, opts PublishOptions) (prep *publishPrep, err error) {
	content, err := pm.loadPublishContent(localPath)
	if err != nil {
		return nil, err
	}

	rem, err := pm.registry.Get(remoteName)
	if err != nil {
		return nil, fmt.Errorf("remote not found: %w", err)
	}

	// The forge decides two things below: whether this destination needs a
	// human's confirmation, and whether owner/repo have to parse at all.
	forge, _, ferr := DetectForge(rem.URL)
	if ferr != nil {
		return nil, fmt.Errorf("invalid remote URL: %w", ferr)
	}

	// Confirm the destination BEFORE anything is created or written. Signed
	// content is about to leave this machine, and "which remote did I just
	// push signed content to" must have been answered by a human at least
	// once per remote. See publish_confirm.go for the scope and the reasoning.
	if err := pm.authorizeRemote(ctx, rem.URL, forge); err != nil {
		return nil, err
	}

	publisher, err := pm.publisherFactory(rem.URL, pm.auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}
	// Everything below can still fail — branch resolution, signing, the
	// existing-file probe — and a publisher that has already made a working
	// clone would leak it. Publish closes the one that reaches it; this closes
	// the one that never does.
	defer func() {
		if err != nil {
			closePublisher(publisher)
		}
	}()

	// owner/repo are a forge API's PATH SEGMENTS ("/repos/<owner>/<repo>/…").
	// Plain git has no such pair — a git URL is one whole clone argument — and
	// a perfectly valid destination like file:///srv/bundles.git names only
	// one segment. So a parse failure is fatal exactly where the value is
	// spent: on the GitHub path, unchanged. Elsewhere the empty strings travel
	// to a publisher and a clone-backed fetcher that both ignore them.
	owner, repo, err := ParseOwnerRepo(rem.URL)
	if err != nil && forge == ForgeGitHub {
		return nil, fmt.Errorf("invalid remote URL: %w", err)
	}

	// The path to write is the caller's single computed value (see
	// PublishOptions.RemotePath), never re-derived from localPath. Empty is a
	// hard error: publishing to "" would write a repo-root file — or, with a
	// forge that tolerates it, nothing at all — and report success either way.
	remotePath := opts.RemotePath
	if remotePath == "" {
		return nil, fmt.Errorf("refusing to publish %s: PublishOptions.RemotePath is empty (compute it with remote.PublishPath so the path reported and the path written are the same value)", localPath)
	}
	// The display name is READ BACK from the path being written, so the commit
	// subject, PR branch and signature commit message can never name a different
	// item than the one being published.
	itemName := strings.TrimSuffix(path.Base(remotePath), ".yaml")

	branch, err := pm.resolvePublishBranch(ctx, publisher, rem.URL, owner, repo, opts.Branch)
	if err != nil {
		return nil, err
	}

	// Sign the EXACT bytes about to be written to remotePath — which are the
	// EXACT bytes read from the local file. Publish injects nothing and
	// re-serializes nothing (spec §3.0: "No re-serialization anywhere between
	// publisher and verifier"; §3.1: the publisher payload is the bundle file
	// bytes, verbatim). One canonical byte-set runs author → signature →
	// remote → consumer, so a `.sig` produced by `ctxloom sign` against the
	// local file verifies against the published bytes unchanged, and
	// republishing an unmodified bundle is reproducible.
	//
	// A signing failure aborts here, before any network write: no publish has
	// happened yet, so there is no partial state to unwind, and the caller
	// never sees the file land unsigned when signing was requested (spec
	// §7A.4, normative — failing to sign is a hard error).
	var signature []byte
	if opts.SignPayload != nil {
		signature, err = opts.SignPayload(content)
		if err != nil {
			return nil, fmt.Errorf("sign %s: %w", remotePath, err)
		}
	}

	// Existing file (if any) decides created vs updated and the default title.
	// GetFileSHA's contract (see the Publisher interface doc) is "empty string
	// means the file doesn't exist" — that is NOT the same as "the forge could
	// not be asked". A transient failure here must not be silently read as
	// "absent": that flips a genuine update into an "Add …" commit
	// subject / PR title, misrepresenting the change.
	existingSHA, err := publisher.GetFileSHA(ctx, owner, repo, remotePath, branch)
	if err != nil {
		return nil, fmt.Errorf("failed to check for an existing file at %s: %w", remotePath, err)
	}
	created := existingSHA == ""

	title, body := resolvePublishTitleAndBody(opts, itemName, created)
	return &publishPrep{
		publisher:     publisher,
		repoURL:       rem.URL,
		owner:         owner,
		repo:          repo,
		itemName:      itemName,
		remotePath:    remotePath,
		branch:        branch,
		content:       content,
		signature:     signature,
		title:         title,
		body:          body,
		commitMessage: buildCommitMessage(title, body),
		created:       created,
	}, nil
}

// publishSignatureSibling writes prep.signature (when non-nil) as
// "<remotePath>.sig" on branch, in its own commit right after the content
// commit — the detached sibling carrier (spec §4.1). Returns Signed=true
// only when a signature was actually written.
func publishSignatureSibling(ctx context.Context, publisher Publisher, prep *publishPrep, branch string) (bool, error) {
	if prep.signature == nil {
		return false, nil
	}
	sigPath := prep.remotePath + ".sig"
	msg := "sign " + prep.itemName
	if _, err := publisher.CreateOrUpdateFile(ctx, prep.owner, prep.repo, sigPath, branch, msg, prep.signature); err != nil {
		return false, fmt.Errorf("publish signature %s: %w", sigPath, err)
	}
	return true, nil
}

// buildCommitMessage assembles a git-convention commit message: subject, blank
// line, body.
func buildCommitMessage(title, body string) string {
	if body == "" {
		return title
	}
	return title + "\n\n" + body
}

// publishDirect pushes the content straight to the target branch, then the
// signature sibling (if any) — spec §4.1, same branch, same tree.
func (pm *PublishManager) publishDirect(ctx context.Context, prep *publishPrep) (*PublishResult, error) {
	sha, err := prep.publisher.CreateOrUpdateFile(ctx, prep.owner, prep.repo, prep.remotePath, prep.branch, prep.commitMessage, prep.content)
	if err != nil {
		return nil, fmt.Errorf("failed to publish: %w", err)
	}
	signed, err := publishSignatureSibling(ctx, prep.publisher, prep, prep.branch)
	if err != nil {
		// The content commit already landed UNSIGNED on the target branch;
		// this is surfaced as an error (never silently swallowed) so the
		// caller knows to retry `ctxloom sign` against the pushed bundle
		// rather than believing the signed publish it asked for succeeded.
		return nil, fmt.Errorf("bundle pushed as %s (commit %s) but its signature failed to publish: %w", prep.remotePath, sha, err)
	}
	return &PublishResult{Path: prep.remotePath, SHA: sha, Created: prep.created, Signed: signed}, nil
}

// publishViaPR creates a feature branch, commits the content there, and opens a
// pull request against the target branch.
func (pm *PublishManager) publishViaPR(ctx context.Context, prep *publishPrep, opts PublishOptions) (*PublishResult, error) {
	// A publisher that cannot open pull requests says so HERE, before the
	// branch, the commits or the base-ref lookup — otherwise the first thing
	// the caller sees is whatever incidental step failed first (the generic
	// git adapter has no API fetcher, so it used to surface as "failed to
	// create fetcher", which names neither the real limitation nor the fix).
	if r, ok := prep.publisher.(pullRequestRefuser); ok {
		if err := r.pullRequestSupport(); err != nil {
			return nil, err
		}
	}

	branchName := fmt.Sprintf("ctxloom/%s/%s-%d", opts.ItemType, prep.itemName, time.Now().Unix())

	fetcher, err := pm.fetcherFactory(prep.repoURL, pm.auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create fetcher: %w", err)
	}
	baseSHA, err := fetcher.ResolveRef(ctx, prep.owner, prep.repo, prep.branch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base branch: %w", err)
	}
	if err := prep.publisher.CreateBranch(ctx, prep.owner, prep.repo, branchName, baseSHA); err != nil {
		return nil, fmt.Errorf("failed to create branch: %w", err)
	}

	sha, err := prep.publisher.CreateOrUpdateFile(ctx, prep.owner, prep.repo, prep.remotePath, branchName, prep.commitMessage, prep.content)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}
	// Signature sibling lands on the SAME feature branch, before the PR is
	// opened, so the PR's diff already carries the signed pair together.
	signed, err := publishSignatureSibling(ctx, prep.publisher, prep, branchName)
	if err != nil {
		return nil, fmt.Errorf("branch %s created with %s but its signature failed to publish: %w", branchName, prep.remotePath, err)
	}

	// Cap the on-PR title for readability; preserve any overflow in the body
	// so the full title text survives alongside the message body.
	prTitle, titleOverflow := fitPRTitle(prep.title)
	prBody := buildPRBody(prep.body, titleOverflow, opts.ItemType, prep.itemName, prep.remotePath)
	prURL, err := prep.publisher.CreatePullRequest(ctx, prep.owner, prep.repo, prTitle, prBody, branchName, prep.branch)
	if err != nil {
		return nil, fmt.Errorf("failed to create pull request: %w", err)
	}

	return &PublishResult{Path: prep.remotePath, SHA: sha, PRURL: prURL, Created: prep.created, Signed: signed}, nil
}

// maxPRTitleRunes is the readable cap for PR titles. Conventional git
// subjects target ~72; GitHub's hard limit is 256 bytes, but anything past
// ~72 is unscannable in PR lists. When a caller-supplied title exceeds this,
// we truncate the on-PR title and preserve the full text in the body.
const maxPRTitleRunes = 72

// fitPRTitle returns a PR title trimmed to maxPRTitleRunes (preferring a word
// boundary), and the leftover text the caller should surface in the body so
// the full original title is preserved.
func fitPRTitle(title string) (fitted, overflow string) {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) <= maxPRTitleRunes {
		return title, ""
	}
	runes := []rune(title)
	window := string(runes[:maxPRTitleRunes])
	if i := strings.LastIndex(window, " "); i > 0 {
		return window[:i] + "…", title
	}
	return window + "…", title
}

// SplitTitleBody resolves a commit subject (title) and body from a caller's
// title and message. When no title is given, the first line of the body is
// lifted into the title (and dropped from the body, so subject and body don't
// duplicate); otherwise both are returned trimmed. Shared by publish and push
// so the result accurately reflects the eventual commit/PR shape.
func SplitTitleBody(title, body string) (string, string) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title != "" || body == "" {
		return title, body
	}
	if idx := strings.IndexByte(body, '\n'); idx >= 0 {
		return strings.TrimSpace(body[:idx]), strings.TrimSpace(body[idx+1:])
	}
	return body, ""
}

// resolvePublishTitleAndBody picks the PR title (commit subject) and message
// body from PublishOptions. Order of precedence for the title: caller-supplied
// Title, then the first line of Message, then a generated default.
func resolvePublishTitleAndBody(opts PublishOptions, itemName string, created bool) (title, body string) {
	title, body = SplitTitleBody(opts.Title, opts.Message)
	if title == "" {
		action := "Add"
		if !created {
			action = "Update"
		}
		title = fmt.Sprintf("%s %s %s", action, opts.ItemType, itemName)
	}
	return title, body
}

// buildPRBody assembles the PR description: the message body (caller-supplied
// detail), the full title if it was truncated (so nothing is lost), and the
// canned "where this came from" footer.
func buildPRBody(msgBody, fullTitleIfOverflow string, itemType ItemType, itemName, remotePath string) string {
	footer := fmt.Sprintf("Publishing %s `%s` from local repository.\n\nPath: `%s`", itemType, itemName, remotePath)

	var sections []string
	if fullTitleIfOverflow != "" {
		sections = append(sections, "**Full title:** "+fullTitleIfOverflow)
	}
	if msgBody != "" {
		sections = append(sections, msgBody)
	}
	sections = append(sections, footer)
	return strings.Join(sections, "\n\n---\n\n")
}

// PublishPath constructs the remote file path an item of the given name
// publishes to. It is the ONE definition of the remote layout for publishing:
// callers use it to fill PublishOptions.RemotePath and to report where the
// item went, so the reported path and the written path are the same string
// rather than two expressions that happen to match.
//
// itemType is currently unused: ItemTypeBundle is the only distributed item
// type (see types.go), so every publish target lives under "bundles"; the
// parameter is kept so a future second ItemType doesn't require re-widening the
// signature (the switch this replaced had an identical case and default arm).
func PublishPath(_ ItemType, name string) string {
	return path.Join(paths.RepoContentPrefix, "bundles", name+".yaml")
}

// NewPublisher creates a publisher for the given repository URL: the GitHub
// forge API for github.com, and plain git (clone, write, commit, push) for
// every other host — file://, ssh://, git://, a self-hosted https forge.
//
// AuthConfig carries the GitHub token and NOTHING ELSE, deliberately. The
// generic path takes no credential from ctxloom at all: it runs the git
// binary, whose ~/.ssh/config, ssh-agent, credential helpers and known_hosts
// are already configured and already trusted by the user for every other
// repository. See GitPublisher.
func NewPublisher(repoURL string, auth AuthConfig) (Publisher, error) {
	forgeType, _, err := DetectForge(repoURL)
	if err != nil {
		return nil, err
	}

	switch forgeType {
	case ForgeGitHub:
		return NewGitHubPublisher(auth.GitHub), nil
	case ForgeGitGeneric:
		return NewGitPublisher(repoURL)
	default:
		return nil, fmt.Errorf("unsupported forge for publishing: %s", repoURL)
	}
}
