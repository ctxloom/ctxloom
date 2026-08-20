package admission

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// The trust-on-first-use store: the first time a given thing would be
// admitted, ctxloom asks a human and RECORDS the answer; every later
// admission of the same thing is silent. A session with nobody to ask
// REFUSES. It is the ssh known_hosts pattern, and it is what both the
// companion exec gate and the publish-destination gate independently built.
//
// A record is DATA, not a signature: its authority is the filesystem
// permissions on the user's home directory, exactly like the unsigned approval
// markers of the signature-envelope spec §9.5. That is why the store is
// personal-only and has no committable project twin.
//
// TWO KEYS, ONE RULE. A record lives in a SCOPE (what a human means when they
// say "this one") and has a KEY (the exact thing they approved). An APPROVAL
// requires the exact key, so any change to the approved thing re-asks. A
// DENIAL matches the whole SCOPE, so "I never want this" survives the thing
// being rebuilt — a refusal that only held until one byte changed would never
// have been a refusal. A scope holds at most one live decision. Where a domain
// has nothing finer than its scope (a publish remote is its URL and nothing
// else) key and scope are the same function and the asymmetry collapses to
// nothing.

// storeVersion is the only on-disk version this build understands. A future
// format change must fail LOUD rather than be misread as an empty store —
// "nothing recorded" is exactly the reading that re-opens a door a human
// closed.
const storeVersion = 1

// Record is one recorded human decision.
type Record[K comparable] struct {
	// Key is the thing decided about, in the domain's own spelling. It carries
	// whatever a human needs to read the file with `cat` as well as whatever
	// the key and scope functions project out of it.
	Key K `yaml:"key"`
	// Approved is the decision. A false record is a DENIAL and is honored
	// scope-wide.
	Approved bool `yaml:"approved"`
	// RecordedAt is untrusted display metadata for humans — never an input to
	// any decision, the same standing every timestamp has in this codebase.
	RecordedAt time.Time `yaml:"recorded_at"`
}

// Ask puts the question to a human and reports the answer.
//
// answered distinguishes "the human said no" — a decision, and recorded — from
// "nothing came back" (EOF, a closed stdin, a read error), which is a
// transient and must NOT be persisted: a hiccup that hardened into a permanent
// refusal is one the user has to discover and undo.
//
// A nil Ask is the NON-INTERACTIVE case and is the deliberate default. Domain
// packages have no terminal and must not acquire one; a frontend supplies this
// only when it actually has a human attached, so an agent, an MCP tool call, a
// CI job and a piped invocation all reach the refusal rather than a prompt
// written into a pipe.
type Ask[K comparable] func(ctx context.Context, k K) (approved, answered bool, err error)

// Reasons is the domain's vocabulary for the four outcomes the store's own
// flow can produce. Everything else in a domain's enum belongs to arms the
// store never sees (an exemption, a "not installed", a signature verdict).
//
// All four must be DISTINCT and none may be the zero value. Distinct because
// "nobody could be asked" and "you declined" have different fixes and must
// never be merged; non-zero because the zero Reason is what an unpopulated
// Decision carries, and a store that emitted it would make a real answer
// indistinguishable from a struct literal.
type Reasons[R comparable] struct {
	// Approved: a recorded approval covers this exact key.
	Approved R
	// Declined: a recorded denial covers this key's scope.
	Declined R
	// Unasked: no record, and nobody could be asked. Fail-closed.
	Unasked R
	// Fault: the store exists but could not be read, or was never configured.
	// Denies EVERYTHING, because a store we cannot read may hold a denial.
	Fault R
}

// validate enforces the distinctness and non-zeroness the doc comment claims.
func (r Reasons[R]) validate() error {
	var zero R
	named := []struct {
		field string
		value R
	}{
		{"Approved", r.Approved}, {"Declined", r.Declined},
		{"Unasked", r.Unasked}, {"Fault", r.Fault},
	}
	seen := make(map[R]string, len(named))
	for _, n := range named {
		if n.value == zero {
			return fmt.Errorf("admission: Reasons.%s is the zero reason, which is what an unpopulated Decision carries", n.field)
		}
		if prev, dup := seen[n.value]; dup {
			return fmt.Errorf("admission: Reasons.%s and Reasons.%s are the same reason; every outcome needs its own, "+
				"or a caller cannot tell them apart", prev, n.field)
		}
		seen[n.value] = n.field
	}
	return nil
}

