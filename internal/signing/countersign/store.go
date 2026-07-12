// Package countersign implements the countersignature STORE: the physical,
// on-disk directory of individual .sig files a countersignature record
// (spec §9.2) lives in, plus the write and candidate-lookup primitives.
//
// This package deliberately knows nothing about trust.Ref, bundle kinds, or
// the review decision function — it operates purely in terms of the
// signing package's closed vocabulary (signing.ItemKind, signing.Form,
// signing.CountersignHeader) so it has no dependency on package operations
// or package trust. The operations-level ReviewRecords adapter (which DOES
// know about trust.Ref, the two physical stores' union, and cross-store
// rejection precedence) is built on top of this package.
//
// A Store here is ONE physical store — either the user-global store
// (~/.ctxloom/approvals) or the project/committable store (.ctxloom/approvals,
// spec §9.2). Neither knows about the other; composing them (union reads,
// "rejection beats an inherited approval") is the caller's job.
package countersign

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// Store is one physical countersignature store: a directory of individual
// .sig files. It offers exactly two capabilities — write one signature, and
// find + verify CANDIDATE signatures for a (header, payload) query. It never
// answers "is X approved" on the strength of a filename alone.
type Store struct {
	dir string
	fs  afero.Fs
}

// NewStore builds a Store rooted at dir, backed by fs. dir need not exist yet
// — it is created lazily on the first write; a Store over a nonexistent dir
// reads as empty (no candidates), which is the correct "nothing approved or
// rejected yet" starting state.
func NewStore(dir string, fs afero.Fs) *Store {
	return &Store{dir: dir, fs: fs}
}

// Dir returns the store's root directory.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Readable probes whether this store can actually be read, distinguishing
// two situations a caller must NOT treat alike:
//
//   - The directory does not exist yet. This is the normal shape for a
//     fresh project or a user who has never run `ctxloom review` —
//     "nothing recorded" — and Readable returns nil.
//   - The directory exists but cannot be listed (permission denied, an I/O
//     error), or it lists fine but one of the record files inside it
//     cannot be opened (permission denied, corrupted at the filesystem
//     level). Either way Readable returns a non-nil error.
//
// The distinction matters because this store's silence is load-bearing: an
// empty store means "nothing rejected", but a store this process simply
// cannot SEE might be hiding a REJECTION — and rejection is supposed to be
// supreme (spec §9.3). A caller that cannot tell "empty" from "blind" and
// treats both as "nothing rejected" has silently reopened a gate a human
// closed. This method exists so the caller (EffectiveTrust's
// records-construction preamble) can fail closed instead.
//
// It deliberately does NOT attempt to parse or verify any file's contents —
// a signature that reads fine but fails cryptographic verification is not
// an error here (see Verified: that is the normal "not proven" outcome).
// Readable only asks "can every byte on disk actually be read", nothing
// more.
func (s *Store) Readable() error {
	if s == nil {
		return nil
	}
	entries, err := afero.ReadDir(s.fs, s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("countersignature store %s: %w", s.dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		f, ferr := s.fs.Open(path)
		if ferr != nil {
			return fmt.Errorf("countersignature store %s: cannot read %s: %w", s.dir, entry.Name(), ferr)
		}
		_ = f.Close()
	}
	return nil
}

// indexHash is the filename's content-addressed key: sha256 of the FULL
// framed countersign payload (header + bytes), not merely the raw payload
// bytes illustrated in the spec's example filename. This is a deliberate
// deviation from the spec's literal `sha256(payload_bytes)`, and the reason
// matters: hashing only the raw bytes would collide an approve-raw and an
// approve-distilled signature over IDENTICAL content at the same ref onto the
// same filename (and every ref-reject, whose payload is always empty, onto
// one shared filename) — the second write would silently clobber the first.
// Hashing the full framed payload makes the key unique per
// (assertion, kind, ref, form, bytes) tuple, which is exactly the granularity
// a countersignature is scoped to, while it remains a pure INDEX: at read
// time the caller reconstructs this SAME hash from the header+payload it
// already has in hand (never from a filename it read off disk), so an
// attacker renaming a file gains nothing — the candidate must still verify
// (spec §9.3, implementer trap #2).
func indexHash(header signing.CountersignHeader, payload []byte) string {
	sum := sha256.Sum256(signing.CountersignPayload(header, payload))
	return hex.EncodeToString(sum[:])
}

// keyTag is a short, filename-safe, UNTRUSTED disambiguator distinguishing
// multiple signers' countersignatures over the same (header, payload) — e.g.
// both an org's reviewer key and a second maintainer's key approving the same
// content. It is never read back as identity; the identity always comes from
// re-verifying the signature blob itself.
func keyTag(pub ssh.PublicKey) string {
	if pub == nil {
		return "unknown"
	}
	sum := sha256.Sum256(pub.Marshal())
	return hex.EncodeToString(sum[:])[:12]
}

func filename(header signing.CountersignHeader, payload []byte, pub ssh.PublicKey) string {
	return indexHash(header, payload) + "." + string(header.Assertion) + "." + keyTag(pub) + ".sig"
}

// write signs header+payload with signer under namespace and persists the
// resulting armored signature under this store's directory.
func (s *Store) write(header signing.CountersignHeader, payload []byte, signer ssh.Signer, namespace string) error {
	armored, err := signing.Sign(signing.CountersignPayload(header, payload), signer, namespace)
	if err != nil {
		return err
	}
	if err := s.fs.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.dir, filename(header, payload, signer.PublicKey()))
	return afero.WriteFile(s.fs, path, armored, 0o644)
}

