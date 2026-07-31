package allowedsigners

import (
	"bytes"
	"time"

	"golang.org/x/crypto/ssh"
)

// Store is a parsed, queryable allowed_signers file — or the union of
// several (embedded defaults, user store, project store; see the package
// doc for the precedence/union model).
type Store struct {
	entries []Entry
	// parseErrors are the lines Parse could not turn into an Entry. They
	// ride ON the Store because the alternative — a second return value —
	// is DROPPABLE, and production call sites did drop it: a malformed line
	// then silently revoked a signer nobody revoked, invisibly, on the very
	// surfaces an operator uses to audit trust. See ParseErrors.
	parseErrors []*ParseError
	// sources is the load provenance of every location that fed this store
	// — see Source.
	sources []Source
}

// NewStore builds a Store directly from entries, without parsing text. The
// entries are deep-copied, so the caller keeps no handle on what it built.
// Useful for tests and for callers that construct entries programmatically
// (e.g. the embedded-defaults store, which never touches disk).
func NewStore(entries ...Entry) *Store {
	return &Store{entries: cloneEntries(entries)}
}

// Union combines the entries of several stores into one, preserving the
// order of the arguments and, within each, file order. Callers implement
// the "all locations are unioned; a key in any of them counts for the
// namespaces it lists there" precedence rule (allowed_signers §7 in the
// signature-envelope spec) by passing embedded-defaults, user, and
// project stores in that order — the order does not affect
// TrustedForNamespace/TrustedAs (every matching entry is considered), but
// it does affect which entry's Line is reported first in diagnostics. A
// nil *Store argument is ignored — nil means "this location was not
// consulted", and it is NOT the way to report a location that failed: use
// FailedSource, whose provenance this preserves (U136-F04).
func Union(stores ...*Store) *Store {
	var all []Entry
	var perrs []*ParseError
	var srcs []Source
	for _, st := range stores {
		if st == nil {
			continue
		}
		all = append(all, cloneEntries(st.entries)...)
		perrs = append(perrs, st.parseErrors...)
		srcs = append(srcs, st.sources...)
	}
	return &Store{entries: all, parseErrors: perrs, sources: srcs}
}

// Entries returns a DEEP copy of every successfully parsed entry, in file
// order. Nothing reachable from the result aliases the Store: mutating the
// slice, an Entry, or the Principals/Namespaces/ValidAfter/ValidBefore inside
// one cannot re-decide trust for anybody else. See Entry.clone.
func (s *Store) Entries() []Entry {
	if s == nil {
		return nil
	}
	return cloneEntries(s.entries)
}

// ParseErrors returns the lines that could not be parsed into an entry, in
// file order — empty for a Store built by NewStore.
//
// A dropped line is a signer that is NOT trusted despite the file saying it
// should be, so a caller that presents the Store as "the trust root" without
// consulting this is presenting a silently-shortened one. Reading it is
// mandatory on any surface that reports absence: "no entry for X" and "there
// is a line for X I could not read" are different answers.
func (s *Store) ParseErrors() []*ParseError {
	if s == nil {
		return nil
	}
	out := make([]*ParseError, len(s.parseErrors))
	copy(out, s.parseErrors)
	return out
}

// Decision is the result of a trust query against a Store.
type Decision struct {
	// Trusted is the answer to "is this key trusted for this namespace
	// (as this identity, for TrustedAs)?"
	Trusted bool
	// Principal is the matched entry's first principal, suitable for use
	// as ctxloom's "signer" identity (signature-envelope spec §4.3: the
	// signer is resolved from allowed_signers, never trusted from the
	// artifact's own advisory field). Empty when Trusted is false.
	//
	// An entry can list several principals (e.g. a shared team alias); by
	// convention this reports the first one written in the file. If a
	// deployment needs to know exactly which principal pattern matched an
	// externally-claimed identity, use TrustedAs and inspect
	// Decision.Entry.Principals directly with Entry.MatchesPrincipal.
	Principal string
	// Entry is a COPY of the matched entry, or nil when Trusted is false.
	//
	// A copy, not the entry itself: it used to be &s.entries[i], a writable
	// pointer into the store's own backing array, so a caller holding a
	// decision could set Namespaces to nil on it — "accepted for all
	// namespaces" — and promote the entry to every namespace, including the
	// reject namespace whose supremacy the design rests on.
	Entry *Entry
}

// TrustedForNamespace reports whether key is authorized, by any entry in
// the store, to make an assertion in namespace ns at time now — the
// question this package exists to answer (signature-envelope spec §8,
// steps 4/5: "signer's key is trusted for the <X> namespace"). It does
// not consider any externally claimed identity; the returned
// Decision.Principal is whichever entry's own principal matched the key.
//
// now is supplied by the caller and is never read from the system clock
// by this package — see Entry.ValidAt's doc for why that matters here.
func (s *Store) TrustedForNamespace(key ssh.PublicKey, ns string, now time.Time) Decision {
	return s.decide(principalCheck{}, key, ns, now)
}

