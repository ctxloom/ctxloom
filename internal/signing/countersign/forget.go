package countersign

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// The fourth thing this store can do: UNRECORD a decision.
//
// Writing is how an item leaves the undecided state; this is how it gets back.
// The store holds three states and a caller with only write paths can move an
// item between the two decided ones forever without ever restoring the third,
// so a decision made in error could only be overwritten by its opposite —
// which, for an approval mistakenly recorded, meant reaching for a REJECTION,
// a far stronger and stickier statement than the one being withdrawn.
//
// A forget is addressed exactly as a query is: by the (assertion, ref, form,
// bytes) tuple, reconstructed into the same index hash the writer filed the
// record under. That is what makes it precise — it cannot clear a neighbouring
// decision it was not asked about — and it is also its one limitation: a
// content-reject can only be found from the bytes it covers, so a caller that
// cannot resolve an item's content can clear the ref block and nothing else.
// Callers must report that partial outcome rather than round it up.
//
// EVERY record under the tuple goes, across all three record kinds:
//
//   - every SIGNED candidate, whoever countersigned it. Several people may
//     have signed the same bytes at the same ref (see keyTag), and they are
//     one decision, not several — leaving one behind leaves the item decided
//     while the caller reports it cleared.
//   - the UNSIGNED marker, whose mere existence IS the decision (§9.5). It is
//     a separate file with a separate suffix, so a sweep of `.sig` alone would
//     print a cleared decision over an item that is exactly as withheld as it
//     was.
//
// Nothing is verified on the way out, deliberately. A record that no longer
// verifies — a superseded contract, a key since untrusted, a corrupted body —
// is still a file that Store.Readable reports and that a reader may yet be
// able to honour; refusing to remove what this process cannot currently prove
// would make the unverifiable records precisely the ones a user could never
// get rid of.
//
// Removal is REPORTED, never assumed: every path returns a count, and a delete
// this process could not perform is an error. A forget that silently failed
// would be the store's worst failure mode — the user is told the item is back
// to pending, and it is still withheld.

// forget removes every record filed under header+payload's index hash and
// reports how many files went.
//
// It refuses on an unconfigured store and on a header outside the closed
// vocabulary, matching the WRITE paths rather than the read ones: a query may
// answer "nothing recorded" when it cannot address a record, but a mutation
// asked to clear a decision must say it could not, not report a clean sweep of
// nothing.
func (s *Store) forget(header signing.CountersignHeader, payload []byte) (int, error) {
	if s == nil {
		return 0, nil
	}
	if err := s.configured(); err != nil {
		return 0, err
	}
	if err := header.Validate(); err != nil {
		return 0, err
	}
	entries, err := afero.ReadDir(s.fs, s.dir)
	if err != nil {
		// A store directory that does not exist yet holds no decisions, so
		// there is nothing to clear and nothing went wrong. Any other listing
		// failure is reported: it might be hiding the very record the caller
		// asked to remove.
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("countersignature store %s: %w", s.dir, err)
	}
	// Both record-kind suffixes under one index-hash prefix: the signed
	// candidates (which carry an assertion and a key tag between the hash and
	// ".sig") and the single unsigned marker. Matching the prefix rather than
	// reconstructing each filename is what makes the signed sweep signer-blind.
	prefix := indexHash(header, payload) + "."
	unsigned := unsignedFilename(header, payload)
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		match := name == unsigned || (strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".sig"))
		if !match {
			continue
		}
		if err := s.fs.Remove(filepath.Join(s.dir, name)); err != nil {
			return removed, fmt.Errorf("could not remove countersignature record %s: %w", name, err)
		}
		removed++
	}
	return removed, nil
}

// ForgetApprove clears the approval of exactly these bytes, at this ref, in
// this form — the inverse of WriteApprove and WriteUnsignedApprove together.
func (s *Store) ForgetApprove(ref string, form signing.AttestationForm, payload []byte) (int, error) {
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Ref: ref, Form: form}
	return s.forget(h, payload)
}

// ForgetContentReject clears the REF-OMITTED rejection of these bytes in this
// form — the inverse of WriteContentReject. It is addressed by bytes alone,
// exactly as the record was written, so it clears that rejection wherever the
// bytes appear.
func (s *Store) ForgetContentReject(form signing.AttestationForm, payload []byte) (int, error) {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: "", Form: form}
	return s.forget(h, payload)
}

// ForgetRefReject clears the STICKY ref-level block — the inverse of
// WriteRefReject, and the durable half of forgetting a rejection: it is the
// component that survives content changes, so it is the one that must go even
// when the item's current bytes can no longer be resolved.
func (s *Store) ForgetRefReject(ref string) (int, error) {
	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: ref, Form: signing.AttestNone}
	return s.forget(h, nil)
}

// ForgetIndex drops every sidecar-index entry for ref and reports how many
// went.
//
// The index decides nothing (§9.2 — untrusted display metadata), but it is
// what labels a pending item UPDATE rather than NEW and supplies the diff
// base. An item whose decision was cleared has, by construction, no earlier
// decision left to be an update of, so leaving its entries behind would show a
// human "you approved something here once" about a record that no longer
// exists — and offer a diff against bytes nothing now attests to.
//
// It shares AppendIndex's read-modify-write discipline: an unreadable or
// unparseable index REFUSES rather than being rewritten from nothing, and the
// replacement goes through a temp file and a rename. Callers may treat a
// failure as non-fatal — the countersignature removal is the part that
// matters — but must not be told the entries went when they did not.
func (s *Store) ForgetIndex(ref string) (int, error) {
	if s == nil {
		return 0, nil
	}
	if err := s.configured(); err != nil {
		return 0, err
	}
	existing, err := s.readIndex()
	if err != nil {
		return 0, fmt.Errorf("refusing to rewrite the countersignature index: %w", err)
	}
	kept := make([]IndexEntry, 0, len(existing))
	for _, e := range existing {
		if e.Ref != ref {
			kept = append(kept, e)
		}
	}
	dropped := len(existing) - len(kept)
	if dropped == 0 {
		return 0, nil
	}
	if err := s.writeIndex(kept); err != nil {
		return 0, err
	}
	return dropped, nil
}
