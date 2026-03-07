package remote

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
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
		baseDir = ".scm"
	}
	m := &VendorManager{
		baseDir:        baseDir,
		fs:             afero.NewOsFs(),
		fetcherFactory: defaultFetcherFactory,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// VendorDir returns the vendor directory path.
func (m *VendorManager) VendorDir() string {
	return filepath.Join(m.baseDir, "vendor")
}

// IsVendored checks if vendor mode is enabled.
func (m *VendorManager) IsVendored() bool {
	configPath := filepath.Join(".scm", "remotes.yaml")
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
	configPath := filepath.Join(".scm", "remotes.yaml")

	// Ensure directory exists
	if err := m.fs.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
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
		ref, err := ParseReference(e.Ref)
		if err != nil {
			return fmt.Errorf("invalid reference %s: %w", e.Ref, err)
		}

		rem, err := registry.Get(ref.Remote)
		if err != nil {
			return fmt.Errorf("remote not found %s: %w", ref.Remote, err)
		}

		fetcher, err := m.fetcherFactory(rem.URL, auth)
		if err != nil {
			return fmt.Errorf("failed to create fetcher: %w", err)
		}

		owner, repo, err := ParseRepoURL(rem.URL)
		if err != nil {
			return fmt.Errorf("invalid URL: %w", err)
		}

		// Build file path
		filePath := ref.BuildFilePath(e.Type, rem.Version)

		// Fetch content at locked SHA
		content, err := fetcher.FetchFile(ctx, owner, repo, filePath, e.Entry.SHA)
		if err != nil {
			return fmt.Errorf("failed to fetch %s: %w", e.Ref, err)
		}

		// Write to vendor directory
		vendorPath := filepath.Join(vendorDir, string(e.Type)+"s", ref.Remote, ref.Path+".yaml")
		if err := m.fs.MkdirAll(filepath.Dir(vendorPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}

		if err := afero.WriteFile(m.fs, vendorPath, content, 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", vendorPath, err)
		}
	}

	return nil
}

// GetVendored returns content from the vendor directory if available.
func (m *VendorManager) GetVendored(itemType ItemType, ref *Reference) ([]byte, error) {
	vendorPath := filepath.Join(m.VendorDir(), itemType.DirName(), ref.Remote, ref.Path+".yaml")
	return afero.ReadFile(m.fs, vendorPath)
}

// HasVendored checks if an item exists in the vendor directory.
func (m *VendorManager) HasVendored(itemType ItemType, ref *Reference) bool {
	vendorPath := filepath.Join(m.VendorDir(), itemType.DirName(), ref.Remote, ref.Path+".yaml")
	_, err := m.fs.Stat(vendorPath)
	return err == nil
}
