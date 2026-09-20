package composite

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
)

// ---- The wire form: encode, then carry ----
//
// ONE codec, consumed identically by the runner (over StartRun) and by the
// local launcher (in-process): both redeem a Carrier to an Encoded and
// Decode it. Decode verifies the digest, so a Package that came through a
// store is the same bytes Resolve encoded. There is no seal and no MAC: the
// frame that carries a Carrier is authenticated by the run credential on the
// runner channel, and if a stronger proof is ever needed it layers on as
// authentication above this codec, not inside it.
//
// SIZE is the CLAIM CHECK pattern behind a polymorphic port: Transport has
// two adapters. Inline puts the bytes in the frame; ClaimCheck stows them in
// a Store both sides can reach and puts a Claim in the frame. Resolve encodes
// the package, MEASURES it, and carries with Inline under InlineMax or with
// ClaimCheck above it — a conditional in Resolve, not a third adapter. The
// consumer redeems with the adapter the carrier's SHAPE names (a claim
// present or not); no arm anywhere branches on the runtime axis, and a
// container runner and a host runner hold the same two adapters.
//
// The frame is bounded explicitly: the gRPC server sets MaxRecvMsgSize to
// InlineMax plus frame headroom, and Inline.Carry refuses above Max.

// Encoded is the canonical encoding of a Package (skill file bytes included)
// and its digest.
type Encoded struct {
	Bytes  []byte
	Digest [32]byte
}

var (
	ErrDigestMismatch = errors.New("composite: encoded bytes do not hash to their digest")
	ErrWrongCarrier   = errors.New("composite: this transport cannot redeem a carrier of that shape")
	ErrClaimMissing   = errors.New("composite: the claimed bytes are not at the stated location")
)

// envelope is the encoded form: the package's exported fields beside the
// attestation only Assemble writes, so a decoded package proves what the
// assembled one did.
type envelope struct {
	Package     Package     `json:"package"`
	Attestation Attestation `json:"attestation"`
}

// Encode is the one encoder; only Assemble-made Packages reach it.
func Encode(pkg Package) (Encoded, error) {
	b, err := json.Marshal(envelope{Package: pkg, Attestation: pkg.attestation})
	if err != nil {
		return Encoded{}, fmt.Errorf("composite: encode package: %w", err)
	}
	return Encoded{Bytes: b, Digest: sha256.Sum256(b)}, nil
}

// Decode is the ONLY constructor of a Package besides Assemble. It verifies
// the digest and returns the Package with the attestation the encoder wrote.
func Decode(e Encoded) (Package, error) {
	if sha256.Sum256(e.Bytes) != e.Digest {
		return Package{}, ErrDigestMismatch
	}
	var env envelope
	if err := json.Unmarshal(e.Bytes, &env); err != nil {
		return Package{}, fmt.Errorf("composite: decode package: %w", err)
	}
	pkg := env.Package
	pkg.attestation = env.Attestation
	return pkg, nil
}

// Carrier is what rides StartRun.launch. Exactly one of Inline or Claim is
// set; Digest always is.
type Carrier struct {
	Inline []byte
	Claim  *Claim
	Digest [32]byte
}

// Claim is the check: where the bytes were stowed. Location is a
// store-relative name, never a host path.
type Claim struct {
	Location string
	Size     int64
}

// Transport is the package-transport PORT. Carry prepares the wire form of
// an encoded package; Redeem recovers it from a carrier of its own shape.
type Transport interface {
	Carry(ctx context.Context, e Encoded) (Carrier, error)
	Redeem(ctx context.Context, c Carrier) (Encoded, error)
}

// Inline is adapter 1: the bytes ride the frame. Carry refuses above Max.
type Inline struct{ Max int }

var ErrTooLargeForInline = errors.New("composite: encoded package exceeds the inline ceiling")

func (t Inline) Carry(_ context.Context, e Encoded) (Carrier, error) {
	if t.Max > 0 && len(e.Bytes) > t.Max {
		return Carrier{}, ErrTooLargeForInline
	}
	return Carrier{Inline: e.Bytes, Digest: e.Digest}, nil
}

func (t Inline) Redeem(_ context.Context, c Carrier) (Encoded, error) {
	if c.Claim != nil || c.Inline == nil {
		return Encoded{}, ErrWrongCarrier
	}
	return Encoded{Bytes: c.Inline, Digest: c.Digest}, nil
}

// ClaimCheck is adapter 2: the bytes are stowed in a Store both sides can
// reach and a Claim rides the frame. The Store is content-addressed (the
// location names the digest), and Decode verifies what was fetched.
type ClaimCheck struct{ Store Store }

// Store is the stow/fetch port under ClaimCheck. Its default implementation
// is the SESSION DIR (<harp>/persist/package/<digest>): on the host's
// filesystem for a host runner, and inside the session-state mount for a
// container runner. Get answers nil bytes for a location it does not hold.
type Store interface {
	Put(ctx context.Context, digest [32]byte, bytes []byte) (location string, err error)
	Get(ctx context.Context, location string) ([]byte, error)
}

func (t ClaimCheck) Carry(ctx context.Context, e Encoded) (Carrier, error) {
	loc, err := t.Store.Put(ctx, e.Digest, e.Bytes)
	if err != nil {
		return Carrier{}, err
	}
	return Carrier{Claim: &Claim{Location: loc, Size: int64(len(e.Bytes))}, Digest: e.Digest}, nil
}

func (t ClaimCheck) Redeem(ctx context.Context, c Carrier) (Encoded, error) {
	if c.Claim == nil {
		return Encoded{}, ErrWrongCarrier
	}
	b, err := t.Store.Get(ctx, c.Claim.Location)
	if err != nil {
		return Encoded{}, err
	}
	if b == nil {
		return Encoded{}, fmt.Errorf("%w: claim %s", ErrClaimMissing, c.Claim.Location)
	}
	return Encoded{Bytes: b, Digest: c.Digest}, nil
}

// Redeem is the consumer's shape conditional, the mirror of Resolve's size
// conditional: a claim present names the claim check, otherwise the inline
// transport. Written once for every consumer — the runner and the local
// launcher — so neither learns which transport answered.
func Redeem(ctx context.Context, inline, claim Transport, c Carrier) (Encoded, error) {
	if c.Claim != nil {
		return claim.Redeem(ctx, c)
	}
	return inline.Redeem(ctx, c)
}

// Open redeems and decodes: the whole consumer side of the codec. A claim
// whose bytes no longer hash to the digest is refused naming the claim, so
// the operator can find what was altered in the store.
func Open(ctx context.Context, inline, claim Transport, c Carrier) (Package, error) {
	enc, err := Redeem(ctx, inline, claim, c)
	if err != nil {
		return Package{}, err
	}
	pkg, err := Decode(enc)
	if err != nil {
		if c.Claim != nil {
			return Package{}, fmt.Errorf("%w: claim %s", err, c.Claim.Location)
		}
		return Package{}, err
	}
	return pkg, nil
}

// DefaultInlineMax leaves headroom under the gRPC frame ceiling the server
// is configured with (MaxRecvMsgSize = DefaultInlineMax + 1 MiB).
const DefaultInlineMax = 2 << 20
