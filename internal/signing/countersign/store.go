// Package countersign implements the countersignature STORE: the physical,
// on-disk directory of individual .sig files a countersignature record
// (spec §9.2) lives in, plus the write and candidate-lookup primitives.
//
// This package deliberately knows nothing about trust.Ref, bundle kinds, or
// the review decision function — it operates purely in terms of the
// signing package's closed vocabulary (signing.AttestationForm,
// signing.CountersignHeader) so it has no dependency on package operations
// or package trust. The operations-level ReviewRecords adapter (which DOES
// know about trust.Ref, derives the attestation form from the item's kind, and
// composes the two physical stores' union and cross-store rejection
// precedence) is built on top of this package.
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
	"strings"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// Store is one physical countersignature store: a directory holding THREE
// kinds of record, with three different authority models. They share a
// directory and a type; they do not share a trust story, and a reader who
// takes one for another gets the security of the weakest.
//
//  1. SIGNED records (.sig). Cryptographic authority. Write one with the
//     assertion wrappers; ask about one with the Verified* wrappers, which
//     re-verify every candidate against the reconstructed payload under a
//     trust root. A filename proves nothing here — see verified.
//
//  2. UNSIGNED markers (.unsigned), the degraded path of spec §9.5.
//     EXISTENCE IS THE ENTIRE RECORD: HasUnsignedApprove answers "is X
//     approved" from a filename and nothing else, so anything that can write
//     this directory can forge one. That is not a defect, it is the documented
//     cost of "signing must never become a barrier to plain local use" — and
//     it is why callers must NEVER route an unsigned write to the committable
//     PROJECT store (spec §9.5: "a shareable approval with no signature is a
//     forgery primitive with a friendly name").
//
//  3. The sidecar index (index.yaml). NO authority whatsoever — display
//     metadata, never an input to a trust decision. It exists so `ctxloom
//     review` can label an item UPDATE vs NEW and offer a diff base.
//
// This doc used to say the type offered "exactly two capabilities" and "never
// answers 'is X approved' on the strength of a filename alone". The second
// sentence is false of (2) by design, which is exactly the thing a reader most
// needs to know.
type Store struct {
	dir string
	fs  afero.Fs
}

// NewStore builds a Store rooted at dir, backed by fs. dir need not exist yet
// — it is created lazily on the first write; a Store over a nonexistent dir
// reads as empty (no candidates), which is the correct "nothing approved or
// rejected yet" starting state.
//
// An EMPTY dir is not that state and never was: it is a store nobody managed
// to configure (the real trigger is `$HOME` being unresolvable, which leaves
// operations' user-store construction with ""). Such a Store is inert — every
// read answers nothing, every write refuses, and Readable reports the
// misconfiguration so the caller's fail-closed gate fires. See configured.
func NewStore(dir string, fs afero.Fs) *Store {
	return &Store{dir: dir, fs: fs}
}

// configured reports the "nobody gave this store a directory" fault as an
// error. It exists because filepath.Join("", x) == x: WITHOUT this guard a
// dir "" store does not read as empty, it reads the PROCESS WORKING
// DIRECTORY. Since unsigned markers are honoured with no cryptographic
// verification whatsoever (§9.5), that turned a file committed at a repo root
// into an unconditional approval of attacker-chosen bytes the moment $HOME
// went missing. Every path that touches s.dir consults this first.
func (s *Store) configured() error {
	if s == nil || s.dir != "" {
		return nil
	}
	return fmt.Errorf("countersignature store: no directory configured (unresolvable home directory?)")
}

