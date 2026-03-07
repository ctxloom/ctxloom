package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

// Registry manages configured remote sources.
// It persists remotes to .scm/remotes.yaml.
type Registry struct {
	mu         sync.RWMutex
	remotes    map[string]*Remote
	configPath string
	fs         afero.Fs
}

// RegistryOption is a functional option for configuring a Registry.
type RegistryOption func(*Registry)

// WithRegistryFS sets a custom filesystem implementation (for testing).
func WithRegistryFS(fs afero.Fs) RegistryOption {
	return func(r *Registry) {
		r.fs = fs
	}
}

// NewRegistry creates a new registry that persists to the given config path.
// If configPath is empty, defaults to .scm/remotes.yaml in current directory.
func NewRegistry(configPath string, opts ...RegistryOption) (*Registry, error) {
	if configPath == "" {
		configPath = filepath.Join(".scm", "remotes.yaml")
	}

	r := &Registry{
		remotes:    make(map[string]*Remote),
		configPath: configPath,
		fs:         afero.NewOsFs(),
	}

	for _, opt := range opts {
		opt(r)
	}

	// Load existing config if it exists
	if err := r.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return r, nil
}

// configFile represents the structure of the config file.
// Only contains remotes-related fields to avoid overwriting other config.
type configFile struct {
	Remotes map[string]Remote `yaml:"remotes,omitempty"`
	Auth    AuthConfig        `yaml:"auth,omitempty"`
	Replace map[string]string `yaml:"replace,omitempty"`
	Vendor  bool              `yaml:"vendor,omitempty"`
}

// load reads remotes from the config file.
func (r *Registry) load() error {
	data, err := afero.ReadFile(r.fs, r.configPath)
	if err != nil {
		return err
	}

	var cfg configFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	for name, remote := range cfg.Remotes {
		remote := remote // Copy to avoid pointer issues
		remote.Name = name
		r.remotes[name] = &remote
	}

	return nil
}