// Snapshot is the store's records as loaded ONCE, plus the two predicates a
// decision keys on. Batch deciders (admit every discovered companion) load one
// snapshot and consult it per candidate, so one pass over N candidates makes
// one read and cannot see the file change underneath it mid-pass.
type Snapshot[K comparable] struct {
	records []Record[K]
	key     func(K) string
	scope   func(K) string
}

// Records returns the loaded records, sorted by scope then key.
func (s *Snapshot[K]) Records() []Record[K] {
	if s == nil {
		return nil
	}
	return append([]Record[K](nil), s.records...)
}

// Declined reports whether a recorded DENIAL covers k's scope. Deliberately
// blind to everything below the scope, which is also what lets a cascade check
// a refusal before it pays to compute the finer half of the key at all.
func (s *Snapshot[K]) Declined(k K) bool {
	if s == nil {
		return false
	}
	want := s.scope(k)
	for _, r := range s.records {
		if !r.Approved && s.scope(r.Key) == want {
			return true
		}
	}
	return false
}

// Approved reports whether a recorded APPROVAL covers this EXACT key. Both
// halves of a compound key are required: that is the whole content of keying
// on more than the scope.
func (s *Snapshot[K]) Approved(k K) bool {
	if s == nil {
		return false
	}
	want := s.key(k)
	for _, r := range s.records {
		if r.Approved && s.key(r.Key) == want {
			return true
		}
	}
	return false
}

// Note adds rec to this in-memory snapshot, WITHOUT writing anything. It is
// for a batch decider that just recorded an answer and must not re-ask for the
// same thing later in the same pass.
func (s *Snapshot[K]) Note(rec Record[K]) {
	if s == nil {
		return
	}
	s.records = append(s.records, rec)
}

// LockPathFor derives the sidecar lock file that guards a store's records
// file. filelock.PathFor (home-rooted, beside the file) and
// filelock.ProjectPathFor (inside a project .ctxloom tree, under
// state/locks/) are the two shapes filelock ships; a domain picks whichever
// matches where ITS store's file actually lives — see WithLockPathFor.
type LockPathFor func(protected string) (string, error)

// Store is one domain's personal record of admission decisions: a single
// YAML file under the user's home.
type Store[K comparable, R comparable] struct {
	fs      afero.Fs
	path    string
	key     func(K) string
	scope   func(K) string
	now     func() time.Time
	reasons Reasons[R]
	// lockPathFor derives the write lock's sidecar path from s.path. See
	// LockPathFor and WithLockPathFor; defaults to filelock.PathFor, the
	// right shape for a home-rooted store like this package's own doc
	// describes (property 6).
	lockPathFor LockPathFor
	// useLock is false only for a store built over a non-OS-backed
	// filesystem (a test double: afero.MemMapFs, a ReadOnlyFs wrapping one,
	// ...). Locking exists to exclude OTHER PROCESSES, which a fake
	// filesystem has none of — and composing a lock path from one of its
	// (often nonexistent, often unwritable-by-this-user) paths and asking
	// the REAL OS to create and flock it would touch actual disk at an
	// address the test never intended, exactly the crosstalk
	// config.Manager's injectedFS guard exists to avoid. See isOSBackedFs.
	useLock bool
	// misconfigured is the construction fault, held rather than panicked so
	// construction stays total. Every method surfaces it; nothing reads or
	// writes through a store that carries one.
	misconfigured error
}

// options carries the optional halves of NewStore.
type options[K comparable] struct {
	scope       func(K) string
	now         func() time.Time
	lockPathFor LockPathFor
}

// Option adjusts a Store at construction.
type Option[K comparable] func(*options[K])

// WithScope sets the coarser identity a DENIAL matches on and a Forget removes
// by. Default: the key function itself, which is right whenever a domain has
// nothing finer than the thing a human named.
func WithScope[K comparable](scope func(K) string) Option[K] {
	return func(o *options[K]) { o.scope = scope }
}

// WithClock replaces the source of Record.RecordedAt, so a test can pin the
// display metadata it never decides on.
func WithClock[K comparable](now func() time.Time) Option[K] {
	return func(o *options[K]) { o.now = now }
}