// TrustedAs additionally requires that identity match the matching
// entry's principals pattern-list, mirroring `ssh-keygen -Y verify -I
// identity`. Use this when an external, unverified identity claim (e.g. a
// git committer email, or the advisory "signer" field of a companion
// loadout envelope) needs to be corroborated against the trust root
// rather than taken at face value.
func (s *Store) TrustedAs(identity string, key ssh.PublicKey, ns string, now time.Time) Decision {
	return s.decide(principalCheck{required: true, identity: identity}, key, ns, now)
}

// principalCheck is decide's "must the entry also corroborate a claimed
// identity, and which one" argument.
//
// It is a named two-field value rather than an *string whose NILNESS carried
// that meaning. In the package's single most security-critical function, a
// reader had to know that a nil pointer meant "skip the principal check" — so
// the difference between TrustedForNamespace and TrustedAs, which is the
// difference between "this key may sign here" and "this key may sign here AS
// this identity", was encoded in something a stray nil would silently satisfy.
// required is false for the zero value, so the SKIP is what you get by
// accident and the check is what you must ask for.
type principalCheck struct {
	required bool
	identity string
}

func (s *Store) decide(check principalCheck, key ssh.PublicKey, ns string, now time.Time) Decision {
	if s == nil || key == nil {
		return Decision{}
	}
	for i := range s.entries {
		e := &s.entries[i]
		if !entryGrants(e, check, key, ns, now) {
			continue
		}
		matched := e.clone()
		return Decision{Trusted: true, Principal: firstPrincipal(e), Entry: &matched}
	}
	return Decision{}
}

// entryGrants is the whole of one entry's trust test, in the order the
// signature-envelope spec states it. Every arm withholds; there is no arm that
// grants, which is what makes "no entry matched" the only default.
func entryGrants(e *Entry, check principalCheck, key ssh.PublicKey, ns string, now time.Time) bool {
	// cert-authority entries are recognized but never grant trust
	// through a direct key match: this package implements no SSH
	// certificate verification, and no certificate is ever presented
	// to it (see Entry.CertAuthority doc, and the package doc's
	// fail-closed decisions). Verified against real ssh-keygen: a
	// cert-authority-flagged entry refuses to verify a plain
	// signature even when every other field matches.
	if e.CertAuthority {
		return false
	}
	if !keysEqual(e.PublicKey, key) {
		return false
	}
	if check.required && !e.MatchesPrincipal(check.identity) {
		return false
	}
	if !e.MatchesNamespace(ns) {
		return false
	}
	return e.ValidAt(now)
}

// firstPrincipal is the identity a Decision reports for a matched entry — by
// convention the first one written in the file. See Decision.Principal.
func firstPrincipal(e *Entry) string {
	if len(e.Principals) == 0 {
		return ""
	}
	return e.Principals[0]
}

func keysEqual(a, b ssh.PublicKey) bool {
	if a == nil || b == nil {
		return false
	}
	return bytes.Equal(a.Marshal(), b.Marshal())
}

// Source is one location that was asked to contribute to a Store, and what
// came back. It exists because the type could not previously express "the
// trust root failed to load": an unreadable location, an absent one, an
// empty one and an entirely-garbage one all resolved to the same value, so a
// caller that wanted to refuse when the root did not actually load had
// nothing to ask (U136-F04). Union skipping a nil *Store was the erasure —
// the caller that returned nil on EACCES produced a union indistinguishable
// from one where that location simply held no keys.
type Source struct {
	// Path is the location, as the loader named it.
	Path string
	// Loaded is whether the location was read successfully. An ABSENT file
	// is Loaded — "there is nothing here" is a real answer.
	Loaded bool
	// Err is why the location failed, non-nil exactly when Loaded is false.
	Err error
	// Count is how many entries this location contributed.
	Count int
}

// FailedSource builds a Store that contributes NO entries and records why:
// the value a loader returns when it could not read a location at all.
func FailedSource(path string, err error) *Store {
	return &Store{sources: []Source{{Path: path, Loaded: false, Err: err}}}
}

// WithSource labels an already-parsed Store with the location it came from,
// returning a Store that carries that provenance. The receiver is not
// mutated.
func (s *Store) WithSource(path string) *Store {
	if s == nil {
		return &Store{sources: []Source{{Path: path, Loaded: true}}}
	}
	out := &Store{entries: cloneEntries(s.entries), parseErrors: s.parseErrors}
	out.sources = append(append([]Source{}, s.sources...), Source{Path: path, Loaded: true, Count: len(s.entries)})
	return out
}

// Len is how many entries this store trusts — without the full copy Entries
// allocates, and without the caller having to infer "did it load" from it.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	return len(s.entries)
}

// Sources returns the provenance of every location that fed this store, in
// union order.
func (s *Store) Sources() []Source {
	if s == nil {
		return nil
	}
	out := make([]Source, len(s.sources))
	copy(out, s.sources)
	return out
}

// LoadErrors returns only the sources that FAILED. A caller presenting this
// store as "the trust root" while any of these is non-empty is presenting a
// root that silently lost a location — the same class of lie ParseErrors
// exists to prevent one line at a time.
func (s *Store) LoadErrors() []Source {
	if s == nil {
		return nil
	}
	var out []Source
	for _, src := range s.sources {
		if !src.Loaded {
			out = append(out, src)
		}
	}
	return out
}