// Readable is the two-answer projection of Resolve that EffectiveTrust's
// fail-closed preamble consults: nil means "you may take a verdict from this
// store", non-nil means "deny everything and say why".
//
// It is deliberately NOT a bare `_, err := s.Resolve(); return err`. Two of
// Resolve's fault states are tolerated here, and each tolerance is a decision
// with a reason:
//
//   - A nil *Store. "No project store configured" is a supported caller
//     shape; a nil store holds no records and cannot hide one.
//   - StateAbsent. A directory that has never been written to is the normal
//     fresh-project / fresh-user shape, and it is ALSO what an approvals
//     volume that failed to mount looks like. Those two are indistinguishable
//     from the filesystem alone, so denying on absence would deny every fresh
//     checkout — an outage — while admitting on absence discards a rejection
//     an operator believes is in force. Telling them apart requires the store
//     directory to be PROVISIONED (created by init, tracked in the committable
//     project store) so that absence can only mean "it went away"; that is an
//     on-disk-layout decision, not one this function may take, and until it is
//     taken this tolerance is the documented, deliberate hole. Resolve is the
//     seam it closes through: the state is already named and already
//     distinguishable, so the change is this branch and the two tests that
//     pin it (TestStore_Readable_AbsentDirectory_IsNotAnError here, and
//     operations' TestEffectiveTrust_AbsentApprovalsStore_NormalPending).
//
// StateUnconfigured and StateUnreadable are reported. An unconfigured store
// would otherwise read the PROCESS WORKING DIRECTORY (filepath.Join("", x) ==
// x) where an unsigned marker is honoured with no verification at all; an
// unreadable one might be hiding a REJECTION, and rejection is supreme
// (spec §9.3). A caller that cannot tell "empty" from "blind" and treats both
// as "nothing rejected" has silently reopened a gate a human closed.
func (s *Store) Readable() error {
	if s == nil {
		return nil
	}
	state, err := s.Resolve()
	if state == StateAbsent {
		return nil
	}
	return err
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
//
// The tuple it is unique per is (assertion, ref, attestation form, bytes): the
// item's ROLE is inside the framed preimage as part of the form, so a fragment
// and an MCP server with byte-identical payloads land on DIFFERENT filenames
// and neither one's record can be found by the other's query.
func indexHash(header signing.CountersignHeader, payload []byte) string {
	sum := sha256.Sum256(signing.CountersignPreimage(header, payload))
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

// filename is the naming CONTRACT between this file's writers and its readers,
// and the only place it is expressed:
//
//	<indexHash>.<assertion>.<keyTag>.sig
//
// candidates finds records by matching the leading "<indexHash>." as a literal
// prefix, so the hash must come first and the separator must be a '.'; it
// keeps every match and re-verifies each, which is what lets the keyTag
// disambiguate several signers over the same (header, payload) without any of
// them being read back as identity. Changing the order or the separator
// silently orphans every record already on disk — they stay on disk, stay
// valid, and are never found again, which on the reject path reads as nothing
// rejected.
func filename(header signing.CountersignHeader, payload []byte, pub ssh.PublicKey) string {
	return indexHash(header, payload) + "." + string(header.Assertion) + "." + keyTag(pub) + ".sig"
}

// pinsBytes enforces the one shape rule that differs between the two
// assertions: an APPROVE must pin bytes, a REJECT need not.
//
// The reader already refuses an empty-payload approval — the composing
// adapter's Approved() returns false for len(payload) == 0 before it touches
// any store, because an approval that pinned nothing is meaningless — so
// writing one produced a record that could never be honoured while telling
// the user "approved" with a key fingerprint against it, leaving the item
// pending forever. Refusing the write is the fail-loud form of a rule the
// read side already relies on.
//
// A REJECT is deliberately exempt: a ref-reject pins no bytes by design (it
// blocks the ref whatever its content becomes, spec §5.3). Keeping the rule
// here rather than in the approve wrappers is what keeps the twelve
// assertion wrappers uniform — the asymmetry is a property of the assertion,
// not of two functions.
func pinsBytes(header signing.CountersignHeader, payload []byte) error {
	if header.Assertion != signing.AssertionApprove || len(payload) > 0 {
		return nil
	}
	return fmt.Errorf("countersign approve %s %s: refusing to record an empty payload — an approval that pinned nothing can never be honoured", header.Form, header.Ref)
}

// write signs header+payload with signer and persists the resulting armored
// signature under this store's directory.
//
// The signing namespace is DERIVED from header.Assertion, through the same
// function the verifier uses (signing.NamespaceForAssertion). It used to be a
// parameter independent of the assertion, hand-re-encoded at each of the
// assertion wrappers below — three chances to write a record under a namespace
// the verifier will never look in. Such a record is written, reported to the
// user with a key fingerprint against it, and then verifies for nobody; on the
// REJECT path that is a fail-open, because an unverifiable rejection reads as
// "nothing rejected" and silently un-rejects the item.
func (s *Store) write(header signing.CountersignHeader, payload []byte, signer ssh.Signer) error {
	if err := s.configured(); err != nil {
		return err
	}
	// Fail LOUD on a header outside the closed vocabulary: a record signed
	// under a form no query can reconstruct is unreachable forever, so it
	// would tell the user "approved" and leave the item pending.
	if err := header.Validate(); err != nil {
		return err
	}
	if err := pinsBytes(header, payload); err != nil {
		return err
	}
	armored, err := signing.Sign(signing.CountersignPreimage(header, payload), signer, signing.NamespaceForAssertion(header.Assertion))
	if err != nil {
		return err
	}
	if err := s.fs.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.dir, filename(header, payload, signer.PublicKey()))
	// No AllowEmpty: armored is signing.Sign's output, never zero-length, and
	// the filename is content-addressed (indexHash), so a re-write at the
	// same path is always the identical bytes — the default empty-over-
	// existing guard can never legitimately fire here; if it ever did, that
	// would mean signing.Sign returned nothing, which should fail loud rather
	// than silently drop a record that reports success.
	//
	// Durable: a countersignature is rotation lineage — the human review
	// record a verifier trusts — and unrecoverable if the rename silently
	// reverts to naming nothing after a crash.
	return iox.WriteFileAtomicFs(s.fs, path, armored, 0o644, iox.Durable())
}

// WriteApprove signs and stores a ref-scoped, form-scoped approve
// countersignature (spec §5.2) — "I reviewed exactly these bytes, at this
// ref, in this form".
//
// An EMPTY payload is refused — see pinsBytes.
func (s *Store) WriteApprove(ref string, form signing.AttestationForm, payload []byte, signer ssh.Signer) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Ref: ref, Form: form}
	return s.write(h, payload, signer)
}

