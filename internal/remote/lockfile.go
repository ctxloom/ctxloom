package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
)

const lockfileName = paths.LockFileName + ".yaml"

// LockfileManager handles reading and writing lockfiles.
type LockfileManager struct {
	baseDir string
	fs      afero.Fs
}

// LockfileOption is a functional option for configuring a LockfileManager.
type LockfileOption func(*LockfileManager)

// WithLockfileFS sets a custom filesystem implementation (for testing).
func WithLockfileFS(fs afero.Fs) LockfileOption {
	return func(m *LockfileManager) {
		m.fs = fs
	}
}

// NewLockfileManager creates a new lockfile manager.
// If baseDir is empty, uses the current directory's .ctxloom folder.
func NewLockfileManager(baseDir string, opts ...LockfileOption) *LockfileManager {
	if baseDir == "" {
		baseDir = paths.AppDirName
	}
	m := &LockfileManager{
		baseDir: baseDir,
		fs:      afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Path returns the path to the lockfile.
// Lockfile is stored at .ctxloom/lock.yaml (root level).
func (m *LockfileManager) Path() string {
	return filepath.Join(m.baseDir, lockfileName)
}

// Load reads the lockfile from disk.
// Returns an empty lockfile if the file doesn't exist.
func (m *LockfileManager) Load() (*Lockfile, error) {
	path := m.Path()

	data, err := afero.ReadFile(m.fs, path)
	if os.IsNotExist(err) {
		return &Lockfile{
			Version:  1,
			Bundles:  make(map[string]LockEntry),
			Profiles: make(map[string]LockEntry),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read lockfile: %w", err)
	}

	var lockfile Lockfile
	if err := yaml.Unmarshal(data, &lockfile); err != nil {
		return nil, fmt.Errorf("failed to parse lockfile: %w", err)
	}

	// Initialize maps if nil
	if lockfile.Bundles == nil {
		lockfile.Bundles = make(map[string]LockEntry)
	}
	if lockfile.Profiles == nil {
		lockfile.Profiles = make(map[string]LockEntry)
	}

	return &lockfile, nil
}

// Save writes the lockfile to disk.
func (m *LockfileManager) Save(lockfile *Lockfile) error {
	lockfile.LockedAt = time.Now().UTC()

	data, err := yaml.Marshal(lockfile)
	if err != nil {
		return fmt.Errorf("failed to marshal lockfile: %w", err)
	}

	// Ensure directory exists
	if err := m.fs.MkdirAll(m.baseDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	path := m.Path()
	if err := afero.WriteFile(m.fs, path, data, 0644); err != nil {
		return fmt.Errorf("failed to write lockfile: %w", err)
	}

	return nil
}

// AddEntry adds or updates an entry in the lockfile.
func (l *Lockfile) AddEntry(itemType ItemType, ref string, entry LockEntry) {
	switch itemType {
	case ItemTypeBundle:
		l.Bundles[ref] = entry
	case ItemTypeProfile:
		l.Profiles[ref] = entry
	}
}

// GetEntry retrieves an entry from the lockfile.
func (l *Lockfile) GetEntry(itemType ItemType, ref string) (LockEntry, bool) {
	var entries map[string]LockEntry
	switch itemType {
	case ItemTypeBundle:
		entries = l.Bundles
	case ItemTypeProfile:
		entries = l.Profiles
	}

	entry, ok := entries[ref]
	return entry, ok
}

// RemoveEntry removes an entry from the lockfile.
func (l *Lockfile) RemoveEntry(itemType ItemType, ref string) {
	switch itemType {
	case ItemTypeBundle:
		delete(l.Bundles, ref)
	case ItemTypeProfile:
		delete(l.Profiles, ref)
	}
}

// AllEntries returns all entries in the lockfile with their types.
func (l *Lockfile) AllEntries() []struct {
	Type  ItemType
	Ref   string
	Entry LockEntry
} {
	var results []struct {
		Type  ItemType
		Ref   string
		Entry LockEntry
	}

	for ref, entry := range l.Bundles {
		results = append(results, struct {
			Type  ItemType
			Ref   string
			Entry LockEntry
		}{ItemTypeBundle, ref, entry})
	}
	for ref, entry := range l.Profiles {
		results = append(results, struct {
			Type  ItemType
			Ref   string
			Entry LockEntry
		}{ItemTypeProfile, ref, entry})
	}

	return results
}

// IsEmpty returns true if the lockfile has no entries.
func (l *Lockfile) IsEmpty() bool {
	return len(l.Bundles) == 0 && len(l.Profiles) == 0
}

// Count returns the total number of entries.
func (l *Lockfile) Count() int {
	return len(l.Bundles) + len(l.Profiles)
}

// GetCanonicalURL builds a canonical URL from a lockfile entry.
// The localName should be in format "remote/path" (e.g., "ctxloom-github/core-practices").
// Returns the full canonical URL including content version for reproducibility.
//
// Format: <url>@<ctxloom_version>/<type>/<path>@<content_version>
//
// If RequestedVersion is set, uses that; otherwise uses SHA.
func (l *Lockfile) GetCanonicalURL(itemType ItemType, localName string) (string, bool) {
	entry, ok := l.GetEntry(itemType, localName)
	if !ok {
		return "", false
	}

	// Extract path from local name (everything after first /)
	slashIdx := strings.Index(localName, "/")
	if slashIdx == -1 {
		return "", false
	}
	itemPath := localName[slashIdx+1:]

	// Use requested_version if available, else SHA for reproducibility
	contentVersion := entry.RequestedVersion
	if contentVersion == "" {
		contentVersion = entry.SHA
	}

	typeName := itemType.DirName()
	return fmt.Sprintf("%s@%s/%s/%s@%s", entry.URL, entry.CtxloomVersion, typeName, itemPath, contentVersion), true
}

// FindByURL searches for a lockfile entry by repository URL.
// Returns the local name (key), entry, and whether it was found.
// Searches both bundles and profiles.
func (l *Lockfile) FindByURL(repoURL string, itemType ItemType) (localName string, entry LockEntry, found bool) {
	var entries map[string]LockEntry
	switch itemType {
	case ItemTypeBundle:
		entries = l.Bundles
	case ItemTypeProfile:
		entries = l.Profiles
	default:
		return "", LockEntry{}, false
	}

	for name, e := range entries {
		if e.URL == repoURL {
			return name, e, true
		}
	}
	return "", LockEntry{}, false
}

// FindAllByURL searches for all lockfile entries matching a repository URL.
// Returns all matching entries with their types and local names.
func (l *Lockfile) FindAllByURL(repoURL string) []struct {
	Type      ItemType
	LocalName string
	Entry     LockEntry
} {
	var results []struct {
		Type      ItemType
		LocalName string
		Entry     LockEntry
	}

	for name, entry := range l.Bundles {
		if entry.URL == repoURL {
			results = append(results, struct {
				Type      ItemType
				LocalName string
				Entry     LockEntry
			}{ItemTypeBundle, name, entry})
		}
	}
	for name, entry := range l.Profiles {
		if entry.URL == repoURL {
			results = append(results, struct {
				Type      ItemType
				LocalName string
				Entry     LockEntry
			}{ItemTypeProfile, name, entry})
		}
	}

	return results
}
