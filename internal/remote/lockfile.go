package remote

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

const lockfileName = paths.LockFileName + ".yaml"

// LockfileManager handles reading and writing the active lockfile (lock.yaml —
// what BundleReader reads against). It is pure dependency pinning; there is no
// pending-review split (trust-simplify slice 3) — exposure of pulled content is
// gated per item by the content-hash trust gate.
type LockfileManager struct {
	baseDir  string
	fs       afero.Fs
	filename string
}

// LockfileOption is a functional option for configuring a LockfileManager.
type LockfileOption func(*LockfileManager)

// WithLockfileFS sets a custom filesystem implementation (for testing).
func WithLockfileFS(fs afero.Fs) LockfileOption {
	return func(m *LockfileManager) {
		m.fs = fs
	}
}

// NewLockfileManager creates a new lockfile manager for the active lockfile.
// If baseDir is empty, uses the current directory's .ctxloom folder.
func NewLockfileManager(baseDir string, opts ...LockfileOption) *LockfileManager {
	if baseDir == "" {
		baseDir = paths.AppDirName
	}
	m := &LockfileManager{
		baseDir:  baseDir,
		fs:       afero.NewOsFs(),
		filename: lockfileName,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Path returns the path to the managed (active) lockfile.
func (m *LockfileManager) Path() string {
	return filepath.Join(m.baseDir, m.filename)
}

// Load reads the lockfile from disk.
// Returns an empty lockfile if the file doesn't exist.
func (m *LockfileManager) Load() (*Lockfile, error) {
	path := m.Path()

	data, err := afero.ReadFile(m.fs, path)
	if os.IsNotExist(err) {
		return &Lockfile{
			Version: 1,
			Bundles: make(map[string]LockEntry),
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

	// Self-heal legacy schema-version residue (ctxloom_version: v1). The field no
	// longer exists on LockEntry, so re-marshalling drops it; persist the cleaned
	// form up front rather than waiting for the next sync. Best-effort: a write
	// failure leaves the (still-valid) in-memory lockfile untouched.
	if strings.Contains(string(data), "ctxloom_version") {
		if err := m.write(&lockfile); err != nil {
			clidiag.Warn("ctxloom", "failed to clean legacy lockfile %s: %v", path, err)
		}
	}

	return &lockfile, nil
}

// Save writes the lockfile to disk, stamping LockedAt with the current time.
func (m *LockfileManager) Save(lockfile *Lockfile) error {
	lockfile.LockedAt = time.Now().UTC()
	return m.write(lockfile)
}

// write marshals the lockfile and atomically replaces the on-disk file without
// modifying LockedAt. Shared by Save and the load-time self-heal.
func (m *LockfileManager) write(lockfile *Lockfile) error {
	data, err := yaml.Marshal(lockfile)
	if err != nil {
		return fmt.Errorf("failed to marshal lockfile: %w", err)
	}

	// Ensure directory exists
	if err := m.fs.MkdirAll(m.baseDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// The lockfile is the sole on-disk trust/provenance record; a torn write
	// would corrupt it, so replace atomically.
	if err := iox.WriteFileAtomicFs(m.fs, m.Path(), data, 0644); err != nil {
		return fmt.Errorf("failed to write lockfile: %w", err)
	}

	return nil
}

// AddEntry adds or updates an entry in the lockfile. Only bundles are locked now
// (top-level profile distribution was retired); a non-bundle itemType is a no-op.
func (l *Lockfile) AddEntry(itemType ItemType, ref string, entry LockEntry) {
	if itemType == ItemTypeBundle {
		l.Bundles[ref] = entry
	}
}

// GetEntry retrieves an entry from the lockfile.
func (l *Lockfile) GetEntry(itemType ItemType, ref string) (LockEntry, bool) {
	if itemType != ItemTypeBundle {
		return LockEntry{}, false
	}
	entry, ok := l.Bundles[ref]
	return entry, ok
}

// RemoveEntry removes an entry from the lockfile.
func (l *Lockfile) RemoveEntry(itemType ItemType, ref string) {
	if itemType == ItemTypeBundle {
		delete(l.Bundles, ref)
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

	return results
}

// IsEmpty returns true if the lockfile has no entries.
func (l *Lockfile) IsEmpty() bool {
	return len(l.Bundles) == 0
}

// Count returns the total number of entries.
func (l *Lockfile) Count() int {
	return len(l.Bundles)
}

// GetCanonicalURL builds a canonical URL from a lockfile entry.
// localName may be an exact canonical lockfile key or a short bundle/profile
// name, which is resolved against the canonical keys by full path or last path
// segment. Returns the full canonical URL including content version for
// reproducibility, found=false when no entry matches, or an error when a short
// name matches more than one entry. Pre-canonical ("remote/path") lockfile
// keys are not supported; a fresh pull rewrites the lockfile with canonical
// keys.
//
// Format: <url>@<type>/<path>@<content_version>
//
// If RequestedVersion is set, uses that; otherwise uses SHA.
func (l *Lockfile) GetCanonicalURL(itemType ItemType, localName string) (string, bool, error) {
	if entry, ok := l.GetEntry(itemType, localName); ok {
		ref, err := ParseReference(localName)
		if err != nil {
			return "", false, nil
		}
		return canonicalURLFor(itemType, ref.Path, entry), true, nil
	}

	// Short name: resolve against the canonical bundle keys.
	var entries map[string]LockEntry
	if itemType == ItemTypeBundle {
		entries = l.Bundles
	}
	// Match the exact path first (full or basename). Only when nothing matches do
	// we reinterpret a leading segment as a remote-alias prefix ("remote/path") —
	// otherwise a genuine nested path like "lang/go" would also collide with a
	// sibling "go" entry and be wrongly reported as ambiguous.
	var keys []string
	for key := range entries {
		ref, err := ParseReference(key)
		if err != nil {
			continue
		}
		if ref.Path == localName || path.Base(ref.Path) == localName {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		if _, prefixStripped, hasPrefix := strings.Cut(localName, "/"); hasPrefix {
			for key := range entries {
				ref, err := ParseReference(key)
				if err != nil {
					continue
				}
				if ref.Path == prefixStripped {
					keys = append(keys, key)
				}
			}
		}
	}
	switch len(keys) {
	case 0:
		return "", false, nil
	case 1:
		ref, _ := ParseReference(keys[0])
		return canonicalURLFor(itemType, ref.Path, entries[keys[0]]), true, nil
	}
	sort.Strings(keys)
	return "", false, fmt.Errorf("%s %q is ambiguous in the lockfile; use a canonical ref (candidates: %s)",
		itemType, localName, strings.Join(keys, ", "))
}

// canonicalURLFor renders an entry's full canonical URL, versioned by the
// requested constraint when present, else the locked SHA.
func canonicalURLFor(itemType ItemType, itemPath string, entry LockEntry) string {
	contentVersion := entry.RequestedVersion
	if contentVersion == "" {
		contentVersion = entry.SHA
	}
	return fmt.Sprintf("%s@%s/%s@%s", entry.URL, itemType.DirName(), itemPath, contentVersion)
}

// FindByURL searches for a bundle lockfile entry by repository URL.
// Returns the local name (key), entry, and whether it was found.
func (l *Lockfile) FindByURL(repoURL string, itemType ItemType) (localName string, entry LockEntry, found bool) {
	if itemType != ItemTypeBundle {
		return "", LockEntry{}, false
	}
	for name, e := range l.Bundles {
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

	return results
}