// WithLockPathFor overrides how the store derives its write lock's sidecar
// path from the records file's own path. The default is filelock.PathFor
// (beside the file), right for a home-rooted store (companion_consent's
// ~/.ctxloom/companion_consent.yaml). A domain whose store instead lives
// inside a PROJECT .ctxloom tree (dirty_tree_ack's
// .ctxloom/state/dirty_tree_commit_ack.yaml) passes filelock.ProjectPathFor
// here — its signature already matches LockPathFor exactly.
func WithLockPathFor[K comparable](lp LockPathFor) Option[K] {
	return func(o *options[K]) { o.lockPathFor = lp }
}

// isOSBackedFs reports whether fs is the real operating-system filesystem, as
// opposed to a test double (afero.MemMapFs, a ReadOnlyFs wrapping one, ...).
// See Store.useLock for why this gates locking.
func isOSBackedFs(fs afero.Fs) bool {
	_, ok := fs.(*afero.OsFs)
	return ok
}

// NewStore builds a store over path, backed by fs, keyed by key.
//
// path need not exist — an absent file is the ordinary "nobody has decided
// anything yet" state, not a fault. An EMPTY path is NOT that state: it is a
// store nobody managed to configure, whose real trigger is an unresolvable
// $HOME. Because filepath.Join("", x) == x such a store would read the process
// working directory, and since a record IS the authority a stray file at a
// repo root would authorise something. An unconfigured store therefore answers
// nothing and refuses every write.
func NewStore[K comparable, R comparable](
	fs afero.Fs, path string, key func(K) string, reasons Reasons[R], opts ...Option[K],
) *Store[K, R] {
	o := options[K]{now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if fs == nil {
		fs = afero.NewOsFs()
	}
	lockPathFor := o.lockPathFor
	if lockPathFor == nil {
		lockPathFor = func(protected string) (string, error) { return filelock.PathFor(protected), nil }
	}
	s := &Store[K, R]{
		fs: fs, path: path, key: key, scope: o.scope, now: o.now, reasons: reasons,
		lockPathFor: lockPathFor, useLock: isOSBackedFs(fs),
	}
	if s.scope == nil {
		s.scope = key
	}
	if key == nil {
		s.misconfigured = errors.New("admission: store built with no key function")
	} else {
		s.misconfigured = reasons.validate()
	}
	return s
}

// Path reports the file this store lives in, for messages that must tell a
// human where the answer was (or would be) recorded. Empty means unconfigured.
func (s *Store[K, R]) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// configured reports the "nobody gave this store a file" fault, and the
// construction faults alongside it. See NewStore for why an empty path is a
// fault and not an empty store.
func (s *Store[K, R]) configured() error {
	if s == nil {
		return errors.New("admission: nil store")
	}
	if s.misconfigured != nil {
		return s.misconfigured
	}
	if s.path == "" {
		return errors.New("admission: no store path configured (an unresolvable home directory), refusing to read or write anything")
	}
	return nil
}

// doc is the on-disk shape.
type doc[K comparable] struct {
	Version int         `yaml:"version"`
	Records []Record[K] `yaml:"records"`
}

// Load reads the store once. An ABSENT file is the ordinary "nobody has
// decided anything yet" state and is not a fault.
//
// Every OTHER read failure is returned rather than folded onto "nothing
// recorded". A store that EXISTS but cannot be read may hold a DENIAL, and
// reading that silence as permission re-opens a door a human closed; on the
// admit side it would ask a human to re-confirm something they already
// approved, teaching them to answer the prompt on reflex, which is the whole
// value of asking.
//
// DELIBERATELY UNLOCKED, including when called from List/Lookup: the file
// this reads is always produced by write's atomic rename (see
// iox.WriteFileAtomicFs), so a concurrent writer can only ever leave a
// reader seeing the whole previous file or the whole new one, never a torn
// mix — the property a SHARED lock exists to buy is already true here for
// free. What a read can still see is a STALE-but-whole file (a writer that
// commits a moment later), which is inherent to any read that is not itself
// inside the same critical section as a write, and no lock changes that.
func (s *Store[K, R]) Load() (*Snapshot[K], error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	data, err := afero.ReadFile(s.fs, s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Snapshot[K]{key: s.key, scope: s.scope}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	var d doc[K]
	if uerr := yaml.Unmarshal(data, &d); uerr != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, uerr)
	}
	if d.Version != storeVersion {
		return nil, fmt.Errorf("%s declares version %d, this build understands %d", s.path, d.Version, storeVersion)
	}
	return &Snapshot[K]{records: d.Records, key: s.key, scope: s.scope}, nil
}