// save writes remotes to the config file, preserving other fields.
func (r *Registry) save() error {
	// Read existing config to preserve other fields
	var existingRaw map[string]interface{}
	data, err := afero.ReadFile(r.fs, r.configPath)
	if err == nil {
		_ = yaml.Unmarshal(data, &existingRaw)
	}
	if existingRaw == nil {
		existingRaw = make(map[string]interface{})
	}

	// Update remotes
	remotesMap := make(map[string]Remote)
	for name, remote := range r.remotes {
		remotesMap[name] = Remote{
			URL:     remote.URL,
			Version: remote.Version,
		}
	}
	if len(remotesMap) > 0 {
		existingRaw["remotes"] = remotesMap
	} else {
		delete(existingRaw, "remotes")
	}

	// Ensure directory exists
	if err := r.fs.MkdirAll(filepath.Dir(r.configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal and write
	out, err := yaml.Marshal(existingRaw)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := afero.WriteFile(r.fs, r.configPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// Add registers a new remote (explicit user command).
// Returns error if:
//   - A remote with the same name already exists
//   - A remote already points to the same URL (use that one instead)
func (r *Registry) Add(name, repoURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.remotes[name]; exists {
		return fmt.Errorf("remote already exists: %s", name)
	}

	// Normalize the URL
	normalizedURL := NormalizeURL(repoURL)

	// Check if any existing remote points to this URL
	if existingName, found := r.findByURLLocked(normalizedURL); found {
		return fmt.Errorf("remote '%s' already points to this URL; use 'scm remote pull %s/<path>' instead", existingName, existingName)
	}

	remote := &Remote{
		Name:    name,
		URL:     normalizedURL,
		Version: "v1", // Default version directory
	}

	r.remotes[name] = remote

	if err := r.save(); err != nil {
		delete(r.remotes, name) // Rollback
		return err
	}

	return nil
}

// AddWithVersion registers a new remote with a specific SCM version.
// Returns error if a remote with the same name or URL already exists.
func (r *Registry) AddWithVersion(name, repoURL, scmVersion string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.remotes[name]; exists {
		return fmt.Errorf("remote already exists: %s", name)
	}

	normalizedURL := NormalizeURL(repoURL)

	// Check if any existing remote points to this URL
	if existingName, found := r.findByURLLocked(normalizedURL); found {
		return fmt.Errorf("remote '%s' already points to this URL; use 'scm remote pull %s/<path>' instead", existingName, existingName)
	}

	remote := &Remote{
		Name:    name,
		URL:     normalizedURL,
		Version: scmVersion,
	}

	r.remotes[name] = remote

	if err := r.save(); err != nil {
		delete(r.remotes, name) // Rollback
		return err
	}

	return nil
}

// GetOrCreateByURL finds an existing remote by URL or creates a new one.
// Used for auto-registration during pull - returns existing remote if URL already registered.
// New remotes are named using the repository name extracted from the URL.
// If a name conflict exists (same repo name, different URL), appends a numeric suffix.
func (r *Registry) GetOrCreateByURL(repoURL, scmVersion string) (*Remote, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	normalizedURL := NormalizeURL(repoURL)

	// Check if any existing remote points to this URL
	for _, remote := range r.remotes {
		if remote.URL == normalizedURL {
			// Return existing remote
			remoteCopy := *remote
			return &remoteCopy, nil
		}
	}

	// Auto-register using repo name
	baseName := ExtractRepoName(repoURL)
	name := baseName

	// Handle name conflicts with numeric suffix
	suffix := 2
	for {
		if _, exists := r.remotes[name]; !exists {
			break
		}
		name = fmt.Sprintf("%s-%d", baseName, suffix)
		suffix++
	}

	remote := &Remote{
		Name:    name,
		URL:     normalizedURL,
		Version: scmVersion,
	}

	r.remotes[name] = remote

	if err := r.save(); err != nil {
		delete(r.remotes, name) // Rollback
		return nil, err
	}

	remoteCopy := *remote
	return &remoteCopy, nil
}

// FindByURL searches for a remote by repository URL.
// Returns the remote name and whether it was found.
func (r *Registry) FindByURL(repoURL string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	normalizedURL := NormalizeURL(repoURL)
	return r.findByURLLocked(normalizedURL)
}

// findByURLLocked searches for a remote by URL (must hold lock).
func (r *Registry) findByURLLocked(normalizedURL string) (string, bool) {
	for name, remote := range r.remotes {
		if remote.URL == normalizedURL {
			return name, true
		}
	}
	return "", false
}

// Remove deletes a remote by name.
// Returns error if the remote doesn't exist.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.remotes[name]; !exists {
		return fmt.Errorf("remote not found: %s", name)
	}

	delete(r.remotes, name)

	return r.save()
}

// Get retrieves a remote by name.
// Returns nil and error if not found.
func (r *Registry) Get(name string) (*Remote, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	remote, ok := r.remotes[name]
	if !ok {
		return nil, fmt.Errorf("remote not found: %s", name)
	}

	// Return a copy to prevent mutation
	remoteCopy := *remote
	return &remoteCopy, nil
}

// List returns all configured remotes, sorted by name.
func (r *Registry) List() []*Remote {
	r.mu.RLock()
	defer r.mu.RUnlock()

	remotes := make([]*Remote, 0, len(r.remotes))
	for _, remote := range r.remotes {
		remoteCopy := *remote
		remotes = append(remotes, &remoteCopy)
	}

	sort.Slice(remotes, func(i, j int) bool {
		return remotes[i].Name < remotes[j].Name
	})

	return remotes
}

// Has checks if a remote exists.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.remotes[name]
	return ok
}

// SetVersion updates the version directory for a remote.
func (r *Registry) SetVersion(name, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	remote, ok := r.remotes[name]
	if !ok {
		return fmt.Errorf("remote not found: %s", name)
	}

	remote.Version = version

	return r.save()
}

// GetFetcher creates a Fetcher for the specified remote.
func (r *Registry) GetFetcher(name string, auth AuthConfig) (Fetcher, error) {
	remote, err := r.Get(name)
	if err != nil {
		return nil, err
	}

	return NewFetcher(remote.URL, auth)
}
