package remote

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
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
	lockfileManager  *LockfileManager
	fs               afero.Fs
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

// WithPublishLockfileManager sets a custom lockfile manager (for testing).
func WithPublishLockfileManager(lm *LockfileManager) PublishManagerOption {
	return func(pm *PublishManager) {
		pm.lockfileManager = lm
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

	// Initialize defaults for nil dependencies
	if pm.lockfileManager == nil {
		pm.lockfileManager = NewLockfileManager(".ctxloom")
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

	// Force overwrites existing content without confirmation.
	Force bool

	// ItemType specifies what type of item to publish.
	ItemType ItemType

	// FS is the filesystem to use (defaults to OS filesystem if nil).
	FS afero.Fs
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
}

// publishPrep holds everything resolved before the push/PR strategy runs.
type publishPrep struct {
	publisher     Publisher
	repoURL       string
	owner, repo   string
	itemName      string
	remotePath    string
	branch        string
	content       []byte // content with publish metadata added
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
	if opts.CreatePR {
		return pm.publishViaPR(ctx, prep, opts)
	}
	return pm.publishDirect(ctx, prep)
}

// loadPublishContent reads the local file and, for profiles, rewrites local
// bundle references to canonical URLs before export.
func (pm *PublishManager) loadPublishContent(localPath string, opts PublishOptions) ([]byte, error) {
	fs := opts.FS
	if fs == nil {
		fs = pm.fs
	}
	content, err := afero.ReadFile(fs, localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read local file: %w", err)
	}
	if opts.ItemType == ItemTypeProfile {
		content, err = transformProfileForExport(content, pm.lockfileManager)
		if err != nil {
			return nil, fmt.Errorf("failed to transform profile for export: %w", err)
		}
	}
	return content, nil
}

// resolvePublishBranch returns opts.Branch, or the repo's default branch when
// the caller didn't pin one.
func (pm *PublishManager) resolvePublishBranch(ctx context.Context, repoURL, owner, repo, optBranch string) (string, error) {
	if optBranch != "" {
		return optBranch, nil
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
// content (with metadata), and commit subject/body shared by both push
// strategies.
func (pm *PublishManager) preparePublish(ctx context.Context, localPath, remoteName string, opts PublishOptions) (*publishPrep, error) {
	content, err := pm.loadPublishContent(localPath, opts)
	if err != nil {
		return nil, err
	}

	rem, err := pm.registry.Get(remoteName)
	if err != nil {
		return nil, fmt.Errorf("remote not found: %w", err)
	}
	publisher, err := pm.publisherFactory(rem.URL, pm.auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}
	owner, repo, err := ParseRepoURL(rem.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid remote URL: %w", err)
	}

	itemName := strings.TrimSuffix(filepath.Base(localPath), ".yaml")
	remotePath := buildPublishPath(opts.ItemType, itemName)

	branch, err := pm.resolvePublishBranch(ctx, rem.URL, owner, repo, opts.Branch)
	if err != nil {
		return nil, err
	}

	contentWithMeta, err := addPublishMetadata(content, localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to add metadata: %w", err)
	}

	// Existing file (if any) decides created vs updated and the default title.
	existingSHA, _ := publisher.GetFileSHA(ctx, owner, repo, remotePath, branch)
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
		content:       contentWithMeta,
		title:         title,
		body:          body,
		commitMessage: buildCommitMessage(title, body),
		created:       created,
	}, nil
}

// buildCommitMessage assembles a git-convention commit message: subject, blank
// line, body.
func buildCommitMessage(title, body string) string {
	if body == "" {
		return title
	}
	return title + "\n\n" + body
}

// publishDirect pushes the content straight to the target branch.
func (pm *PublishManager) publishDirect(ctx context.Context, prep *publishPrep) (*PublishResult, error) {
	sha, err := prep.publisher.CreateOrUpdateFile(ctx, prep.owner, prep.repo, prep.remotePath, prep.branch, prep.commitMessage, prep.content)
	if err != nil {
		return nil, fmt.Errorf("failed to publish: %w", err)
	}
	return &PublishResult{Path: prep.remotePath, SHA: sha, Created: prep.created}, nil
}

// publishViaPR creates a feature branch, commits the content there, and opens a
// pull request against the target branch.
func (pm *PublishManager) publishViaPR(ctx context.Context, prep *publishPrep, opts PublishOptions) (*PublishResult, error) {
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

	// Cap the on-PR title for readability; preserve any overflow in the body
	// so the full title text survives alongside the message body.
	prTitle, titleOverflow := fitPRTitle(prep.title)
	prBody := buildPRBody(prep.body, titleOverflow, opts.ItemType, prep.itemName, prep.remotePath)
	prURL, err := prep.publisher.CreatePullRequest(ctx, prep.owner, prep.repo, prTitle, prBody, branchName, prep.branch)
	if err != nil {
		return nil, fmt.Errorf("failed to create pull request: %w", err)
	}

	return &PublishResult{Path: prep.remotePath, SHA: sha, PRURL: prURL, Created: prep.created}, nil
}

// buildPublishPath constructs the remote file path for an item.
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

func buildPublishPath(itemType ItemType, name string) string {
	var dir string
	switch itemType {
	case ItemTypeBundle:
		dir = "bundles"
	case ItemTypeProfile:
		dir = "profiles"
	default:
		dir = "bundles"
	}
	return fmt.Sprintf("ctxloom/%s/%s.yaml", dir, name)
}

// addPublishMetadata adds _source metadata to content for tracking.
func addPublishMetadata(content []byte, localPath string) ([]byte, error) {
	// Parse YAML
	var data map[string]interface{}
	if err := yaml.Unmarshal(content, &data); err != nil {
		// Not valid YAML, return as-is
		return content, nil
	}

	// Add publish metadata
	data["_published"] = map[string]interface{}{
		"from":         localPath,
		"published_at": time.Now().UTC().Format(time.RFC3339),
	}

	return yaml.Marshal(data)
}

// NewPublisher creates a publisher for the given repository URL.
func NewPublisher(repoURL string, auth AuthConfig) (Publisher, error) {
	forgeType, baseURL, err := DetectForge(repoURL)
	if err != nil {
		return nil, err
	}

	switch forgeType {
	case ForgeGitHub:
		return NewGitHubPublisher(auth.GitHub), nil
	case ForgeGitLab:
		return NewGitLabPublisher(baseURL, auth.GitLab), nil
	default:
		return nil, fmt.Errorf("unsupported forge for publishing: %s", repoURL)
	}
}

// profileHasLocalBundleRefs reports whether any bundle entry is a local name
// (i.e. needs canonicalization before export).
func profileHasLocalBundleRefs(bundles []interface{}) bool {
	for _, b := range bundles {
		if s, ok := b.(string); ok && !IsCanonicalRef(s) {
			return true
		}
	}
	return false
}

// canonicalizeBundleRef resolves a single local bundle name to its canonical
// URL via the lockfile, preserving any #item-path suffix. Canonical refs are
// returned unchanged by the caller, so this is only invoked for local names.
func canonicalizeBundleRef(ref string, lockfile *Lockfile) (string, error) {
	localName := ref
	var itemPath string
	if hashIdx := strings.Index(ref, "#"); hashIdx != -1 {
		localName = ref[:hashIdx]
		itemPath = ref[hashIdx:]
	}

	canonicalURL, found := lockfile.GetCanonicalURL(ItemTypeBundle, localName)
	if !found {
		return "", fmt.Errorf("bundle %q not found in lockfile; pull it first before publishing", localName)
	}
	return canonicalURL + itemPath, nil
}

// transformBundleRefs maps each profile bundle entry to its canonical form:
// already-canonical refs pass through, local names resolve via the lockfile.
func transformBundleRefs(bundles []interface{}, lockfile *Lockfile) ([]string, error) {
	out := make([]string, 0, len(bundles))
	for _, b := range bundles {
		ref, ok := b.(string)
		if !ok {
			continue
		}
		if IsCanonicalRef(ref) {
			out = append(out, ref)
			continue
		}
		canonical, err := canonicalizeBundleRef(ref, lockfile)
		if err != nil {
			return nil, err
		}
		out = append(out, canonical)
	}
	return out, nil
}

// transformProfileForExport converts local bundle references to canonical URLs.
// This is used when publishing/exporting a profile for sharing.
func transformProfileForExport(content []byte, lm *LockfileManager) ([]byte, error) {
	var rawProfile map[string]interface{}
	if err := yaml.Unmarshal(content, &rawProfile); err != nil {
		return content, nil // Not valid YAML, return as-is
	}

	bundlesRaw, ok := rawProfile["bundles"]
	if !ok {
		return content, nil // No bundles, return as-is
	}
	bundles, ok := bundlesRaw.([]interface{})
	if !ok {
		return content, nil // Not a list, return as-is
	}

	if !profileHasLocalBundleRefs(bundles) {
		return content, nil // All already canonical
	}

	lockfile, err := lm.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load lockfile: %w", err)
	}

	transformed, err := transformBundleRefs(bundles, lockfile)
	if err != nil {
		return nil, err
	}

	rawProfile["bundles"] = transformed
	return yaml.Marshal(rawProfile)
}
