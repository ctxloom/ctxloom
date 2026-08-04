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

// Store is one domain's personal record of admission decisions: a single
// YAML file under the user's home.
type Store[K comparable, R comparable] struct {
	fs      afero.Fs
	path    string
	key     func(K) string
	scope   func(K) string
	now     func() time.Time
	reasons Reasons[R]
	// misconfigured is the construction fault, held rather than panicked so
	// construction stays total. Every method surfaces it; nothing reads or
	// writes through a store that carries one.
	misconfigured error
}

// options carries the optional halves of NewStore.
type options[K comparable] struct {
	scope func(K) string
	now   func() time.Time
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
	s := &Store[K, R]{fs: fs, path: path, key: key, scope: o.scope, now: o.now, reasons: reasons}
	if s.scope == nil {
		s.scope = key
	}
	switch {
	case key == nil:
		s.misconfigured = errors.New("admission: store built with no key function")
	default:
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

// Reasons reports the vocabulary this store decides in, so a caller rendering
// a Decision it did not produce can still name the outcomes.
func (s *Store[K, R]) Reasons() Reasons[R] {
	if s == nil {
		var zero Reasons[R]
		return zero
	}
	return s.reasons
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
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := s.scope(out[i].Key), s.scope(out[j].Key)
		if si != sj {
			return si < sj
		}
		return s.key(out[i].Key) < s.key(out[j].Key)
	})
	return out, nil
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
func (s *Store[K, R]) Set(k K, approved bool) (Record[K], error) {
	var zero Record[K]
	snap, err := s.Load()
	if err != nil {
		// Refuse to overwrite a record we could not read: the file may hold
		// decisions this write would silently erase.
		return zero, fmt.Errorf("admission record is unreadable, refusing to overwrite it: %w", err)
	}
	rec := Record[K]{Key: k, Approved: approved, RecordedAt: s.now()}
	want := s.scope(k)
	kept := make([]Record[K], 0, len(snap.records)+1)
	for _, r := range snap.records {
		if s.scope(r.Key) != want {
			kept = append(kept, r)
		}
	}
	kept = append(kept, rec)
	if werr := s.write(kept); werr != nil {
		return zero, werr
	}
	return rec, nil
}

// Forget drops every recorded decision in k's scope and reports how many went.
// Zero removed is reported as ZERO, never as success-with-no-effect: undoing
// something nobody recorded is the caller's mistake to see.
func (s *Store[K, R]) Forget(k K) (int, error) {
	snap, err := s.Load()
	if err != nil {
		return 0, err
	}
	want := s.scope(k)
	kept := make([]Record[K], 0, len(snap.records))
	for _, r := range snap.records {
		if s.scope(r.Key) != want {
			kept = append(kept, r)
		}
	}
	removed := len(snap.records) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	if werr := s.write(kept); werr != nil {
		return 0, werr
	}
	return removed, nil
}

// write serializes recs, 0600 in a 0700 directory. These records decide
// whether code runs and where signed content is pushed: world-readable would
// leak the machine's layout, and world-WRITABLE would hand the decision away.
func (s *Store[K, R]) write(recs []Record[K]) error {
	if err := s.configured(); err != nil {
		return err
	}
	sort.SliceStable(recs, func(i, j int) bool {
		si, sj := s.scope(recs[i].Key), s.scope(recs[j].Key)
		if si != sj {
			return si < sj
		}
		return s.key(recs[i].Key) < s.key(recs[j].Key)
	})
	data, err := yaml.Marshal(doc[K]{Version: storeVersion, Records: recs})
	if err != nil {
		return fmt.Errorf("marshal admission records: %w", err)
	}
	if mkErr := s.fs.MkdirAll(filepath.Dir(s.path), 0o700); mkErr != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.path), mkErr)
	}
	if werr := afero.WriteFile(s.fs, s.path, data, 0o600); werr != nil {
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
