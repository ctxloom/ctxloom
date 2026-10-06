package trust

import (
	"time"

	"golang.org/x/crypto/ssh"
)

// ContentForm identifies which materialization of an item's content was hashed
// or served: the raw authored bytes, or the distilled rewrite.
type ContentForm string

const (
	FormRaw       ContentForm = "raw"
	FormDistilled ContentForm = "distilled"
)

// SignerDecision is what a TrustRoot says about one key in one namespace. It
// is core-owned so the allowed_signers adapter returns it and nothing in core
// imports a signing package to read the answer.
type SignerDecision struct {
	// Trusted is whether the key may sign in the namespace, now.
	Trusted bool
	// Principal is the identity the granting entry names; empty when untrusted.
	// It is resolved from the trust root, never read from the artifact's own
	// advisory field (signature-envelope spec §4.3).
	Principal string
	// Reason is display-only: why the key was refused, when it was.
	Reason string
}

// The ports a signature and a retraction record are read through. They are
// declared here, at the leaf, so nothing in core imports an adapter to read
// the answer: the allowed_signers adapter implements TrustRoot, and the
// lockfile implements RetractionRecords.
type (
	// TrustRoot says which keys may publish in which namespace, right now. It
	// is the only policy question signature verification asks, and taking the
	// interface (never a concrete store) is what keeps the namespace check a
	// mandatory input to verification rather than an afterthought a caller
	// can forget.
	TrustRoot interface {
		TrustedForNamespace(key ssh.PublicKey, ns string, now time.Time) SignerDecision
	}
	// RetractionRecords is the LOCAL record of publisher retractions, written
	// at pull time. It is asked with the parsed BundleRef because a pull records the
	// retraction under the bundle's identity, and any other rendering of the
	// ref is a second spelling that can miss it.
	RetractionRecords interface {
		Retracted(ref BundleRef) (retracted bool, reason string)
	}
)

// NoSigners is the TrustRoot that trusts no key in any namespace. It is the
// root of a Config no generation bound — a fixture, or a configuration that
// failed to load — so a surface that verifies a signature
// itself still gets an answer, and the answer is "untrusted", never a nil to
// dereference.
type NoSigners struct{}

// NoSignersReason is the refusal NoSigners gives every key.
const NoSignersReason = "no trust root: the configuration was not loaded, so no signer is trusted"

// TrustedForNamespace implements TrustRoot: never trusted.
func (NoSigners) TrustedForNamespace(ssh.PublicKey, string, time.Time) SignerDecision {
	return SignerDecision{Reason: NoSignersReason}
}

// Faulted is the OPTIONAL capability a records port exposes when its backing
// store could not be read, checked by type assertion rather than as a port
// method.
type Faulted interface {
	Fault() error
}