// WriteContentReject signs and stores a REF-OMITTED reject countersignature
// (spec §5.3) — these bytes, wherever they appear, in this form, are refused.
// Callers emit one of these per form the item currently has (raw and
// distilled), mirroring the deleted denylist's two-hash rejection.
func (s *Store) WriteContentReject(form signing.AttestationForm, payload []byte, signer ssh.Signer) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: "", Form: form}
	return s.write(h, payload, signer)
}

// WriteRefReject signs and stores the STICKY ref-level block (spec §5.3):
// form is always FormNone and the payload is always empty — this blocks the
// ref regardless of what its content becomes.
func (s *Store) WriteRefReject(ref string, signer ssh.Signer) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: ref, Form: signing.AttestNone}
	return s.write(h, nil, signer)
}

// headerInvalid reports a header the closed vocabulary rejects, on the READ
// paths, where the answer must be "nothing recorded" rather than an error: a
// lookup is not a place to fail loud, and every read here feeds a decision
// whose default is to withhold. The WRITE paths call Validate directly instead,
// because there the same fault must be reported, not absorbed.
func (s *Store) headerInvalid(header signing.CountersignHeader) bool {
	return header.Validate() != nil
}

// candidates returns the armored signature blobs found under header+payload's
// index hash. Finding one proves NOTHING — see verified.
//
// The directory is LISTED and the index hash matched as a literal prefix,
// never globbed. Globbing joined s.dir into a PATTERN, so every metacharacter
// in the store's own path was interpreted rather than matched: an unterminated
// '[' anywhere in it made filepath.Match return ErrBadPattern, and a
// well-formed class like [ab] matched a different directory. Both made every
// query into the store come back empty, forever, with Readable() — which lists
// the directory by its literal name — reporting it perfectly healthy. On the
// REJECT path "no candidates" is indistinguishable from "nothing rejected", so
// that silently un-rejected everything a human had refused.
//
// The remaining dropped errors are droppable HERE, and only because something
// else catches them: a directory that cannot be listed and a record that
// cannot be read both fail Store.Readable, which walks every file in the store
// and gates EffectiveTrust's fail-closed preamble. This function answers one
// query and cannot tell "no record" from "a record I could not see"; Readable
// is the pass that can. Do not silence an error here without checking that
// Readable still sees it.
func (s *Store) candidates(header signing.CountersignHeader, payload []byte) [][]byte {
	if s == nil {
		return nil
	}
	if s.configured() != nil {
		// Not "no candidates here" but "there is no here" — listing an
		// unconfigured store would list the working directory.
		return nil
	}
	// Fail CLOSED on a header outside the closed vocabulary: an unrecognized
	// form must find nothing, never be framed and matched on its bytes alone.
	if s.headerInvalid(header) {
		return nil
	}
	entries, err := afero.ReadDir(s.fs, s.dir)
	if err != nil {
		return nil // see Readable
	}
	prefix := indexHash(header, payload) + "."
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".sig") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([][]byte, 0, len(names))
	for _, n := range names {
		data, rerr := afero.ReadFile(s.fs, filepath.Join(s.dir, n))
		if rerr != nil {
			continue // see Readable
		}
		out = append(out, data)
	}
	return out
}

