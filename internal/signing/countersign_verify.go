package signing

import (
	"time"

	"github.com/hiddeco/sshsig"
)

// namespaceForAssertion maps a countersignature Assertion to its domain
// separator (spec §1): approve and reject are DIFFERENT namespaces so a
// rejection signature can never be replayed as an approval or vice versa.
func namespaceForAssertion(a Assertion) string {
	switch a {
	case AssertionApprove:
		return NamespaceApprove
	case AssertionReject:
		return NamespaceReject
	default:
		return ""
	}
}

// VerifyCountersignature reports whether armored is a valid countersignature
// over header+payloadBytes, made by a key root trusts for the assertion's
// namespace, at time now.
//
// It collapses EVERY failure mode into one quiet (false, "") outcome —
// an empty/unparseable blob, an untrusted key, or a well-formed signature over
// DIFFERENT bytes than header+payloadBytes reconstructs. This is deliberately
// unlike VerifyPublisher, which distinguishes "untrusted key" (quiet) from
// "trusted key, wrong bytes" (loud tamper, ErrSignatureTampered): a bundle's
// publisher signature is the trust basis for LOADING content, so corrupting it
// must not silently downgrade signed-and-tampered to plain-unsigned.
//
// A countersignature DOES carry an asymmetry, and this function is the wrong
// place to resolve it (U137-F03). Approve and reject are the asymmetry: on
// the APPROVE path a record that does not verify degrades toward LESS
// exposure exactly like a deleted one (spec §10.5: deletion of records is
// fail-safe), which is what justifies the collapse here. On the REJECT path
// the same collapse is a fail-OPEN — "not countersigned" is benign, so a
// corrupted-but-openable rejection record silently un-rejects the item.
//
// The collapse stays, because the distinction cannot be drawn from HERE: this
// function only ever sees the candidates whose index hash a particular query
// reconstructed, so it cannot tell "no record exists" from "the record exists
// and is corrupt". That is a whole-store question, and it is answered by
// countersign.Store.Readable, which parses EVERY record in the store and
// fails the caller's fail-closed gate when one will not unarmor. Do not add a
// third outcome here without moving that gate. Callers must never treat
// "candidate file found at the expected index hash" as authority on its own
// (spec §9.3, implementer trap #2) — finding a file only earns it a call here.
func VerifyCountersignature(header CountersignHeader, payloadBytes, armored []byte, root TrustRoot, now time.Time) (principal string, ok bool) {
	if len(armored) == 0 || root == nil {
		return "", false
	}
	sig, err := sshsig.Unarmor(armored)
	if err != nil {
		return "", false
	}
	ns := namespaceForAssertion(header.Assertion)
	if ns == "" {
		return "", false
	}
	decision := root.TrustedForNamespace(sig.PublicKey, ns, now)
	if !decision.Trusted {
		return "", false
	}
	full := CountersignPayload(header, payloadBytes)
	if err := Verify(full, armored, sig.PublicKey, ns); err != nil {
		return "", false
	}
	return decision.Principal, true
}
