package operations

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/remote"
)

// FetchRemoteContentRequest contains parameters for fetching remote content.
type FetchRemoteContentRequest struct {
	Reference string `json:"reference"`
	ItemType  string `json:"item_type"` // "bundle" or "profile"

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
	// Fetcher is an optional pre-configured fetcher (for testing).
	Fetcher remote.Fetcher `json:"-"`
}

// FetchRemoteContentResult contains the fetched content.
type FetchRemoteContentResult struct {
	Reference  string `json:"reference"`
	ItemType   string `json:"item_type"`
	SHA        string `json:"sha"`
	FullSHA    string `json:"full_sha"`
	SourceURL  string `json:"source_url"`
	FilePath   string `json:"file_path"`
	Content    string `json:"content"`
	PullToken  string `json:"pull_token"`
	Warning    string `json:"warning"`
	RemoteName string `json:"-"` // Internal use
}

// FetchRemoteContent fetches content from a remote for preview.
// This is used by the MCP preview_remote tool.
func FetchRemoteContent(ctx context.Context, cfg *config.Config, req FetchRemoteContentRequest) (*FetchRemoteContentResult, error) {
	if req.Reference == "" {
		return nil, fmt.Errorf("reference is required")
	}
	if req.ItemType == "" {
		return nil, fmt.Errorf("item_type is required")
	}

	var itemType remote.ItemType
	switch req.ItemType {
	case "bundle":
		itemType = remote.ItemTypeBundle
	case "profile":
		itemType = remote.ItemTypeProfile
	default:
		return nil, fmt.Errorf("invalid item_type: %s (only bundle and profile supported)", req.ItemType)
	}

	ref, err := remote.ParseReference(req.Reference)
	if err != nil {
		return nil, fmt.Errorf("invalid reference: %w", err)
	}

	// Use injected registry or load from config
	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to load registry: %w", err)
		}
	}

	rem, err := registry.Get(ref.Remote)
	if err != nil {
		return nil, err
	}

	// Use injected fetcher or create one
	fetcher := req.Fetcher
	if fetcher == nil {
		baseDir := getBaseDir(cfg)
		auth := remote.LoadAuth(baseDir)
		var err error
		fetcher, err = remote.NewFetcher(rem.URL, auth)
		if err != nil {
			return nil, fmt.Errorf("failed to create fetcher: %w", err)
		}
	}

	owner, repo, err := remote.ParseRepoURL(rem.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid remote URL: %w", err)
	}

	// Resolve ref to SHA
	gitRef := ref.GitRef
	if gitRef == "" {
		gitRef, err = fetcher.GetDefaultBranch(ctx, owner, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to get default branch: %w", err)
		}
	}

	sha, err := fetcher.ResolveRef(ctx, owner, repo, gitRef)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ref '%s': %w", gitRef, err)
	}

	// Fetch content
	filePath := ref.BuildFilePath(itemType, rem.Version)
	content, err := fetcher.FetchFile(ctx, owner, repo, filePath, sha)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch: %w", err)
	}

	// Generate pull token (item_type:reference@SHA for re-fetch capability)
	pullToken := fmt.Sprintf("%s:%s/%s@%s", req.ItemType, ref.Remote, ref.Path, sha)

	shortSHA := sha
	if len(sha) > 7 {
		shortSHA = sha[:7]
	}

	return &FetchRemoteContentResult{
		Reference:  req.Reference,
		ItemType:   req.ItemType,
		SHA:        shortSHA,
		FullSHA:    sha,
		SourceURL:  rem.URL,
		FilePath:   filePath,
		Content:    string(content),
		PullToken:  pullToken,
		Warning:    "REVIEW THIS CONTENT CAREFULLY. Malicious prompts can override AI safety guidelines, exfiltrate data, or execute unintended actions. Use confirm_pull with the pull_token to install.",
		RemoteName: ref.Remote,
	}, nil
}

// WriteRemoteItemRequest contains parameters for writing a fetched item.
type WriteRemoteItemRequest struct {
	Reference string   `json:"reference"` // The pull token reference
	ItemType  string   `json:"item_type"`
	Content   []byte   `json:"-"`
	SHA       string   `json:"sha"`
	FS        afero.Fs `json:"-"` // Optional filesystem (defaults to OS filesystem if nil)
}