// verified reports whether ANY candidate signature for header+payload in this
// store verifies under a key root trusts for the assertion's namespace, at
// time now. This is the store's ONLY entry point that may say yes: every
// candidate is cryptographically re-verified against the RECONSTRUCTED
// payload (signing.VerifyCountersignature) before it counts, so a
// hand-crafted file, a corrupted signature body, or an untrusted signer all
// resolve to (false, "") — never to an approval or rejection (spec §9.3,
// implementer trap #2; and see the required trap test in the operations
// package: a corrupted signature body at the RIGHT index hash still resolves
// pending, never allow).
//
// Unexported — its only callers are the three wrappers below
// (verified zero external/test callers of the exported spelling), which
// are the intended API surface.
func (s *Store) verified(header signing.CountersignHeader, payload []byte, root signing.TrustRoot, now time.Time) (principal string, ok bool) {
	if s == nil {
		return "", false
	}
	for _, armored := range s.candidates(header, payload) {
		if p, ok := signing.VerifyCountersignature(header, payload, armored, root, now); ok {
			return p, true
		}
	}
	return "", false
}

// VerifiedApprove is the verified convenience wrapper for an approve query.
func (s *Store) VerifiedApprove(ref string, form signing.AttestationForm, payload []byte, root signing.TrustRoot, now time.Time) (string, bool) {
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Ref: ref, Form: form}
	return s.verified(h, payload, root, now)
}

// VerifiedContentReject is the verified convenience wrapper for a
// content-reject query (ref omitted, matching WriteContentReject).
func (s *Store) VerifiedContentReject(form signing.AttestationForm, payload []byte, root signing.TrustRoot, now time.Time) (string, bool) {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: "", Form: form}
	return s.verified(h, payload, root, now)
}

// VerifiedRefReject is the verified convenience wrapper for a ref-reject
// query (matching WriteRefReject: form none, payload empty).
func (s *Store) VerifiedRefReject(ref string, root signing.TrustRoot, now time.Time) (string, bool) {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: ref, Form: signing.AttestNone}
	return s.verified(h, nil, root, now)
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

// unsignedFilename is the degraded path's half of the same contract:
//
//	<indexHash>.<assertion>.unsigned
//
// hasUnsigned looks for this exact name in the directory listing. No keyTag,
// because there is no key — which is the whole of what makes this record
// forgeable (see the Store doc, record kind 2). The ".unsigned" suffix is what
// keeps these files out of Readable's signature parsing and out of candidates'
// ".sig" filter.
func unsignedFilename(header signing.CountersignHeader, payload []byte) string {
	return indexHash(header, payload) + "." + string(header.Assertion) + ".unsigned"
}

// writeUnsigned records header+payload as an unsigned marker: existence is
// the entire record, and it carries no cryptographic weight whatsoever.
func (s *Store) writeUnsigned(header signing.CountersignHeader, payload []byte) error {
	if err := s.configured(); err != nil {
		return err
	}
	// See write: an unreachable marker is worse than a refused write.
	if err := header.Validate(); err != nil {
		return err
	}
	if err := pinsBytes(header, payload); err != nil {
		return err
	}
	if err := s.fs.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.dir, unsignedFilename(header, payload))
	// No AllowEmpty: the marker's whole content is the fixed literal
	// "unsigned\n" — never zero-length — and the filename is
	// content-addressed, so a re-write at the same path is always identical
	// bytes.
	return iox.WriteFileAtomicFs(s.fs, path, []byte("unsigned\n"), 0o644)
}

// hasUnsigned reports whether the unsigned marker for header+payload is
// present. Existence IS the entire record here (spec §9.5) — there is nothing
// to re-verify — so getting this wrong in the absent direction does not
// degrade a rejection, it CANCELS one.
//
// It LISTS the directory rather than stat-ing the one path, which is not
// pedantry: a stat failure used to be folded into "absent" (err == nil &&
// exists), and a fault that hides a single path from Stat alone leaves
// Store.Readable — the whole-store guard that gates EffectiveTrust's
// fail-closed preamble — reporting the store perfectly healthy. Reading
// through the same ReadDir that Readable performs makes the two agree by
// construction: anything that can hide a marker from this function also fails
// Readable, so the fail-closed gate fires instead of the marker quietly
// vanishing.
func (s *Store) hasUnsigned(header signing.CountersignHeader, payload []byte) bool {
	if s == nil {
		return false
	}
	if s.configured() != nil {
		// See candidates: an unconfigured store must never read the working
		// directory, because a marker's mere existence IS the approval.
		return false
	}
	if s.headerInvalid(header) {
		return false
	}
	entries, err := afero.ReadDir(s.fs, s.dir)
	if err != nil {
		return false // see Readable
	}
	want := unsignedFilename(header, payload)
	for _, e := range entries {
		if !e.IsDir() && e.Name() == want {
			return true
		}
	}
	return false
}

