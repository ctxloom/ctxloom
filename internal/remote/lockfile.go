package remote

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// LockfileManager handles reading and writing the active lockfile (lock.yaml —
// what BundleReader reads against). It is pure dependency pinning; there is no
// pending-review split (trust-simplify slice 3) — exposure of pulled content is
// gated per item by the content-hash trust gate.
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

	// A PRESENT-but-empty (or whitespace-only) file is a DIFFERENT
	// fact from "no lockfile" (handled above) and must not collapse into it.
	// Save/write always stamp LockedAt to time.Now() before marshaling, so
	// any real write produces non-trivial bytes ("version: N\nlocked_at:
	// ...\n" at minimum) — a genuinely 0-byte or blank file on disk can only
	// be truncation, a crash mid-write, or a hand-created stub. Loading it as
	// a valid empty lockfile makes every pinned remote bundle vanish from the
	// session with no diagnostic at all. (A comment-only or `null` document
	// that yaml still parses to a non-empty byte count is NOT covered here —
	// unlike bundles.ParseBundle's analogous floor, this package has no
	// invariant guaranteeing Version is always non-zero on a legitimate
	// lockfile, so that stronger check would false-positive on a
	// test/programmatically-constructed Lockfile{} that was never round-
	// tripped through Load.)
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("lockfile %s exists but is empty — this is not the same as no lockfile at all (which is fine); "+
			"fix or delete the file, then re-sync (a legitimate lockfile always contains at least 'version: 1')", path)
	}

	// REFUSE the retired hold key rather than letting yaml drop it. A lockfile
	// written by an older ctxloom spells a hold `pinned`, which this struct no
	// longer models — so it would load cleanly with every hold silently gone,
	// and the next `deps upgrade` would advance a dependency the user
	// deliberately froze while reporting success. Unlike the schema-version
	// residue below, this key carries a DECISION, so it is not something a
	// read may quietly rewrite on the user's behalf.
	if entry, found := findRetiredHoldField(data); found {
		return nil, fmt.Errorf("lockfile %s spells a hold %q on entry %q; it is now %q — "+
			"delete the lockfile and re-run `ctxloom deps pull` to rebuild it, "+
			"then re-apply the hold with `ctxloom deps hold %s`",
			path, retiredHoldField, entry, "held", entry)
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
	//
	// The trigger is the KEY, not the characters. A read that writes has to be
	// sure it has something to clean: re-marshalling replaces the file with the
	// struct's view of it, discarding comments and any key this version does not
	// model, and a repository URL, a bundle path or a retraction reason may
	// mention "ctxloom_version" without one existing anywhere.
	if hasLegacySchemaField(data) {
		if err := m.write(&lockfile); err != nil {
			clidiag.Warn("ctxloom", "failed to clean legacy lockfile %s: %v", path, err)
		}
	}

	return &lockfile, nil
}

// legacySchemaField is the retired per-entry schema-version key. Git tag/SHA is
// the sole content version now; an entry still carrying it is pre-removal
// residue.
const legacySchemaField = "ctxloom_version"

// retiredHoldField is the pre-rename spelling of LockEntry.Held. A hold is a
// user DECISION, so a file still using this key is refused by name rather than
// read with the decision dropped.
const retiredHoldField = "pinned"

// findRetiredHoldField returns the first bundle entry carrying the retired hold
// key as a FIELD, and whether one was found. Parsing is what separates a key
// from a URL, a bundle path or a retraction reason that merely contains the
// word — the same distinction hasLegacySchemaField draws, for the same reason.
// Unparseable input reports false and leaves the loader's own yaml.Unmarshal to
// produce the error.
func findRetiredHoldField(data []byte) (string, bool) {
	var doc struct {
		Bundles map[string]map[string]any `yaml:"bundles"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", false
	}
	// Sorted so a lockfile with several stale entries names the same one every
	// run; map order would make the message differ between identical inputs.
	names := make([]string, 0, len(doc.Bundles))
	for name := range doc.Bundles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := doc.Bundles[name][retiredHoldField]; ok {
			return name, true
		}
	}
	return "", false
}

// hasLegacySchemaField reports whether any bundle entry in data carries the
// retired key as a FIELD. Parsing the document is what separates a key from a
// URL, a path or a sentence that happens to contain the same characters.
// Unparseable input reports false: the loader's own yaml.Unmarshal will surface
// that as an error, and a file nobody can read is not a file to rewrite.
func hasLegacySchemaField(data []byte) bool {
	var doc struct {
		Bundles map[string]map[string]any `yaml:"bundles"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false
	}
	for _, entry := range doc.Bundles {
		if _, ok := entry[legacySchemaField]; ok {
			return true
		}
	}
	return false
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
//   - ANY write over an UNREADABLE lockfile. Every rebuild carries
//     Pinned and Retracted forward by reading the previous file; when that
//     read fails the rebuild silently drops them. Refusing the write keeps the
//     evidence on disk and puts the fix in the user's hands.
//
// A genuinely empty project stays a legitimate success: an empty write is
// allowed whenever the file is absent, blank, or already empty.
func (m *LockfileManager) Save(lockfile *Lockfile, opts ...SaveOption) error {
	if lockfile == nil {
		// Before the guard, before the disk read, before the LockedAt stamp:
		// each of those dereferences the argument, and a panic partway through
		// a write to the sole on-disk pin/hold/retraction record leaves the
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

// LockedEntry is one lockfile record together with the identity it is stored
// under: the item Type, the Ref that keys it, and the LockEntry itself. It
// exists so AllEntries' element has a name -- callers rebuild the lockfile
// from these (carrying Pinned and Retracted forward), and a result that can
// only be described by re-spelling its own shape cannot be stored in a
// variable, passed to a helper or ranged over by anything but its producer.
type LockedEntry struct {
	Type  ItemType
	Ref   string
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
