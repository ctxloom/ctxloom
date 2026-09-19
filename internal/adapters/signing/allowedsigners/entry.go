package allowedsigners

import (
	"slices"
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
	Principals []string `json:"principals"`

	// CertAuthority is true when the entry carries the cert-authority
	// option. This package recognizes the option but implements no SSH
	// certificate verification — this package is only ever handed plain
	// public keys (per PROTOCOL.sshsig; there is no certificate to
	// validate against a CA). A cert-authority entry therefore NEVER
	// grants trust through Store.TrustedForNamespace / Store.TrustedAs.
	// This is a deliberate, tested, fail-closed decision, not an
	// oversight — see the package doc.
	CertAuthority bool `json:"cert_authority"`

	// Namespaces is the entry's namespaces pattern-list.
	//
	//   - nil means the namespaces= option was absent from the entry.
	//     OpenSSH defines this as "accepted for all namespaces" — verified
	//     against real ssh-keygen, not a shortcut this package takes.
	//   - a non-nil slice means the option was present. An empty quoted
	//     value (namespaces="") is syntactically legal and produces a
	//     non-nil empty slice, which matches no namespace at all (also
	//     verified against real ssh-keygen).
	Namespaces []string `json:"namespaces"`

	// ValidAfter / ValidBefore implement the valid-after= / valid-before=
	// options. nil means the corresponding bound is absent (unbounded).
	ValidAfter  *time.Time `json:"valid_after"`
	ValidBefore *time.Time `json:"valid_before"`

	// KeyType is the SSH key type string as it appears in the file, e.g.
	// "ssh-ed25519", "ssh-rsa", "sk-ssh-ed25519@openssh.com".
	KeyType string `json:"key_type"`

	// PublicKey is the parsed key blob.
	PublicKey ssh.PublicKey `json:"-"`

	// Comment is the trailing free-text comment, if any. Never load-bearing
	// for any trust decision.
	Comment string `json:"comment"`

	// Line is the 1-based index of the line this entry was parsed from,
	// within the exact byte stream handed to Parse.
	//
	// It is LOAD-BEARING, not merely diagnostic. operations.RemoveSigner
	// re-parses the file it just read and then deletes the physical lines the
	// surviving entries name, so this number decides which bytes of the trust
	// root disappear. A number that is off by one revokes the wrong signer and
	// leaves the intended one trusted, and nothing downstream can detect that:
	// both outcomes are a well-formed file.
	//
	// The invariant that makes it usable that way, and that
	// TestParse_LineNumbersIndexTheSourceStream pins: EVERY line of the input
	// is counted — blank lines, comments, and lines that produced a ParseError
	// included — so Line is an index into the caller's own lines, never a
	// count of entries.
	Line int `json:"line"`
}

// clone returns a copy sharing nothing writable with e.
//
// An Entry is a value, but four of its fields are not: Principals and
// Namespaces are slices, and ValidAfter/ValidBefore are pointers. A plain
// assignment therefore leaves the copy holding the ORIGINAL's grant, so
// mutating the copy re-decides trust for whoever holds the original — the
// widening Store's accessors exist to prevent.
//
// PublicKey is deliberately not cloned: ssh.PublicKey is an interface over an
// immutable marshalled blob, there is no general way to deep-copy one, and
// keysEqual compares Marshal() bytes rather than identity.
func (e Entry) clone() Entry {
	out := e
	out.Principals = slices.Clone(e.Principals)
	out.Namespaces = slices.Clone(e.Namespaces)
	if e.ValidAfter != nil {
		t := *e.ValidAfter
		out.ValidAfter = &t
	}
	if e.ValidBefore != nil {
		t := *e.ValidBefore
		out.ValidBefore = &t
	}
	return out
}

// cloneEntries is clone over a slice, preserving a nil slice as nil.
func cloneEntries(entries []Entry) []Entry {
	if entries == nil {
		return nil
	}
	out := make([]Entry, len(entries))
	for i, e := range entries {
		out[i] = e.clone()
	}
	return out
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
