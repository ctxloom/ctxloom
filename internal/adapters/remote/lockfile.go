package remote

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	lockfilemig "github.com/ctxloom/ctxloom/internal/migrations/lockfile"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// LockfileManager handles reading and writing the active lockfile (lock.yaml —
// what BundleReader reads against). It is pure dependency pinning.
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

// NewLockfileManager creates a new lockfile manager for the active lockfile.
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

// BaseDir returns the .ctxloom directory this manager was bound to.
//
// It exists so a caller that must write BESIDE the lockfile — Puller's
// directory-form bundle install, which lands a fetched tree under
// <baseDir>/cache/bundles — resolves its root from the same value the lockfile
// did rather than carrying a second, separately-configured copy of it. Two
// configuration axes for one directory is how a pin and the content it pins end
// up in different trees.
func (m *LockfileManager) BaseDir() string {
	return m.baseDir
}

// FS returns the filesystem this manager reads and writes through, for the same
// reason BaseDir exists: a caller writing alongside the lockfile must use the
// same filesystem, or a test that swapped in a memory fs would find its pins
// there and its content on the real disk.
func (m *LockfileManager) FS() afero.Fs {
	return m.fs
}

// Path returns the path to the managed (active) lockfile.
//
// The layout comes from paths.LockPath, the package that exists precisely to
// own it. Assembling the name here from a private const would leave that
// helper production-dead and free to drift. One construction, one place.
func (m *LockfileManager) Path() string {
	return paths.LockPath(m.baseDir)
}

// lockfileKind versions lock.yaml.
var lockfileKind = schemaver.Define("lockfile", LockfileVersion, lockfilemig.Steps()...)

