package remote

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
		fetcherFactory:   defaultFetcherFactory,
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

	// Message is the commit message.
	Message string

	// Force overwrites existing content without confirmation.
	Force bool

	// ItemType specifies what type of item to publish.
	ItemType ItemType

	// Version is the SCM version directory (e.g., "v1").
	Version string

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
	version := opts.Version
	if version == "" {
		version = rem.Version
	}
	if version == "" {
		version = "v1"
	}

	remotePath := buildPublishPath(opts.ItemType, version, itemName)

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

	// Build commit message
	message := opts.Message
	if message == "" {
		action := "Add"
		if !created {
			action = "Update"
		}
		message = fmt.Sprintf("%s %s %s", action, opts.ItemType, itemName)
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
		sha, err := publisher.CreateOrUpdateFile(ctx, owner, repo, remotePath, branchName, message, contentWithMeta)
		if err != nil {
			return nil, fmt.Errorf("failed to create file: %w", err)
		}

		// Create PR
		prTitle := message
		prBody := fmt.Sprintf("Publishing %s `%s` from local repository.\n\nPath: `%s`", opts.ItemType, itemName, remotePath)
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
		sha, err := publisher.CreateOrUpdateFile(ctx, owner, repo, remotePath, branch, message, contentWithMeta)
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

// Publish is a convenience function that creates a PublishManager and publishes.
// Deprecated: Use NewPublishManager and the Publish method for better testability.
func Publish(ctx context.Context, localPath string, remoteName string, opts PublishOptions, registry *Registry, auth AuthConfig) (*PublishResult, error) {
	pm := NewPublishManager(registry, auth, WithPublishFS(opts.FS))
	return pm.Publish(ctx, localPath, remoteName, opts)
}

// buildPublishPath constructs the remote file path for an item.
func buildPublishPath(itemType ItemType, version, name string) string {
	var dir string
	switch itemType {
	case ItemTypeBundle:
		dir = "bundles"
	case ItemTypeProfile:
		dir = "profiles"
	default:
		dir = "bundles"
	}
	return fmt.Sprintf("ctxloom/%s/%s/%s.yaml", version, dir, name)
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