// WriteUnsignedApprove records an unsigned approve marker (degraded path).
func (s *Store) WriteUnsignedApprove(ref string, form signing.AttestationForm, payload []byte) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Ref: ref, Form: form}
	return s.writeUnsigned(h, payload)
}

// WriteUnsignedContentReject records an unsigned content-reject marker.
func (s *Store) WriteUnsignedContentReject(form signing.AttestationForm, payload []byte) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: "", Form: form}
	return s.writeUnsigned(h, payload)
}

// WriteUnsignedRefReject records an unsigned ref-reject marker.
func (s *Store) WriteUnsignedRefReject(ref string) error {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: ref, Form: signing.AttestNone}
	return s.writeUnsigned(h, nil)
}

// HasUnsignedApprove reports whether an unsigned approve marker exists for
// exactly this (kind, ref, form, payload).
func (s *Store) HasUnsignedApprove(ref string, form signing.AttestationForm, payload []byte) bool {
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Ref: ref, Form: form}
	return s.hasUnsigned(h, payload)
}

// HasUnsignedContentReject reports whether an unsigned content-reject marker
// exists for exactly this (kind, form, payload).
func (s *Store) HasUnsignedContentReject(form signing.AttestationForm, payload []byte) bool {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: "", Form: form}
	return s.hasUnsigned(h, payload)
}

// HasUnsignedRefReject reports whether an unsigned ref-reject marker exists
// for exactly this (kind, ref).
func (s *Store) HasUnsignedRefReject(ref string) bool {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: ref, Form: signing.AttestNone}
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
// VerifiedApprove/VerifiedContentReject/VerifiedRefReject, which re-verifies
// from scratch.

// IndexEntry is one appended record in the sidecar index.
//
// Kind and Form are DISPLAY labels drawn from the item's live kind and its
// LAYOUT form — deliberately not the attestation vocabulary the preimage binds.
// The index has to outlive contract bumps: it is the only thing that can tell a
// human "you approved something here once" after their records stopped
// verifying, so keying it on the preimage vocabulary would make every
// superseded approval read as a first-time item instead of an update.
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

// IsAfter reports whether e was reviewed after other.
//
// ReviewedAt is free text in a YAML file: it is compared as an INSTANT when
// both stamps parse as RFC3339, and a stamp that parses beats one that does
// not. Neither rule is fussiness. Byte comparison — what this used to be — is
// correct only while every stamp is UTC and Z-suffixed, which holds for the
// single writer today and for nothing else: the index is a plain file a human
// may edit, and its own doc requires it to outlive contract bumps, so records
// this build did not write are expected. An offset-suffixed stamp for an
// earlier instant out-sorts a later UTC one, and a stamp that is not a
// timestamp at all beats every real one, because 'y' sorts above every digit.
//
// Getting it wrong picks the wrong DIFF BASE and can relabel an UPDATE as NEW
// — precisely the reading a reviewer relies on to notice substituted bytes.
// When neither stamp reads, the bytes are all that is left.
func (e IndexEntry) IsAfter(other IndexEntry) bool {
	et, eok := e.reviewedAt()
	ot, ook := other.reviewedAt()
	switch {
	case eok && ook:
		return et.After(ot)
	case eok != ook:
		return eok
	default:
		return e.ReviewedAt > other.ReviewedAt
	}
}

// reviewedAt parses the stamp, reporting whether it read as RFC3339 at all.
func (e IndexEntry) reviewedAt() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, e.ReviewedAt)
	return t, err == nil
}

// indexPath is the sidecar index's fixed location within the store.
func (s *Store) indexPath() string {
	return filepath.Join(s.dir, "index.yaml")
}

// readIndex reads and parses the sidecar index, distinguishing the three
// outcomes its callers must not conflate: absent (nil, nil — the normal fresh
// shape), present and parseable (entries, nil), and present but UNPARSEABLE
// (nil, err). Folding the third onto the first is what let one truncated
// write destroy the entire approval history: AppendIndex rebuilds the whole
// file from what this returns.
func (s *Store) readIndex() ([]IndexEntry, error) {
	if s == nil {
		return nil, nil
	}
	if err := s.configured(); err != nil {
		return nil, err
	}
	data, err := afero.ReadFile(s.fs, s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("countersignature index %s: %w", s.indexPath(), err)
	}
	var entries []IndexEntry
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("countersignature index %s is unparseable: %w", s.indexPath(), err)
	}
	return entries, nil
}