// WriteApprove signs and stores a ref-scoped, form-scoped approve
// countersignature (spec §5.2) — "I reviewed exactly these bytes, at this
// ref, in this form".
func (s *Store) WriteApprove(kind signing.ItemKind, ref string, form signing.Form, payload []byte, signer ssh.Signer) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Kind: kind, Ref: ref, Form: form}
	return s.write(h, payload, signer, signing.NamespaceApprove)
}

// WriteContentReject signs and stores a REF-OMITTED reject countersignature
// (spec §5.3) — these bytes, wherever they appear, in this form, are refused.
// Callers emit one of these per form the item currently has (raw and
// distilled), mirroring the deleted denylist's two-hash rejection.
func (s *Store) WriteContentReject(kind signing.ItemKind, form signing.Form, payload []byte, signer ssh.Signer) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Kind: kind, Ref: "", Form: form}
	return s.write(h, payload, signer, signing.NamespaceReject)
}

// WriteRefReject signs and stores the STICKY ref-level block (spec §5.3):
// form is always FormNone and the payload is always empty — this blocks the
// ref regardless of what its content becomes.
func (s *Store) WriteRefReject(kind signing.ItemKind, ref string, signer ssh.Signer) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Kind: kind, Ref: ref, Form: signing.FormNone}
	return s.write(h, nil, signer, signing.NamespaceReject)
}

// candidates returns the armored signature blobs found under header+payload's
// index hash. Finding one proves NOTHING — see Verified.
func (s *Store) candidates(header signing.CountersignHeader, payload []byte) [][]byte {
	if s == nil {
		return nil
	}
	pattern := filepath.Join(s.dir, indexHash(header, payload)+".*.sig")
	matches, err := afero.Glob(s.fs, pattern)
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	out := make([][]byte, 0, len(matches))
	for _, m := range matches {
		data, rerr := afero.ReadFile(s.fs, m)
		if rerr != nil {
			continue
		}
		out = append(out, data)
	}
	return out
}

