package operations

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// canonicalizeUserRef accepts a convenience short-form reference
// ("<remote-alias>/<path>[#<sel>/<item>]") as USER INPUT and expands it to a
// canonical "<url>@bundles/<path>[#<sel>/<item>]" ref via the registry. Anything
// already scheme-qualified (a canonical URL or ctxloom:local) — or an
// unresolvable alias, or a bare unprefixed name — is returned unchanged for the
// downstream parser to accept or reject. The short form is a CLI convenience
// only: this resolved canonical ref is what flows on to the lockfile and the
// on-disk install path, so nothing short is ever stored. Delegates to the shared
// short-name choke (remote.CanonicalizeShortRef) so the widened grammar —
// selector preservation, local-only bare names — lives in one place.
func canonicalizeUserRef(reference string, _ remote.ItemType, registry *remote.Registry) string {
	return remote.CanonicalizeShortRef(reference, registryAliasToURL(registry), nil)
}

// CanonicalizeRemoteRef expands a convenience short-form reference
// ("<alias>/<path>") to its canonical "<url>@<kind>/<path>" form via the
// registry, leaving an already-canonical (or unresolvable) ref unchanged. CLI
// commands that look items up by their canonical lockfile key (`bundle
// hold`/`unhold`) use it so they accept the same short input as install.
// Returns the ref unchanged when no registry is available.
func CanonicalizeRemoteRef(cfg *config.Config, ref string, itemType remote.ItemType) string {
	registry, err := getRegistry(cfg, remote.WithRegistryFS(getFS(nil)))
	if err != nil {
		return ref
	}
	return canonicalizeUserRef(ref, itemType, registry)
}

// parseRemoteItemType maps the request item_type string to a remote.ItemType.
// Only bundles are directly pullable; profiles now travel inside bundles.
func parseRemoteItemType(s string) (remote.ItemType, error) {
	switch s {
	case "bundle":
		return remote.ItemTypeBundle, nil
	}
	return "", fmt.Errorf("invalid item_type: %s (only bundle supported)", s)
}

// PullItemRequest contains parameters for a direct pull operation.
type PullItemRequest struct {
	Reference string `json:"reference"`
	ItemType  string `json:"item_type"` // "bundle" or "profile"
	Force     bool   `json:"force"`

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
	// Puller is an optional pre-configured puller (for testing).
	Puller Puller `json:"-"`
	// FS is an optional filesystem (for testing).
	FS afero.Fs `json:"-"`
}

// PullItemResult contains the result of a pull operation.
type PullItemResult struct {
	Reference    string `json:"reference"` // canonical ref the item was locked under
	LocalPath    string `json:"local_path"`
	SHA          string `json:"sha"`
	Overwritten  bool   `json:"overwritten"`
	Installation string `json:"installation,omitempty"` // Setup instructions for the user
}

// PullItem performs a direct pull operation using the existing Puller.
// This wraps the remote.Puller with correct config-based LocalDir.
func PullItem(ctx context.Context, cfg *config.Config, req PullItemRequest) (*PullItemResult, error) {
	itemType, err := parseRemoteItemType(req.ItemType)
	if err != nil {
		return nil, err
	}

	baseDir := getBaseDir(cfg)

	// Resolve the registry once: used both to canonicalize a convenience
	// short-form reference and to build the puller.
	registry := req.Registry
	if registry == nil {
		registry, err = getRegistry(cfg, remote.WithRegistryFS(getFS(req.FS)))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}
	req.Registry = registry

	puller, err := resolvePullItemPuller(cfg, req, baseDir)
	if err != nil {
		return nil, err
	}

	opts := remote.PullOptions{
		LocalDir: baseDir,
		Force:    req.Force,
		ItemType: itemType,
	}

	// Short forms ("<remote-alias>/<path>") are accepted as USER INPUT for
	// convenience and resolved to a canonical ref here; canonical is the sole
	// stored identity (lockfile, install path), so nothing short is persisted.
	ref := canonicalizeUserRef(req.Reference, itemType, registry)

	result, err := puller.Pull(ctx, ref, opts)
	if err != nil {
		return nil, err
	}

	// The canonical ref is the locked identity — the lockfile key callers wire
	// into a local profile. Derive it from the resolved input ref.
	canonical := ref
	if parsed, perr := remote.ParseReference(ref); perr == nil {
		canonical = parsed.CanonicalString()
	}

	return &PullItemResult{
		Reference:    canonical,
		LocalPath:    result.LocalPath,
		SHA:          result.SHA,
		Overwritten:  result.Overwritten,
		Installation: bundleInstallation(itemType, result.Content),
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
	return remote.NewPuller(registry, auth, remote.WithFetcherFactory(NewCachedFetcherFactory(cfg))), nil
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