// AppendIndex appends one record to the sidecar index.
//
// It is a read-modify-write of the whole file, so it REFUSES when the
// existing index cannot be read or parsed: appending onto a failed read means
// rewriting the file from nothing, silently destroying every prior record.
// The index is display-only, but it is what labels an item UPDATE and
// supplies the diff base, so losing it makes substituted bytes look like a
// first-time item — a review-integrity loss, not a cosmetic one.
//
// The write goes through a temp file + rename so a crash mid-write cannot
// leave behind the truncated file that would trip the refusal above.
//
// The caller may still treat a failure here as non-fatal — the actual
// countersignature write already succeeded and is the record that matters —
// but it must not be told the append happened when it did not.
func (s *Store) AppendIndex(e IndexEntry) error {
	if s == nil {
		return nil
	}
	if err := s.configured(); err != nil {
		return err
	}
	existing, err := s.readIndex()
	if err != nil {
		return fmt.Errorf("refusing to rewrite the countersignature index: %w", err)
	}
	return s.writeIndex(append(existing, e))
}

// writeIndex replaces the sidecar index with entries, atomically, through
// iox.WriteFileAtomicFs (unique temp file + fsync + rename) — a crash
// mid-write cannot leave behind the truncated file that makes readIndex (and
// therefore every later append) refuse. yaml.Marshal of a []IndexEntry, even
// nil or empty, always renders "[]\n": this write can never be legitimately
// empty, so no AllowEmpty option is passed.
//
// It is the ONE writer of this file, shared by the append and the forget path,
// because "rewrite the whole index safely" is a single property and two
// copies of it are two chances to lose an approval history.
func (s *Store) writeIndex(entries []IndexEntry) error {
	data, err := yaml.Marshal(entries)
	if err != nil {
		return err
	}
	if err := s.fs.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	// Durable: the index is what labels an item UPDATE vs first-time and
	// supplies the diff base (see AppendIndex's doc) — rotation lineage that
	// a silently-reverted rename after a crash would corrupt back to a
	// truncated view, exactly the failure AppendIndex's own refusal-on-bad-
	// read guards against on the way in.
	return iox.WriteFileAtomicFs(s.fs, s.indexPath(), data, 0o644, iox.Durable())
}

// LatestApprove returns the most recently appended approve index entry for
// (ref, layout form), or (_, false, nil) if none exist. Display-only: a caller
// MUST still verify before treating anything about the result as an approval
// — this only answers "was there ever a prior approval attempt here", to
// decide whether to label a pending item UPDATE and to pick a diff base.
//
// The query is deliberately keyed on the LAYOUT form and NOT on the entry's kind
// label, and both halves of that are load-bearing:
//
//   - the ref already embeds the item's kind directory, so (ref, layout) is
//     exactly as precise as adding a kind term would be;
//   - it is what lets a record written under a SUPERSEDED contract still be
//     recognized. Those records name their kind in an older vocabulary and can
//     never verify again, but "a human approved something here once" is still
//     true and still worth showing. Matching on nothing that a bump changes is
//     what makes a stale approval read as STALE (an update to re-review) rather
//     than as ABSENT.
//
// Recognizing such a record is the ONLY thing done with it: it is reported, and
// never re-keyed onto the current vocabulary — re-keying would guess at the role
// a superseded record was approved in, which is precisely the confusion the
// composite attestation form exists to make impossible.
//
// The error return is what stops an unreadable index from silently answering
// "no prior approval": that answer relabels an UPDATE as NEW, which is
// exactly how substituted bytes would slip past a reviewer looking for a
// diff. Callers must not treat an error as "false".
func (s *Store) LatestApprove(ref string, layout signing.Form) (IndexEntry, bool, error) {
	entries, err := s.readIndex()
	if err != nil {
		return IndexEntry{}, false, err
	}
	var latest IndexEntry
	found := false
	for _, e := range entries {
		if e.Assertion != string(signing.AssertionApprove) ||
			e.Ref != ref || e.Form != string(layout) {
			continue
		}
		if !found || e.IsAfter(latest) {
			latest = e
			found = true
		}
	}
	return latest, found, nil
}
