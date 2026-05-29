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

// Publish publishes a local item to a remote repository.
func (pm *PublishManager) Publish(ctx context.Context, localPath string, remoteName string, opts PublishOptions) (*PublishResult, error) {
	// Use provided filesystem or manager's default
	fs := opts.FS
	if fs == nil {
		fs = pm.fs
	}

	// Read local content
	content, err := afero.ReadFile(fs, localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read local file: %w", err)
	}

	// Transform profile content if needed (convert local names to canonical URLs)
	if opts.ItemType == ItemTypeProfile {
		content, err = transformProfileForExport(content, pm.lockfileManager)
		if err != nil {
			return nil, fmt.Errorf("failed to transform profile for export: %w", err)
		}
	}

	// Get remote configuration
	rem, err := pm.registry.Get(remoteName)
	if err != nil {
		return nil, fmt.Errorf("remote not found: %w", err)
	}

	// Create publisher
	publisher, err := pm.publisherFactory(rem.URL, pm.auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}

	// Parse repository URL
	owner, repo, err := ParseRepoURL(rem.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid remote URL: %w", err)
	}

	// Determine item name from local path
	itemName := strings.TrimSuffix(filepath.Base(localPath), ".yaml")

	// Build remote path
	remotePath := buildPublishPath(opts.ItemType, itemName)

	// Get default branch if not specified
	branch := opts.Branch
	if branch == "" {
		fetcher, err := pm.fetcherFactory(rem.URL, pm.auth)
		if err != nil {
			return nil, fmt.Errorf("failed to create fetcher: %w", err)
		}
		branch, err = fetcher.GetDefaultBranch(ctx, owner, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to get default branch: %w", err)
		}
	}

	// Add source metadata to content
	contentWithMeta, err := addPublishMetadata(content, localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to add metadata: %w", err)
	}

	// Check if file exists
	existingSHA, _ := publisher.GetFileSHA(ctx, owner, repo, remotePath, branch)
	created := existingSHA == ""

	// Resolve title and message body. Title is the PR title and commit subject;
	// Message is the commit/PR body. Either may be empty — fall back through
	// (caller title) → (first line of Message) → (generated default).
	title, msgBody := resolvePublishTitleAndBody(opts, itemName, created)

	// Commit message follows git convention: subject, blank line, body.
	commitMessage := title
	if msgBody != "" {
		commitMessage = title + "\n\n" + msgBody
	}

	var result *PublishResult

	if opts.CreatePR {
		// Create a new branch and PR
		branchName := fmt.Sprintf("ctxloom/%s/%s-%d", opts.ItemType, itemName, time.Now().Unix())

		// Get base SHA for branch creation
		fetcher, err := pm.fetcherFactory(rem.URL, pm.auth)
		if err != nil {
			return nil, fmt.Errorf("failed to create fetcher: %w", err)
		}
		baseSHA, err := fetcher.ResolveRef(ctx, owner, repo, branch)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve base branch: %w", err)
		}

		// Create branch
		if err := publisher.CreateBranch(ctx, owner, repo, branchName, baseSHA); err != nil {
			return nil, fmt.Errorf("failed to create branch: %w", err)
		}

		// Create/update file on new branch
		sha, err := publisher.CreateOrUpdateFile(ctx, owner, repo, remotePath, branchName, commitMessage, contentWithMeta)
		if err != nil {
			return nil, fmt.Errorf("failed to create file: %w", err)
		}

		// Cap the on-PR title for readability; preserve any overflow in the body
		// so the full title text survives alongside the message body.
		prTitle, titleOverflow := fitPRTitle(title)
		prBody := buildPRBody(msgBody, titleOverflow, opts.ItemType, itemName, remotePath)
		prURL, err := publisher.CreatePullRequest(ctx, owner, repo, prTitle, prBody, branchName, branch)
		if err != nil {
			return nil, fmt.Errorf("failed to create pull request: %w", err)
		}

		result = &PublishResult{
			Path:    remotePath,
			SHA:     sha,
			PRURL:   prURL,
			Created: created,
		}
	} else {
		// Direct push to branch
		sha, err := publisher.CreateOrUpdateFile(ctx, owner, repo, remotePath, branch, commitMessage, contentWithMeta)
		if err != nil {
			return nil, fmt.Errorf("failed to publish: %w", err)
		}

		result = &PublishResult{
			Path:    remotePath,
			SHA:     sha,
			Created: created,
		}
	}

	return result, nil
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

