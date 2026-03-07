package remote

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

// ReplaceManager handles replace directives for local development overrides.
type ReplaceManager struct {
	configPath string
	replaces   map[string]string
	fs         afero.Fs
}

// ReplaceOption is a functional option for configuring a ReplaceManager.
type ReplaceOption func(*ReplaceManager)

// WithReplaceFS sets a custom filesystem implementation (for testing).
func WithReplaceFS(fs afero.Fs) ReplaceOption {
	return func(m *ReplaceManager) {
		m.fs = fs
	}
}

// NewReplaceManager creates a new replace manager.
func NewReplaceManager(configPath string, opts ...ReplaceOption) (*ReplaceManager, error) {
	if configPath == "" {
		configPath = filepath.Join(".scm", "remotes.yaml")
	}

	m := &ReplaceManager{
		configPath: configPath,
		replaces:   make(map[string]string),
		fs:         afero.NewOsFs(),
	}

	for _, opt := range opts {
		opt(m)
	}

	if err := m.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return m, nil
}

func (m *ReplaceManager) load() error {
	data, err := afero.ReadFile(m.fs, m.configPath)
	if err != nil {
		return err
	}

	var cfg struct {
		Replace map[string]string `yaml:"replace,omitempty"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return err
	}

	if cfg.Replace != nil {
		m.replaces = cfg.Replace
	}

	return nil
}

func (m *ReplaceManager) save() error {
	// Read existing config
	var existingRaw map[string]interface{}
	data, err := afero.ReadFile(m.fs, m.configPath)
	if err == nil {
		_ = yaml.Unmarshal(data, &existingRaw)
	}
	if existingRaw == nil {
		existingRaw = make(map[string]interface{})
	}

	// Update replace
	if len(m.replaces) > 0 {
		existingRaw["replace"] = m.replaces
	} else {
		delete(existingRaw, "replace")
	}

	// Ensure directory exists
	if err := m.fs.MkdirAll(filepath.Dir(m.configPath), 0755); err != nil {
		return err
	}

	out, err := yaml.Marshal(existingRaw)
	if err != nil {
		return err
	}

	return afero.WriteFile(m.fs, m.configPath, out, 0644)
}

// Add adds a replace directive.
func (m *ReplaceManager) Add(ref, localPath string) error {
	// Validate local path exists
	if _, err := m.fs.Stat(localPath); os.IsNotExist(err) {
		return fmt.Errorf("local path does not exist: %s", localPath)
	}

	m.replaces[ref] = localPath
	return m.save()
}

// Remove removes a replace directive.
func (m *ReplaceManager) Remove(ref string) error {
	if _, ok := m.replaces[ref]; !ok {
		return fmt.Errorf("replace not found: %s", ref)
	}
	delete(m.replaces, ref)
	return m.save()
}

// Get returns the local path for a reference if replaced.
func (m *ReplaceManager) Get(ref string) (string, bool) {
	path, ok := m.replaces[ref]
	return path, ok
}

// List returns all replace directives.
func (m *ReplaceManager) List() map[string]string {
	result := make(map[string]string)
	for k, v := range m.replaces {
		result[k] = v
	}
	return result
}

// IsReplaced checks if a reference has a replace directive.
func (m *ReplaceManager) IsReplaced(ref string) bool {
	_, ok := m.replaces[ref]
	return ok
}

// LoadReplaced loads content from a replaced local file.
func (m *ReplaceManager) LoadReplaced(ref string) ([]byte, error) {
	path, ok := m.replaces[ref]
	if !ok {
		return nil, fmt.Errorf("no replace directive for: %s", ref)
	}

	return afero.ReadFile(m.fs, path)
}