// List returns every recorded decision, sorted by scope then key. It is the
// read half of a `list` CLI leaf.
func (s *Store[K, R]) List() ([]Record[K], error) {
	snap, err := s.Load()
	if err != nil {
		return nil, err
	}
	out := snap.Records()
	s.sortByScopeThenKey(out)
	return out, nil
}

// sortByScopeThenKey sorts recs in place by scope then key — the one
// ordering rule this store imposes on its records, shared by the read side
// (List) and the write side (write, ahead of serializing) so a human reading
// the YAML file with `cat` sees the same order `list` prints, and so the two
// call sites cannot drift into two different orderings of "sorted".
//
// This is the extraction reprise's inconsistent-update finding against
// List/write asked for (the sort comparator used to be pasted verbatim in
// both). Past this call the two diverge completely — List returns the sorted
// slice to a reader, write hands it to a YAML marshaler and an atomic
// filesystem write — so a residual "both call sortByScopeThenKey" shape in a
// future scan is the ordinary look of two callers sharing one helper, not a
// second copy to extract.
func (s *Store[K, R]) sortByScopeThenKey(recs []Record[K]) {
	sort.SliceStable(recs, func(i, j int) bool {
		si, sj := s.scope(recs[i].Key), s.scope(recs[j].Key)
		if si != sj {
			return si < sj
		}
		return s.key(recs[i].Key) < s.key(recs[j].Key)
	})
}

// Lookup reports the live decision for k: a scope-wide denial first, then an
// exact-key approval. found false means nobody has decided about it yet.
func (s *Store[K, R]) Lookup(k K) (Record[K], bool, error) {
	var zero Record[K]
	snap, err := s.Load()
	if err != nil {
		return zero, false, err
	}
	if snap.Declined(k) {
		for _, r := range snap.records {
			if !r.Approved && s.scope(r.Key) == s.scope(k) {
				return r, true, nil
			}
		}
	}
	if snap.Approved(k) {
		for _, r := range snap.records {
			if r.Approved && s.key(r.Key) == s.key(k) {
				return r, true, nil
			}
		}
	}
	return zero, false, nil
}

// Set records an explicit decision for k, replacing every prior record in k's
// SCOPE — a scope holds exactly one live decision, so re-deciding never leaves
// a stale approval behind for an older shape of the same thing.
//
// It is the scriptable form of the interactive prompt: the escape hatch a
// non-interactive CI run or an agent host needs, deliberately requiring a
// human to type it rather than inferring consent from the environment.
//
// The whole read-modify-write runs under the store's write lock (see
// lockedRMW): the load below is the "read" half, taken AFTER the lock so it
// sees any writer that just committed, not a snapshot from before this call
// even started waiting.
func (s *Store[K, R]) Set(k K, approved bool) (Record[K], error) {
	var zero Record[K]
	if err := s.configured(); err != nil {
		return zero, err
	}
	rec := Record[K]{Key: k, Approved: approved, RecordedAt: s.now()}
	if err := s.lockedRMW(func() error {
		snap, err := s.Load()
		if err != nil {
			// Refuse to overwrite a record we could not read: the file may
			// hold decisions this write would silently erase.
			return fmt.Errorf("admission record is unreadable, refusing to overwrite it: %w", err)
		}
		want := s.scope(k)
		kept := make([]Record[K], 0, len(snap.records)+1)
		for _, r := range snap.records {
			if s.scope(r.Key) != want {
				kept = append(kept, r)
			}
		}
		kept = append(kept, rec)
		return s.write(kept)
	}); err != nil {
		return zero, err
	}
	return rec, nil
}

// Forget drops every recorded decision in k's scope and reports how many went.
// Zero removed is reported as ZERO, never as success-with-no-effect: undoing
// something nobody recorded is the caller's mistake to see.
//
// Runs under the same write lock as Set — see lockedRMW.
func (s *Store[K, R]) Forget(k K) (int, error) {
	if err := s.configured(); err != nil {
		return 0, err
	}
	removed := 0
	if err := s.lockedRMW(func() error {
		snap, err := s.Load()
		if err != nil {
			return err
		}
		want := s.scope(k)
		kept := make([]Record[K], 0, len(snap.records))
		for _, r := range snap.records {
			if s.scope(r.Key) != want {
				kept = append(kept, r)
			}
		}
		removed = len(snap.records) - len(kept)
		if removed == 0 {
			return nil
		}
		return s.write(kept)
	}); err != nil {
		return 0, err
	}
	return removed, nil
}