// Load reads the lockfile from disk.
// Returns an empty lockfile if the file doesn't exist.
//
// Reading never writes: an older spelling is migrated in memory and reaches
// disk on the next Save, or at once under --write-upgrades.
func (m *LockfileManager) Load() (*Lockfile, error) {
	path := m.Path()

	data, err := afero.ReadFile(m.fs, path)
	if os.IsNotExist(err) {
		return &Lockfile{
			Version: LockfileVersion,
			Bundles: make(map[ident.BundleKey]LockEntry),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read lockfile: %w", err)
	}

	r, err := upgradeLockfile(path, data)
	if err != nil {
		return nil, err
	}

	var lockfile Lockfile
	if err := yamlx.DecodeStrict(r.Data, &lockfile); err != nil {
		return nil, fmt.Errorf("failed to parse lockfile %s: %w — fix it by hand, or delete it and re-run `ctxloom deps pull` "+
			"to rebuild it (then re-apply any hold with `ctxloom deps hold`)", path, err)
	}
	if err := checkIdentityKeys(path, lockfile.Bundles); err != nil {
		return nil, err
	}

	// Initialize maps if nil
	if lockfile.Bundles == nil {
		lockfile.Bundles = make(map[ident.BundleKey]LockEntry)
	}

	if len(r.Applied) > 0 && schemaver.WriteUpgrades() {
		if err := schemaver.WriteBack(m.fs, path, r, schemaver.NoBackup); err != nil {
			return nil, err
		}
	}

	return &lockfile, nil
}

// upgradeLockfile brings data to lockfileKind.Current() in memory, or refuses
// it: present but empty, or a generation this binary does not read.
func upgradeLockfile(path string, data []byte) (schemaver.Result, error) {
	// A PRESENT lockfile with no document is a DIFFERENT fact from "no
	// lockfile" (Load's not-exist case) and must not collapse into it: every
	// real write stamps its generation, so a documentless file
	// can only be truncation, a crash mid-write, or a hand-created stub, and
	// loading it as a valid empty lockfile would make every pinned remote
	// bundle vanish with no diagnostic. It is checked BEFORE the generation
	// gate, which would read it as generation 0.
	if isDocumentless(data) {
		return schemaver.Result{}, fmt.Errorf("%w: %s — this is not the same as no lockfile at all (which is fine); "+
			"delete it and re-run `ctxloom deps pull` to rebuild it", errLockfileEmpty, path)
	}
	r, err := lockfileKind.Upgrade(data)
	if err != nil {
		return schemaver.Result{}, fmt.Errorf("lockfile %s: %w", path, err)
	}
	return r, nil
}

// errLockfileEmpty reports a lock.yaml that is present but holds no document.
// Every lockfile ctxloom writes records its format generation and its entries,
// so a documentless one was truncated or created by hand; a project with
// nothing pinned has no lock.yaml at all.
var errLockfileEmpty = errors.New("lockfile holds no document (it is empty, or whitespace and comments only)")

// isDocumentless reports whether data decodes to no value at all: empty,
// whitespace, comments only, or an explicit null.
func isDocumentless(data []byte) bool {
	var v any
	return yaml.Unmarshal(data, &v) == nil && v == nil
}

// ErrLockKeyNotIdentity reports a lockfile entry keyed by something other
// than its own bundle identity: every pin and hold is looked up by identity,
// so such an entry is one no lookup reaches.
var ErrLockKeyNotIdentity = errors.New("lockfile entry is not keyed by its bundle identity")

// checkIdentityKeys refuses the first (sorted) key that is not its own bundle
// identity.
func checkIdentityKeys(path string, bundles map[ident.BundleKey]LockEntry) error {
	keys := make([]string, 0, len(bundles))
	for key := range bundles {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !IsBundleIdentity(key) {
			return fmt.Errorf("%w: %s: %q; delete it and re-run `ctxloom deps pull` to rebuild it", ErrLockKeyNotIdentity, path, key)
		}
	}
	return nil
}

// IsBundleIdentity reports whether key is exactly the bundle identity it
// parses to — the one test, shared by every store keyed by bundle identity so
// they cannot disagree about what a key is.
func IsBundleIdentity(key string) bool {
	br, err := ident.ParseBundleRef(key)
	return err == nil && string(br.BundleIdentity()) == key
}

// ErrLockfileWouldErase reports a refused write: the incoming lockfile is
// empty and the one already on disk is not. Callers that mean it say so with
// AllowEmpty.
var ErrLockfileWouldErase = errors.New("refusing to erase lockfile entries")

// ErrLockfileUnreadable reports a refused write over a lockfile whose current
// contents cannot be parsed. There is deliberately no override: the pins and
// holds in an unparseable file cannot be read, so nothing can carry them
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

// Save writes the lockfile to disk, stamping the current format generation.
//
// Save refuses two destructive writes, because the lockfile is the sole
// on-disk record of every dependency pin and every user hold (Held) — losing
// it silently un-holds and re-resolves every pin:
//
//   - An EMPTY lockfile over a populated one. A caller that arrives here with
//     no entries has, by construction, nothing to say about the entries
//     already recorded; "I computed nothing" and "erase everything" are not
//     the same statement. This is the class of bug where `ctxloom remote
//     upgrade`, handed a fallback config with no profile definitions,
//     resolved an empty closure, wrote it wholesale, and reported "Everything
//     is up to date." Callers that genuinely mean to empty the lock pass
//     AllowEmpty.
//   - ANY write over an UNREADABLE lockfile. Every rebuild carries
//     Held forward by reading the previous file; when that
//     read fails the rebuild silently drops them. Refusing the write keeps the
//     evidence on disk and puts the fix in the user's hands.
//
// A genuinely empty project stays a legitimate success: an empty write is
// allowed whenever the file is absent, blank, or already empty.
func (m *LockfileManager) Save(lockfile *Lockfile, opts ...SaveOption) error {
	if lockfile == nil {
		// Before the guard, before the disk read, before the version stamp:
		// each of those dereferences the argument, and a panic partway through
		// a write to the sole on-disk pin/hold record leaves the
		// caller nothing to report. An empty lockfile is a legitimate value
		// with its own rules below; a nil one is a caller that has nothing to
		// say at all.
		return errors.New("refusing to save a nil lockfile")
	}

	var o saveOptions
	for _, opt := range opts {
		opt(&o)
	}
	if err := m.guardDestructiveWrite(lockfile, o); err != nil {
		return err
	}
	lockfile.Version = LockfileVersion
	return m.write(lockfile)
}

// CheckSave reports the refusal Save would give for lockfile, writing
// nothing: a caller that previews a write refuses exactly what applying it
// would.
func (m *LockfileManager) CheckSave(lockfile *Lockfile, opts ...SaveOption) error {
	var o saveOptions
	for _, opt := range opts {
		opt(&o)
	}
	return m.guardDestructiveWrite(lockfile, o)
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
		// Present but unreadable: it may hold pins and holds we cannot
		// see, so it is not ours to replace.
		return fmt.Errorf("%w: %s: %v (fix its permissions, or delete it to start a fresh lock)",
			ErrLockfileUnreadable, path, err)
	}

	// A newer lockfile is not this binary's to replace: the write would
	// downgrade it, dropping whatever the newer format records.
	if _, verr := lockfileKind.Upgrade(data); errors.Is(verr, schemaver.ErrNewer) {
		return fmt.Errorf("lockfile %s: %w", path, verr)
	}

	var current Lockfile
	if uerr := yaml.Unmarshal(data, &current); uerr != nil {
		return fmt.Errorf("%w: %s: %v (fix the file, or delete it to start a fresh lock — every pin and hold it records will be lost)",
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

// write marshals the lockfile and atomically replaces the on-disk file.
func (m *LockfileManager) write(lockfile *Lockfile) error {
	data, err := yamlx.Marshal(lockfile)
	if err != nil {
		return fmt.Errorf("failed to marshal lockfile: %w", err)
	}

	// Ensure directory exists
	if err := m.fs.MkdirAll(m.baseDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// The lockfile is the sole on-disk record of what is pinned; a torn write
	// would corrupt it, so replace atomically.
	if err := safefs.WriteFile(m.fs, m.Path(), data, 0644); err != nil {
		return fmt.Errorf("failed to write lockfile: %w", err)
	}

	return nil
}

// AddEntry adds or updates an entry in the lockfile. Only bundles are locked now
// (top-level profile distribution was retired); a non-bundle itemType is a no-op.
func (l *Lockfile) AddEntry(itemType ItemType, ref ident.BundleKey, entry LockEntry) {
	if itemType == ItemTypeBundle {
		l.Bundles[ref] = entry
	}
}

// GetEntry retrieves an entry from the lockfile.
func (l *Lockfile) GetEntry(itemType ItemType, ref ident.BundleKey) (LockEntry, bool) {
	if itemType != ItemTypeBundle {
		return LockEntry{}, false
	}
	entry, ok := l.Bundles[ref]
	return entry, ok
}

// RemoveEntry removes an entry from the lockfile.
func (l *Lockfile) RemoveEntry(itemType ItemType, ref ident.BundleKey) {
	if itemType == ItemTypeBundle {
		delete(l.Bundles, ref)
	}
}

// LockedEntry is one lockfile record together with the identity it is stored
// under: the item Type, the Ref that keys it, and the LockEntry itself. It
// exists so AllEntries' element has a name -- callers rebuild the lockfile
// from these (carrying Held forward), and a result that can
// only be described by re-spelling its own shape cannot be stored in a
// variable, passed to a helper or ranged over by anything but its producer.
type LockedEntry struct {
	Type  ItemType
	Ref   ident.BundleKey
	Entry LockEntry
}

// AllEntries returns all entries in the lockfile with their types.
func (l *Lockfile) AllEntries() []LockedEntry {
	var results []LockedEntry
	for ref, entry := range l.Bundles {
		results = append(results, LockedEntry{Type: ItemTypeBundle, Ref: ref, Entry: entry})
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
