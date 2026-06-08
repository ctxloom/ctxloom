package operations

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// parseRemoteItemType maps the request item_type string to a remote.ItemType,
// accepting only "bundle" and "profile".
func parseRemoteItemType(s string) (remote.ItemType, error) {
	switch s {
	case "bundle":
		return remote.ItemTypeBundle, nil
	case "profile":
		return remote.ItemTypeProfile, nil
	}
	return "", fmt.Errorf("invalid item_type: %s (only bundle and profile supported)", s)
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
	Installation  string   `json:"installation,omitempty"` // Setup instructions for the user
}

// PullItem performs a direct pull operation using the existing Puller.
// This wraps the remote.Puller with correct config-based LocalDir.
func PullItem(ctx context.Context, cfg *config.Config, req PullItemRequest) (*PullItemResult, error) {
	itemType, err := parseRemoteItemType(req.ItemType)
	if err != nil {
		return nil, err
	}

	baseDir := getBaseDir(cfg)
	puller, err := resolvePullItemPuller(cfg, req, baseDir)
	if err != nil {
		return nil, err
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
		Installation:  bundleInstallation(itemType, result.Content),
	}, nil
}

// resolvePullItemPuller returns the injected puller, or builds one from an
// injected/loaded registry.
func resolvePullItemPuller(cfg *config.Config, req PullItemRequest, baseDir string) (Puller, error) {
	if req.Puller != nil {
		return req.Puller, nil
	}
	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(getFS(req.FS)))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}
	auth := remote.LoadAuth(baseDir)
	return remote.NewPuller(registry, auth, remote.WithFetcherFactory(newCachedFetcherFactory(cfg))), nil
}

// bundleInstallation extracts a pulled bundle's installation instructions from
// its content. After PR 1 the bundle bytes come back on result.Content
// (LocalPath is synthetic), so parse those directly instead of reading disk.
// Returns "" for non-bundles, empty content, or parse failures.
func bundleInstallation(itemType remote.ItemType, content []byte) string {
	if itemType != remote.ItemTypeBundle || len(content) == 0 {
		return ""
	}
	bundle, err := bundles.ParseBundle(content)
	if err != nil {
		return ""
	}
	return bundle.Installation
}