// Verified reports whether ANY candidate signature for header+payload in this
// store verifies under a key root trusts for the assertion's namespace, at
// time now. This is the store's ONLY entry point that may say yes: every
// candidate is cryptographically re-verified against the RECONSTRUCTED
// payload (signing.VerifyCountersignature) before it counts, so a
// hand-crafted file, a corrupted signature body, or an untrusted signer all
// resolve to (false, "") — never to an approval or rejection (spec §9.3,
// implementer trap #2; and see the required trap test in the operations
// package: a corrupted signature body at the RIGHT index hash still resolves
// pending, never allow).
func (s *Store) Verified(header signing.CountersignHeader, payload []byte, root signing.TrustRoot, now time.Time) (principal string, ok bool) {
	if s == nil {
		return "", false
	}
	for _, armored := range s.candidates(header, payload) {
		if p, verified := signing.VerifyCountersignature(header, payload, armored, root, now); verified {
			return p, true
		}
	}
	return "", false
}

// VerifiedApprove is the Verified convenience wrapper for an approve query.
func (s *Store) VerifiedApprove(kind signing.ItemKind, ref string, form signing.Form, payload []byte, root signing.TrustRoot, now time.Time) (string, bool) {
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Kind: kind, Ref: ref, Form: form}
	return s.Verified(h, payload, root, now)
}

// VerifiedContentReject is the Verified convenience wrapper for a
// content-reject query (ref omitted, matching WriteContentReject).
func (s *Store) VerifiedContentReject(kind signing.ItemKind, form signing.Form, payload []byte, root signing.TrustRoot, now time.Time) (string, bool) {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Kind: kind, Ref: "", Form: form}
	return s.Verified(h, payload, root, now)
}

// VerifiedRefReject is the Verified convenience wrapper for a ref-reject
// query (matching WriteRefReject: form none, payload empty).
func (s *Store) VerifiedRefReject(kind signing.ItemKind, ref string, root signing.TrustRoot, now time.Time) (string, bool) {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Kind: kind, Ref: ref, Form: signing.FormNone}
	return s.Verified(h, nil, root, now)
}

// --- the degraded, UNSIGNED path (spec §9.5) ---------------------------------
//
// "Signing must never become a barrier to plain local use." When there is
// genuinely no key available, a review decision may be recorded UNSIGNED — a
// bare marker file, no signature — which is EXACTLY as safe as the deleted
// trust.yaml design: anything that can write this store can forge one. That
// is why callers (operations.SetItemTrust / SetBlacklist) must NEVER route an
// unsigned write to the committable PROJECT store — an unsigned record is
// strictly local and non-shareable (spec §9.5: "a shareable approval with no
// signature is a forgery primitive with a friendly name").

func unsignedFilename(header signing.CountersignHeader, payload []byte) string {
	return indexHash(header, payload) + "." + string(header.Assertion) + ".unsigned"
}

// writeUnsigned records header+payload as an unsigned marker: existence is
// the entire record, and it carries no cryptographic weight whatsoever.
func (s *Store) writeUnsigned(header signing.CountersignHeader, payload []byte) error {
	if err := s.fs.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.dir, unsignedFilename(header, payload))
	return afero.WriteFile(s.fs, path, []byte("unsigned\n"), 0o644)
}

func (s *Store) hasUnsigned(header signing.CountersignHeader, payload []byte) bool {
	if s == nil {
		return false
	}
	exists, err := afero.Exists(s.fs, filepath.Join(s.dir, unsignedFilename(header, payload)))
	return err == nil && exists
}

// WriteUnsignedApprove records an unsigned approve marker (degraded path).
func (s *Store) WriteUnsignedApprove(kind signing.ItemKind, ref string, form signing.Form, payload []byte) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Kind: kind, Ref: ref, Form: form}
	return s.writeUnsigned(h, payload)
}

// WriteUnsignedContentReject records an unsigned content-reject marker.
func (s *Store) WriteUnsignedContentReject(kind signing.ItemKind, form signing.Form, payload []byte) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Kind: kind, Ref: "", Form: form}
	return s.writeUnsigned(h, payload)
}