// lockedRMW acquires this store's write lock and runs fn while holding it,
// unless the store is backed by a non-OS test filesystem (see useLock), in
// which case fn runs directly — there is no other process to exclude.
//
// BOTH a lock-path derivation failure and a lock ACQUISITION failure fail
// closed: fn never runs unlocked as a fallback. Degrading to unlocked on
// either would discard the serialization this method exists to provide,
// silently, on every subsequent call — exactly the fix config.Manager.Update
// made for the identical failure shape (see its doc).
func (s *Store[K, R]) lockedRMW(fn func() error) error {
	if !s.useLock {
		return fn()
	}
	lockPath, err := s.lockPathFor(s.path)
	if err != nil {
		return fmt.Errorf("admission: locating write lock for %s: %w", s.path, err)
	}
	unlock, err := filelock.Lock(lockPath)
	if err != nil {
		return fmt.Errorf("admission: acquiring write lock for %s: %w", s.path, err)
	}
	defer unlock()
	return fn()
}

// write serializes recs, 0600 in a 0700 directory, atomically (unique temp
// file, fsynced, renamed into place — see iox.WriteFileAtomicFs) so a reader
// never observes a half-written file. These records decide whether code runs
// and where signed content is pushed: world-readable would leak the
// machine's layout, and world-WRITABLE would hand the decision away.
func (s *Store[K, R]) write(recs []Record[K]) error {
	if err := s.configured(); err != nil {
		return err
	}
	s.sortByScopeThenKey(recs)
	data, err := yaml.Marshal(doc[K]{Version: storeVersion, Records: recs})
	if err != nil {
		return fmt.Errorf("marshal admission records: %w", err)
	}
	if mkErr := s.fs.MkdirAll(filepath.Dir(s.path), 0o700); mkErr != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.path), mkErr)
	}
	// Durable: this records a human's admission decision — the one
	// unrecoverable class this store exists for (see the package doc's
	// ssh known_hosts comparison) — and a rename that silently reverts
	// after a crash would re-open a door a human closed with no signal
	// that it happened.
	if werr := iox.WriteFileAtomicFs(s.fs, s.path, data, 0o600, iox.Durable()); werr != nil {
		return fmt.Errorf("write %s: %w", s.path, werr)
	}
	return nil
}

// Decide is the whole trust-on-first-use flow in one call: look up, ask or
// refuse, record.
//
// The four outcomes and their reasons:
//
//   - the store could not be read, or was never configured → Fault, refused.
//     Above everything, including any exemption a caller would otherwise
//     apply, because an unreadable store may hold a denial.
//   - a recorded decision covers k → Approved or Declined, as recorded.
//   - nothing recorded and ask is nil, or the question was put and nothing came
//     back → Unasked, refused. Never a prompt into a pipe, never an assumed
//     yes, and a transient non-answer is deliberately NOT persisted.
//   - the human answered → that answer, recorded.
//
// A non-nil error alongside an ALLOWING decision is the "you said yes and the
// answer could not be written" case. The decision still holds for this call —
// refusing to honor a yes a human just typed would be its own silent no-op —
// and the caller chooses: say so and continue, or treat an unrecordable
// confirmation as fatal. Both callers in this repo want different things here,
// which is exactly why the store returns both and decides neither.
func (s *Store[K, R]) Decide(ctx context.Context, k K, ask Ask[K]) (Decision[R], error) {
	rec, found, err := s.Lookup(k)
	if err != nil {
		return Decision[R]{Reason: s.reasons.Fault, Detail: err.Error()}, err
	}
	if found {
		if rec.Approved {
			return Decision[R]{Allow: true, Reason: s.reasons.Approved}, nil
		}
		return Decision[R]{Reason: s.reasons.Declined}, nil
	}
	if ask == nil {
		return Decision[R]{Reason: s.reasons.Unasked}, nil
	}
	approved, answered, aerr := ask(ctx, k)
	if aerr != nil {
		return Decision[R]{Reason: s.reasons.Unasked, Detail: aerr.Error()}, aerr
	}
	if !answered {
		return Decision[R]{Reason: s.reasons.Unasked}, nil
	}
	d := Decision[R]{Allow: approved, Reason: s.reasons.Declined}
	if approved {
		d.Reason = s.reasons.Approved
	}
	if _, serr := s.Set(k, approved); serr != nil {
		d.Detail = serr.Error()
		return d, serr
	}
	return d, nil
}
