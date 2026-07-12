package signing

import (
	"errors"
	"fmt"
	"time"

	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// The three assertion namespaces (spec §1). A namespace is the domain
// separator that keeps one assertion from being replayed as another: a
// publish signature cannot be presented as an approval, and vice versa.
// They are matched byte-for-byte — a version is an identity, not a
// constraint (spec §12).
const (
	// NamespacePublish: "I, key K, published these bytes" — authentication,
	// made by the author of the content over the raw bundle FILE bytes.
	NamespacePublish = "publish.v1.ctxloom.dev"
	// NamespaceApprove: "I reviewed these exact bytes and allow them to
	// reach my agent" — authorization, made by the human running ctxloom.
	NamespaceApprove = "approve.v1.ctxloom.dev"
	// NamespaceReject: "I refuse these exact bytes / this ref, permanently."
	NamespaceReject = "reject.v1.ctxloom.dev"
)

// ErrSignatureTampered reports the one publisher-verification outcome that is
// never benign: a signature that is present but does not honestly cover the
// content it claims to (spec §10.2). It is raised for a structurally invalid
// blob AND for a signature by a TRUSTED key that does not verify over these
// bytes. Both mean "the bytes do not match the signature that claims to cover
// them", and callers must withhold the content entirely rather than degrade it
// to unsigned — otherwise an attacker downgrades a signed bundle to an
// unsigned one simply by corrupting its signature.
//
// It is deliberately NOT raised for a signature by a key the verifier does not
// trust: that is ordinary third-party content (see VerifyPublisher).
var ErrSignatureTampered = errors.New("signature does not cover these bytes")

// TrustRoot answers the only policy question publisher verification asks: is
// this key trusted to make an assertion in this namespace, right now?
// *allowedsigners.Store is the production implementation; tests inject a fake.
// Taking the interface (rather than the concrete store) is what keeps the
// namespace/role check — spec trap #3 — a mandatory input to verification
// instead of an optional afterthought a caller can forget.
type TrustRoot interface {
	TrustedForNamespace(key ssh.PublicKey, ns string, now time.Time) allowedsigners.Decision
}

// VerifyPublisher resolves the verified publisher identity of bundleBytes from
// its detached signature, and is the ONLY way a bundle acquires a signer. Its
// three outcomes are the three cases of spec §10.1/§10.2, and they are
// deliberately distinct:
//
//	("", nil)                     UNSIGNED TO YOU. Either armoredSig is empty
//	                              (no .sig at the pinned SHA — the common case
//	                              today), or it is a well-formed signature by a
//	                              key that is NOT in the trust root for the
//	                              publish namespace. Both take the review path.
//	                              This is NOT an error and must stay quiet: a
//	                              scary message for every unknown publisher
//	                              trains users to ignore messages.
//
//	(principal, nil)              VERIFIED. The signature is by a key the trust
//	                              root authorizes for NamespacePublish, and it
//	                              cryptographically covers exactly bundleBytes.
//	                              principal is the matched allowed_signers
//	                              entry's principal — resolved from the trust
//	                              root, NEVER from any advisory "signer" field
//	                              carried by the artifact itself (trap #3).
//
//	("", ErrSignatureTampered)    TAMPER. Withhold the content entirely.
//
// The verification order is load-bearing. The key comes from the signature
// blob, but trust in that key comes only from the trust root, and the bytes are
// only ever verified against a key the trust root already authorized for THIS
// namespace. A valid publish-namespace signature from an approve-only key is
// not a publish authorization, and this function will not report one.
//
// It is pure Go, offline, and in-process: no network, no ssh-keygen binary
// (spec §11A.2), so it runs inside a minimal agent container that has neither.
func VerifyPublisher(bundleBytes, armoredSig []byte, root TrustRoot, now time.Time) (string, error) {
	// No .sig at the pinned SHA. Unsigned content is legal and ordinary
	// (spec §10.1) — the review path handles it.
	if len(armoredSig) == 0 {
		return "", nil
	}
	if root == nil {
		// No trust root trusts no key. Fail toward LESS exposure: treat the
		// signature as unattributable rather than assuming anything about it.
		return "", nil
	}

	// The KEY comes from the signature blob; TRUST in that key comes only from
	// the trust root, below. A blob we cannot even parse is a tamper signal, not
	// an unsigned bundle — otherwise corrupting a .sig would silently downgrade
	// a signed bundle to an unsigned one (spec §10.2).
	sig, err := sshsig.Unarmor(armoredSig)
	if err != nil {
		return "", fmt.Errorf("%w: unarmor: %w", ErrSignatureTampered, err)
	}

	// The namespace/role check comes BEFORE the cryptographic verification, and
	// gates it: we only ever verify bytes against a key the trust root has
	// already authorized for THIS namespace. A key we do not trust for publish —
	// whether unknown entirely, or known but scoped to approve/reject only — makes
	// this unsigned content TO US (spec §10.2). Quiet, no error, review path.
	decision := root.TrustedForNamespace(sig.PublicKey, NamespacePublish, now)
	if !decision.Trusted {
		return "", nil
	}

	// The key is trusted to publish. Now the bytes must actually be the bytes it
	// signed. A trusted key's signature that does not cover these bytes is never
	// benign — it is withheld entirely, never degraded to "unsigned, please
	// review".
	if err := Verify(bundleBytes, armoredSig, sig.PublicKey, NamespacePublish); err != nil {
		return "", fmt.Errorf("%w: signed by %s: %w", ErrSignatureTampered, decision.Principal, err)
	}
	return decision.Principal, nil
}
