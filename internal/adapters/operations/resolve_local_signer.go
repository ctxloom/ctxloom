package operations

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/signing/agentkey"
)

// LocalSigner is ResolveLocalSigner's outcome: the key to sign with, or the
// decision to continue unsigned. Ambiguous carries the identities discovery
// would not choose between, for the frontend to render — this service prints
// nothing.
type LocalSigner struct {
	Signer    ssh.Signer
	Unsigned  bool
	Ambiguous *agentkey.AmbiguousKeyError
}

// NoSigningKeyError is ResolveLocalSigner's refusal of a project-store write
// with no key: an unsigned record in a COMMITTABLE store would be a forgery
// primitive with a friendly name. Cause is discovery's own error; each
// frontend adds the remedy for its surface.
type NoSigningKeyError struct{ Cause error }

func (e *NoSigningKeyError) Error() string { return "no signing key available: " + e.Cause.Error() }
func (e *NoSigningKeyError) Unwrap() error { return e.Cause }

// ResolveLocalSigner is the one signing-key decision `review`, `sign`, the
// trust and blacklist writers and `bundle push` share. explicitKey is the
// caller's merged --key/sign.key value, fed to the discovery chain's
// explicit-key slot exactly as `ctxloom sign` feeds it — the same inputs, not
// merely the same function. A key resolves and signs. Otherwise a PERSONAL
// write degrades to the unsigned path (with the ambiguous candidates carried
// for rendering), and a PROJECT write is refused with *NoSigningKeyError.
func ResolveLocalSigner(ctx context.Context, discoverer *agentkey.Discoverer, explicitKey string, project bool) (LocalSigner, error) {
	discovered, agentErr := discoverer.Discover(ctx, explicitKey)
	if agentErr == nil {
		return LocalSigner{Signer: discovered.Signer}, nil
	}
	if project {
		return LocalSigner{}, &NoSigningKeyError{Cause: agentErr}
	}
	out := LocalSigner{Unsigned: true}
	// A *agentkey.NoKeyError (or any other discovery failure) degrades the
	// same way; only the ambiguous case has candidates worth showing.
	errors.As(agentErr, &out.Ambiguous)
	return out, nil
}

// remedyf wraps a NoSigningKeyError's cause in the caller's remedy prose,
// keeping the typed refusal in the chain.
func remedyf(refused *NoSigningKeyError, format string) error {
	return fmt.Errorf(format, refused.Cause)
}
