package signing

import (
	"bytes"
	"fmt"

	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

// defaultHashAlgorithm is the hash algorithm ctxloom uses when it signs.
// sha512 matches OpenSSH's own `ssh-keygen -Y sign` default, which is what
// makes our signatures and OpenSSH's interoperate (see interop_test.go).
//
// U134-F12: this was exported as DefaultHashAlgorithm but had zero references
// anywhere in the repo outside its own use below — unexported since nothing
// reads it as package API.
const defaultHashAlgorithm = sshsig.HashSHA512

// Sign signs payload under namespace using signer, and returns an armored
// (PEM "-----BEGIN SSH SIGNATURE-----") PROTOCOL.sshsig blob — the same wire
// format `ssh-keygen -Y sign -n <namespace>` produces.
//
// signer may be backed by an on-disk key (ssh.NewSignerFromKey /
// ssh.NewSignerFromSigner) or by an ssh-agent client (golang.org/x/crypto/
// ssh/agent's Client already implements ssh.Signer — see agent/client.go
// ExtendedAgent/SignWithFlags), so signing via ssh-agent needs no additional
// dependency (spec §9.1, §11A).
//
// namespace must be non-empty: it is the assertion's domain separator (spec
// §1) and is what keeps a publish signature from being replayable as an
// approve signature or vice versa.
func Sign(payload []byte, signer ssh.Signer, namespace string) ([]byte, error) {
	// U134-F01: sshsig.Sign validates only that the namespace is non-empty and
	// will happily hash an empty reader, producing a valid armored signature
	// over the empty string — one blob that verifies against EVERY zero-byte
	// payload, for every consumer, forever. A signature over nothing attests to
	// nothing, so the floor lives in the primitive where it covers every caller
	// (publish, skills, bundles) at once.
	if len(payload) == 0 {
		return nil, fmt.Errorf("signing: refusing to sign an empty payload — a signature over zero bytes attests to nothing")
	}
	sig, err := sshsig.Sign(bytes.NewReader(payload), signer, defaultHashAlgorithm, namespace)
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}
	return sshsig.Armor(sig), nil
}

// Verify checks that armored is a valid PROTOCOL.sshsig signature over
// payload, under namespace, by pub. It returns nil only if all three match:
// the signature cryptographically verifies AND its embedded namespace equals
// namespace AND its embedded public key matches pub.
//
// Verify is pure Go, offline, and in-process: it does not shell out to
// ssh-keygen and performs no network access, so it is safe to run inside a
// minimal agent container that has neither (spec §11A.2). This function
// answers only "is this signature cryptographically valid over these bytes
// in this namespace" — whether pub is a key anyone should TRUST for that
// namespace is a policy question answered by the allowed_signers store
// (owned by a different slice; deliberately not this package's concern).
func Verify(payload []byte, armored []byte, pub ssh.PublicKey, namespace string) error {
	sig, err := sshsig.Unarmor(armored)
	if err != nil {
		return fmt.Errorf("verify: unarmor: %w", err)
	}
	// Verify hashes payload using the algorithm embedded in the signature
	// itself (rather than a hardcoded defaultHashAlgorithm), so a signature
	// produced with a different supported algorithm — e.g. a real
	// `ssh-keygen -Y sign` invocation with -O hashalg=sha256 — still
	// verifies correctly.
	if err := sshsig.Verify(bytes.NewReader(payload), sig, pub, sig.HashAlgorithm, namespace); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	return nil
}

// ParseArmored reports whether armored is a well-formed PROTOCOL.sshsig blob
// — nothing about who signed it or what it covers, only that the bytes on
// disk are a signature at all.
//
// It exists because "this file will not even parse" and "there is no such
// file" are different facts that the VERIFY path deliberately conflates:
// VerifyCountersignature collapses every failure into (false, "") because on
// the APPROVE path a signature that does not prove anything is correctly
// treated as absent. On the REJECT path that collapse is a fail-OPEN — an
// unparseable rejection record silently un-rejects the item — so the caller
// that owns the fail-closed gate (countersign.Store.Readable) needs a way to
// ask this narrower question about every record it can see, ahead of any
// query-scoped verification.
func ParseArmored(armored []byte) error {
	if len(armored) == 0 {
		return fmt.Errorf("empty signature blob")
	}
	if _, err := sshsig.Unarmor(armored); err != nil {
		return fmt.Errorf("unarmor: %w", err)
	}
	return nil
}