// WriteRemoteItemResult contains the result of writing an item.
type WriteRemoteItemResult struct {
	Status      string `json:"status"` // "installed" or "updated"
	Reference   string `json:"reference"`
	ItemType    string `json:"item_type"`
	LocalPath   string `json:"local_path"`
	SHA         string `json:"sha"`
	Overwritten bool   `json:"overwritten"`
}

// WriteRemoteItem writes fetched content to the local filesystem.
// This is used by the MCP confirm_pull tool.
func WriteRemoteItem(ctx context.Context, cfg *config.Config, req WriteRemoteItemRequest) (*WriteRemoteItemResult, error) {
	// Use provided filesystem or default to OS
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	var itemType remote.ItemType
	switch req.ItemType {
	case "bundle":
		itemType = remote.ItemTypeBundle
	case "profile":
		itemType = remote.ItemTypeProfile
	default:
		return nil, fmt.Errorf("invalid item_type: %s", req.ItemType)
	}

	ref, err := remote.ParseReference(req.Reference)
	if err != nil {
		return nil, fmt.Errorf("invalid reference: %w", err)
	}

	// Use config's SCM path - THIS IS THE BUG FIX
	baseDir := getBaseDir(cfg)
	localPath := ref.LocalPath(baseDir, itemType)

	// Check for existing file
	overwritten := false
	if _, err := fs.Stat(localPath); err == nil {
		overwritten = true
	}

	// Write file
	if err := fs.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	if err := afero.WriteFile(fs, localPath, req.Content, 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	shortSHA := req.SHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	status := "installed"
	if overwritten {
		status = "updated"
	}

	return &WriteRemoteItemResult{
		Status:      status,
		Reference:   req.Reference,
		ItemType:    req.ItemType,
		LocalPath:   localPath,
		SHA:         shortSHA,
		Overwritten: overwritten,
	}, nil
}

// PullItemRequest contains parameters for a direct pull operation.
type PullItemRequest struct {
	Reference string `json:"reference"`
	ItemType  string `json:"item_type"` // "bundle" or "profile"
	Force     bool   `json:"force"`
	Blind     bool   `json:"blind"` // Skip security review display (implies Force)
	Cascade   bool   `json:"cascade"`

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
	// Puller is an optional pre-configured puller (for testing).
	Puller Puller `json:"-"`
	// FS is an optional filesystem (for testing).
	FS afero.Fs `json:"-"`
}

// PullItemResult contains the result of a pull operation.
type PullItemResult struct {
	LocalPath     string   `json:"local_path"`
	SHA           string   `json:"sha"`
	Overwritten   bool     `json:"overwritten"`
	CascadePulled []string `json:"cascade_pulled,omitempty"`
}

// PullItem performs a direct pull operation using the existing Puller.
// This wraps the remote.Puller with correct config-based LocalDir.
func PullItem(ctx context.Context, cfg *config.Config, req PullItemRequest) (*PullItemResult, error) {
	var itemType remote.ItemType
	switch req.ItemType {
	case "bundle":
		itemType = remote.ItemTypeBundle
	case "profile":
		itemType = remote.ItemTypeProfile
	default:
		return nil, fmt.Errorf("invalid item_type: %s (only bundle and profile supported)", req.ItemType)
	}

	// Use injected puller or create one
	puller := req.Puller
	baseDir := getBaseDir(cfg)
	if puller == nil {
		// Use injected registry or load from config
		registry := req.Registry
		if registry == nil {
			fs := req.FS
			if fs == nil {
				fs = afero.NewOsFs()
			}
			var err error
			registry, err = getRegistry(cfg, remote.WithRegistryFS(fs))
			if err != nil {
				return nil, fmt.Errorf("failed to initialize registry: %w", err)
			}
		}

		auth := remote.LoadAuth(baseDir)
		puller = remote.NewPuller(registry, auth)
	}

	opts := remote.PullOptions{
		LocalDir: baseDir, // THIS IS THE BUG FIX
		Force:    req.Force,
		Blind:    req.Blind,
		ItemType: itemType,
		Cascade:  req.Cascade,
	}

	result, err := puller.Pull(ctx, req.Reference, opts)
	if err != nil {
		return nil, err
	}

	return &PullItemResult{
		LocalPath:     result.LocalPath,
		SHA:           result.SHA,
		Overwritten:   result.Overwritten,
		CascadePulled: result.CascadePulled,
	}, nil
}