// WriteUnsignedRefReject records an unsigned ref-reject marker.
func (s *Store) WriteUnsignedRefReject(kind signing.ItemKind, ref string) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Kind: kind, Ref: ref, Form: signing.FormNone}
	return s.writeUnsigned(h, nil)
}

// HasUnsignedApprove reports whether an unsigned approve marker exists for
// exactly this (kind, ref, form, payload).
func (s *Store) HasUnsignedApprove(kind signing.ItemKind, ref string, form signing.Form, payload []byte) bool {
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Kind: kind, Ref: ref, Form: form}
	return s.hasUnsigned(h, payload)
}

// HasUnsignedContentReject reports whether an unsigned content-reject marker
// exists for exactly this (kind, form, payload).
func (s *Store) HasUnsignedContentReject(kind signing.ItemKind, form signing.Form, payload []byte) bool {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Kind: kind, Ref: "", Form: form}
	return s.hasUnsigned(h, payload)
}

// HasUnsignedRefReject reports whether an unsigned ref-reject marker exists
// for exactly this (kind, ref).
func (s *Store) HasUnsignedRefReject(kind signing.ItemKind, ref string) bool {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Kind: kind, Ref: ref, Form: signing.FormNone}
	return s.hasUnsigned(h, nil)
}

// --- the display-only sidecar index (spec §9.2) -------------------------------
//
// "A sidecar index.yaml MAY cache {ref, kind, form, principal, reviewed_at}
// for `ctxloom approvals list` to render without verifying everything; it is
// UNTRUSTED display metadata and must never be an input to step 1–6." This
// store uses it for exactly that: `ctxloom review` labels an item UPDATE vs
// NEW and offers a diff base by consulting the index — never by trusting it
// as an approval. Every actual trust decision still goes through
// Verified/VerifiedApprove/etc., which re-verifies from scratch.

// IndexEntry is one appended record in the sidecar index.
type IndexEntry struct {
	Ref         string `yaml:"ref"`
	Kind        string `yaml:"kind"`
	Form        string `yaml:"form"`
	Assertion   string `yaml:"assertion"`
	Principal   string `yaml:"principal,omitempty"`
	Unsigned    bool   `yaml:"unsigned,omitempty"`
	PayloadHash string `yaml:"payload_hash"`
	ReviewedAt  string `yaml:"reviewed_at"`
}

func (s *Store) indexPath() string {
	return filepath.Join(s.dir, "index.yaml")
}

func (s *Store) readIndex() []IndexEntry {
	if s == nil {
		return nil
	}
	data, err := afero.ReadFile(s.fs, s.indexPath())
	if err != nil {
		return nil
	}
	var entries []IndexEntry
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil
	}
	return entries
}

// AppendIndex appends one record to the sidecar index (best-effort: a write
// failure here must never fail the caller's actual countersignature write,
// which already succeeded and is the record that matters).
func (s *Store) AppendIndex(e IndexEntry) error {
	if s == nil {
		return nil
	}
	entries := append(s.readIndex(), e)
	data, err := yaml.Marshal(entries)
	if err != nil {
		return err
	}
	if err := s.fs.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return afero.WriteFile(s.fs, s.indexPath(), data, 0o644)
}

// LatestApprove returns the most recently appended approve index entry for
// (kind, ref, form), or (_, false) if none exist. Display-only: a caller MUST
// still verify before treating anything about the result as an approval — this
// only answers "was there ever a prior approval attempt here", to decide
// whether to label a pending item UPDATE and to pick a diff base.
func (s *Store) LatestApprove(kind signing.ItemKind, ref string, form signing.Form) (IndexEntry, bool) {
	var latest IndexEntry
	found := false
	for _, e := range s.readIndex() {
		if e.Assertion != string(signing.AssertionApprove) || e.Kind != string(kind) ||
			e.Ref != ref || e.Form != string(form) {
			continue
		}
		if !found || e.ReviewedAt > latest.ReviewedAt {
			latest = e
			found = true
		}
	}
	return latest, found
}
