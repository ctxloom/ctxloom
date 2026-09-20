package trust

import (
	"time"

	"golang.org/x/crypto/ssh"
)

// ContentForm identifies which materialization of an item's content was hashed
// or served: the raw authored bytes, or the distilled rewrite. A review record
// binds {payload, form} together so an approval of the raw form can never
// validate a distilled exposure, and vice-versa — which is why the form is part
// of the ReviewRecords question and lives here beside it.
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

// The three PORTS trust is decided with. They are declared here, at the leaf,
// so every reader of a signature and the gate that decides exposure name the
// same interface: the allowed_signers adapter implements TrustRoot, the
// countersign adapter implements ReviewRecords, the lockfile implements
// RetractionRecords, and config.Sources.TrustPorts builds all three for a
// generation.
type (
	// TrustRoot says which keys may publish in which namespace, right now. It
	// is the only policy question signature verification asks, and taking the
	// interface (never a concrete store) is what keeps the namespace check a
	// mandatory input to verification rather than an afterthought a caller
	// can forget.
	TrustRoot interface {
		TrustedForNamespace(key ssh.PublicKey, ns string, now time.Time) SignerDecision
	}
	// ReviewRecords is what a human decided: a rejection covering the ref OR
	// exactly these bytes (both scopes must be honoured, for every item), and
	// an approval of exactly these bytes at this ref in this layout form.
	ReviewRecords interface {
		Rejected(ref Ref, payload []byte) bool
		Approved(ref Ref, payload []byte, form ContentForm) bool
	}
	// RetractionRecords is the LOCAL record of publisher retractions, written
	// at pull time and read here, so the decision never touches the network.
	RetractionRecords interface {
		Retracted(ref Ref) (retracted bool, reason string)
	}
)

// Faulted is the OPTIONAL capability a records port exposes when its backing
// store could not be read. The gate checks it by type assertion, never as a
// port method, so a closure-shaped test fake stays two lines and a real
// disk-backed adapter fails CLOSED: "could not evaluate" never means "allow".
// A faulted ReviewRecords withholds every item; a faulted RetractionRecords
// withholds every item a retraction could cover.
type Faulted interface {
	Fault() error
}