// resolvePublishTitleAndBody picks the PR title (commit subject) and message
// body from PublishOptions. Order of precedence for the title: caller-supplied
// Title, then the first line of Message, then a generated default.
func resolvePublishTitleAndBody(opts PublishOptions, itemName string, created bool) (title, body string) {
	body = strings.TrimSpace(opts.Message)
	title = strings.TrimSpace(opts.Title)

	if title == "" && body != "" {
		// Pull the first line off the body; if it's the only line, the body is
		// empty after the lift (no duplication between subject and body).
		if idx := strings.IndexByte(body, '\n'); idx >= 0 {
			title = strings.TrimSpace(body[:idx])
			body = strings.TrimSpace(body[idx+1:])
		} else {
			title = body
			body = ""
		}
	}

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

// transformProfileForExport converts local bundle references to canonical URLs.
// This is used when publishing/exporting a profile for sharing.
func transformProfileForExport(content []byte, lm *LockfileManager) ([]byte, error) {
	// Parse the profile
	var rawProfile map[string]interface{}
	if err := yaml.Unmarshal(content, &rawProfile); err != nil {
		// Not valid YAML, return as-is
		return content, nil
	}

	// Check if there are bundles to transform
	bundlesRaw, ok := rawProfile["bundles"]
	if !ok {
		return content, nil // No bundles, return as-is
	}

	bundles, ok := bundlesRaw.([]interface{})
	if !ok {
		return content, nil // Not a list, return as-is
	}

	// Check if any bundles need transformation (are local names, not URLs)
	hasLocalRefs := false
	for _, b := range bundles {
		bundleStr, ok := b.(string)
		if !ok {
			continue
		}
		if !strings.HasPrefix(bundleStr, "https://") &&
			!strings.HasPrefix(bundleStr, "http://") &&
			!strings.HasPrefix(bundleStr, "git@") {
			hasLocalRefs = true
			break
		}
	}

	if !hasLocalRefs {
		return content, nil // All already canonical
	}

	// Load lockfile to get canonical URLs
	lockfile, err := lm.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load lockfile: %w", err)
	}

	// Transform the bundles
	transformedBundles := make([]string, 0, len(bundles))

	for _, b := range bundles {
		bundleStr, ok := b.(string)
		if !ok {
			continue
		}

		// Check if this is already a canonical URL
		if strings.HasPrefix(bundleStr, "https://") ||
			strings.HasPrefix(bundleStr, "http://") ||
			strings.HasPrefix(bundleStr, "git@") {
			// Already canonical, keep as-is
			transformedBundles = append(transformedBundles, bundleStr)
			continue
		}

		// Parse item path suffix (e.g., #fragments/name)
		var itemPath string
		localName := bundleStr
		if hashIdx := strings.Index(bundleStr, "#"); hashIdx != -1 {
			localName = bundleStr[:hashIdx]
			itemPath = bundleStr[hashIdx:]
		}

		// Look up in lockfile to get canonical URL
		canonicalURL, found := lockfile.GetCanonicalURL(ItemTypeBundle, localName)
		if !found {
			return nil, fmt.Errorf("bundle %q not found in lockfile; pull it first before publishing", localName)
		}

		// Add item path if present
		canonicalRef := canonicalURL + itemPath
		transformedBundles = append(transformedBundles, canonicalRef)
	}

	// Update the profile with transformed bundles
	rawProfile["bundles"] = transformedBundles

	// Re-marshal the profile
	return yaml.Marshal(rawProfile)
}
