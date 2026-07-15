package allowedsigners

import (
	"time"

	"golang.org/x/crypto/ssh"
)

// Entry is one parsed line of an OpenSSH allowed_signers file (see
// ssh-keygen(1), the ALLOWED SIGNERS section). It pairs an identity — a
// principals pattern-list — and a set of options with the public key they
// authorize.
type Entry struct {
	// Principals is the entry's principals pattern-list, exactly as
	// written (e.g. []string{"alice@example.com", "bob@example.com"} or
	// []string{"*@example.com"}). Never empty for a successfully parsed
	// entry.
	Principals []string

	// CertAuthority is true when the entry carries the cert-authority
	// option. This package recognizes the option but implements no SSH
	// certificate verification — this package is only ever handed plain
	// public keys (per PROTOCOL.sshsig; there is no certificate to
	// validate against a CA). A cert-authority entry therefore NEVER
	// grants trust through Store.TrustedForNamespace / Store.TrustedAs.
	// This is a deliberate, tested, fail-closed decision, not an
	// oversight — see the package doc.
	CertAuthority bool

	// Namespaces is the entry's namespaces pattern-list.
	//
	//   - nil means the namespaces= option was absent from the entry.
	//     OpenSSH defines this as "accepted for all namespaces" — verified
	//     against real ssh-keygen, not a shortcut this package takes.
	//   - a non-nil slice means the option was present. An empty quoted
	//     value (namespaces="") is syntactically legal and produces a
	//     non-nil empty slice, which matches no namespace at all (also
	//     verified against real ssh-keygen).
	Namespaces []string

	// ValidAfter / ValidBefore implement the valid-after= / valid-before=
	// options. nil means the corresponding bound is absent (unbounded).
	ValidAfter  *time.Time
	ValidBefore *time.Time

	// KeyType is the SSH key type string as it appears in the file, e.g.
	// "ssh-ed25519", "ssh-rsa", "sk-ssh-ed25519@openssh.com".
	KeyType string

	// PublicKey is the parsed key blob.
	PublicKey ssh.PublicKey

	// Comment is the trailing free-text comment, if any. Never load-bearing
	// for any trust decision.
	Comment string

	// Line is the 1-based source line number this entry was parsed from,
	// for diagnostics.
	Line int
}

// MatchesPrincipal reports whether identity matches this entry's
// Principals pattern-list, per ssh_config(5) PATTERNS (comma-separated
// glob patterns using '*'/'?', with '!'-negation). Matching is
// case-sensitive: principal identities are opaque strings to this
// package, and case-folding an identity comparison is a well-known way to
// silently widen a grant.
func (e Entry) MatchesPrincipal(identity string) bool {
	return matchPatternList(e.Principals, identity)
}

// MatchesAnyPrincipal reports whether any of e's OWN Principals (an exact,
// literal string, never a glob expansion) is present in principals — the
// set-membership counterpart to MatchesPrincipal's single-identity pattern
// match. Shared by config.filterSuppressedPrincipals (TrustRoot()'s
// subtraction of a locally-distrusted embedded principal) and
// operations.entrySuppressed (the `signer list`/`show` Suppressed tag), so
// the "does this entry name a principal in this set" check has one
// implementation instead of two copies drifting apart.
func (e Entry) MatchesAnyPrincipal(principals map[string]bool) bool {
	for _, p := range e.Principals {
		if principals[p] {
			return true
		}
	}
	return false
}

// MatchesNamespace reports whether ns is accepted by this entry's
// Namespaces option. See the Namespaces field doc for the nil-vs-empty
// distinction.
func (e Entry) MatchesNamespace(ns string) bool {
	if e.Namespaces == nil {
		return true
	}
	return matchPatternList(e.Namespaces, ns)
}

// ValidAt reports whether now falls within [ValidAfter, ValidBefore],
// inclusive of both bounds — matching ssh-keygen's own boundary behavior
// (verified: its error text uses strict '<'/'>' comparisons, so the exact
// boundary instant is itself valid). The caller supplies now; this
// package never reads the system clock. See the package doc for why: an
// sshsig signature carries no trusted timestamp (PROTOCOL.sshsig has no
// timestamp field), so "now" can only ever be a verifier-supplied,
// untrusted opinion of the time — exactly ssh-keygen -Y verify
// -Overify-time's own contract. Baking time.Now() into this package would
// misrepresent that as something more authoritative than it is, and would
// make the decision untestable without sleeping.
func (e Entry) ValidAt(now time.Time) bool {
	if e.ValidAfter != nil && now.Before(*e.ValidAfter) {
		return false
	}
	if e.ValidBefore != nil && now.After(*e.ValidBefore) {
		return false
	}
	return true
}
