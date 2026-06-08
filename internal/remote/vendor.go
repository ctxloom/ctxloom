package remote

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// VendorManager handles vendoring remote dependencies locally.
type VendorManager struct {
	baseDir        string
	fs             afero.Fs
	fetcherFactory FetcherFactory
}

// VendorOption is a functional option for configuring a VendorManager.
type VendorOption func(*VendorManager)

// WithVendorFS sets a custom filesystem implementation (for testing).
func WithVendorFS(fs afero.Fs) VendorOption {
	return func(m *VendorManager) {
		m.fs = fs
	}
}

// WithVendorFetcherFactory sets a custom fetcher factory (for testing).
func WithVendorFetcherFactory(ff FetcherFactory) VendorOption {
	return func(m *VendorManager) {
		m.fetcherFactory = ff
	}
}

// NewVendorManager creates a new vendor manager.
func NewVendorManager(baseDir string, opts ...VendorOption) *VendorManager {
	if baseDir == "" {
		baseDir = paths.AppDirName
	}
	m := &VendorManager{
		baseDir:        baseDir,
		fs:             afero.NewOsFs(),
		fetcherFactory: DefaultFetcherFactory,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// VendorDir returns the vendor directory path.
// Vendor directory is in .ctxloom/cache/vendor.
func (m *VendorManager) VendorDir() string {
	return paths.VendorPath(m.baseDir)
}

// IsVendored checks if vendor mode is enabled.
func (m *VendorManager) IsVendored() bool {
	configPath := paths.DefaultRemotesPath()
	data, err := afero.ReadFile(m.fs, configPath)
	if err != nil {
		return false
	}

	var cfg struct {
		Vendor bool `yaml:"vendor"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false
	}

	return cfg.Vendor
}

// SetVendorMode enables or disables vendor mode.
func (m *VendorManager) SetVendorMode(enabled bool) error {
	configPath := paths.DefaultRemotesPath()

	// Ensure base directory exists (remotes.yaml is at root level)
	if err := m.fs.MkdirAll(paths.AppDirName, 0755); err != nil {
		return err
	}

	var existingRaw map[string]interface{}
	data, err := afero.ReadFile(m.fs, configPath)
	if err == nil {
		_ = yaml.Unmarshal(data, &existingRaw)
	}
	if existingRaw == nil {
		existingRaw = make(map[string]interface{})
	}

	if enabled {
		existingRaw["vendor"] = true
	} else {
		delete(existingRaw, "vendor")
	}

	out, err := yaml.Marshal(existingRaw)
	if err != nil {
		return err
	}

	return afero.WriteFile(m.fs, configPath, out, 0644)
}

// VendorAll copies all locked dependencies to the vendor directory.
func (m *VendorManager) VendorAll(ctx context.Context, lockfile *Lockfile, registry *Registry, auth AuthConfig) error {
	vendorDir := m.VendorDir()

	// Clean existing vendor directory
	if err := m.fs.RemoveAll(vendorDir); err != nil {
		return fmt.Errorf("failed to clean vendor directory: %w", err)
	}

	entries := lockfile.AllEntries()
	if len(entries) == 0 {
		return fmt.Errorf("no entries in lockfile")
	}

	for _, e := range entries {
		if err := m.vendorEntry(ctx, e.Type, e.Ref, e.Entry, registry, auth, vendorDir); err != nil {
			return err
		}
	}

	return nil
}

// vendorEntry fetches one locked entry at its pinned SHA and writes it into the
// vendor directory.
func (m *VendorManager) vendorEntry(ctx context.Context, itemType ItemType, entryRef string, entry LockEntry, registry *Registry, auth AuthConfig, vendorDir string) error {
	ref, err := ParseReference(entryRef)
	if err != nil || !ref.IsCanonical {
		return fmt.Errorf("invalid reference %s: %w", entryRef, err)
	}

	// Canonical keys carry the repo URL directly — no registry lookup needed.
	fetcher, err := m.fetcherFactory(ref.URL, auth)
	if err != nil {
		return fmt.Errorf("failed to create fetcher: %w", err)
	}

	owner, repo, err := ParseRepoURL(ref.URL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	filePath := ref.BuildFilePath(itemType)
	content, err := fetcher.FetchFile(ctx, owner, repo, filePath, entry.SHA)
	if err != nil {
		return fmt.Errorf("failed to fetch %s: %w", entryRef, err)
	}

	vendorPath := filepath.Join(vendorDir, string(itemType)+"s", ref.LocalRemoteName(), ref.Path+".yaml")
	if err := m.fs.MkdirAll(filepath.Dir(vendorPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := afero.WriteFile(m.fs, vendorPath, content, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", vendorPath, err)
	}
	return nil
}

// GetVendored returns content from the vendor directory if available.
func (m *VendorManager) GetVendored(itemType ItemType, ref *Reference) ([]byte, error) {
	vendorPath := filepath.Join(m.VendorDir(), itemType.DirName(), ref.LocalRemoteName(), ref.Path+".yaml")
	return afero.ReadFile(m.fs, vendorPath)
}

// HasVendored checks if an item exists in the vendor directory.
func (m *VendorManager) HasVendored(itemType ItemType, ref *Reference) bool {
	vendorPath := filepath.Join(m.VendorDir(), itemType.DirName(), ref.LocalRemoteName(), ref.Path+".yaml")
	_, err := m.fs.Stat(vendorPath)
	return err == nil
}
