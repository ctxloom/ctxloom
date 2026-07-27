package remote

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// ErrLockfileWouldErase reports a refused write: the incoming lockfile is
// empty and the one already on disk is not. Callers that mean it say so with
// AllowEmpty.
var ErrLockfileWouldErase = errors.New("refusing to erase lockfile entries")

// ErrLockfileUnreadable reports a refused write over a lockfile whose current
// contents cannot be parsed. There is deliberately no override: the holds and
// retractions in an unparseable file cannot be read, so nothing can carry them
// forward and every write over it destroys state nobody can account for. Fix
// or delete the file instead.
var ErrLockfileUnreadable = errors.New("refusing to overwrite an unreadable lockfile")

type saveOptions struct{ allowEmpty bool }

// SaveOption tunes a lockfile write.
type SaveOption func(*saveOptions)

// AllowEmpty declares that emptying the lockfile is the caller's INTENT, not
// an accident of an incomplete computation — the caller removed entries one by
// one and the last one happened to go (operations.RemoveLocalItems). It
// relaxes only the empty-over-populated refusal; an unreadable lockfile is
// still never overwritten.
func AllowEmpty() SaveOption {
	return func(o *saveOptions) { o.allowEmpty = true }
}

// Save writes the lockfile to disk, stamping LockedAt with the current time.
//
// Save refuses two destructive writes, because the lockfile is the sole
// on-disk record of every dependency pin, every user hold (Pinned) and every
// publisher retraction (Retracted) — losing it silently un-holds and, worse,
// UN-RETRACTS content the publisher withdrew:
//
//   - An EMPTY lockfile over a populated one. A caller that arrives here with
//     no entries has, by construction, nothing to say about the entries
//     already recorded; "I computed nothing" and "erase everything" are not
//     the same statement. This is the class of bug where `ctxloom remote
//     upgrade`, handed a fallback config with no profile definitions,
//     resolved an empty closure, wrote it wholesale, and reported "Everything
//     is up to date." Callers that genuinely mean to empty the lock pass
//     AllowEmpty.
//   - ANY write over an UNREADABLE lockfile (U085-F01). Every rebuild carries
//     Pinned and Retracted forward by reading the previous file; when that
//     read fails the rebuild silently drops them. Refusing the write keeps the
//     evidence on disk and puts the fix in the user's hands.
//
// A genuinely empty project stays a legitimate success: an empty write is
// allowed whenever the file is absent, blank, or already empty.
func (m *LockfileManager) Save(lockfile *Lockfile, opts ...SaveOption) error {
	var o saveOptions
	for _, opt := range opts {
		opt(&o)
	}
	if err := m.guardDestructiveWrite(lockfile, o); err != nil {
		return err
	}
	lockfile.LockedAt = time.Now().UTC()
	return m.write(lockfile)
}

// guardDestructiveWrite reads back what is currently on disk and reports the
// refusals documented on Save. Nothing on disk means nothing to protect.
func (m *LockfileManager) guardDestructiveWrite(incoming *Lockfile, o saveOptions) error {
	path := m.Path()

	data, err := afero.ReadFile(m.fs, path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		// Present but unreadable: it may hold holds and retractions we cannot
		// see, so it is not ours to replace.
		return fmt.Errorf("%w: %s: %v (fix its permissions, or delete it to start a fresh lock)",
			ErrLockfileUnreadable, path, err)
	}

	var current Lockfile
	if uerr := yaml.Unmarshal(data, &current); uerr != nil {
		return fmt.Errorf("%w: %s: %v (fix the file, or delete it to start a fresh lock — every hold and retraction it records will be lost)",
			ErrLockfileUnreadable, path, uerr)
	}

	if current.IsEmpty() || !incoming.IsEmpty() || o.allowEmpty {
		return nil
	}
	return fmt.Errorf("%w: the write would replace %d locked entry(ies) in %s with none; "+
		"this is a bug in the caller unless you meant to unlock everything "+
		"(run `ctxloom remote lock` from the project root, or check that your config loaded)",
		ErrLockfileWouldErase, current.Count(), path)
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
// canonicalURLFor renders an entry's full canonical URL, versioned by the
// requested constraint when present, else the locked SHA.
// FindByURL searches for a bundle lockfile entry by repository URL.
// Returns the local name (key), entry, and whether it was found.
// FindAllByURL searches for all lockfile entries matching a repository URL.
// Returns all matching entries with their types and local names.
