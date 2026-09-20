package composite

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// attestedPackage is a package with every field populated and an
// attestation, the way only Assemble makes one: the codec must carry all of
// it, the attestation included, or a decoded package proves less than the
// assembled one did.
func attestedPackage() Package {
	return Package{
		Context:   Context{Text: "# ctx\nbody", Hash: "h1"},
		Fragments: []Item[Fragment]{{Value: Fragment{Name: "b/f", Body: "body"}, Ref: "b/f", Form: bundles.ContentForm("dir"), Decision: trust.Decision("approved"), Signer: "s"}},
		Premised:  []Item[Fragment]{{Value: Fragment{Name: "b/p", Body: "p", Premise: "when x"}, Ref: "b/p"}},
		Commands: []Item[Command]{{Value: Command{Name: "c", Bundle: "b", Item: "c", ExportName: "b-c", Tags: []string{"t"}, Description: "d", Body: "cmd",
			Exports: map[string][]byte{"fixture": []byte(`{"enabled":true}`)}, Curated: true}, Ref: "b/c"}},
		Skills: []Item[Skill]{{Value: Skill{Name: "s", Bundle: "b", Item: "s", Description: "sd",
			Files: []engine.SkillFile{{Path: "SKILL.md", Digest: "d", Size: 3, Mode: 0o644, Bytes: []byte("abc")}}}, Ref: "b/s"}},
		Hooks:      wire.HooksConfig{Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "echo hi", Type: "command"}}}},
		MCP:        map[string]wire.MCPServer{"ctxloom": {Command: "ctxloom", Args: []string{"mcp"}, Env: map[string]string{"K": "v"}}},
		Links:      []LinkGroup{{Server: "ctxloom", Members: []trust.Ref{{Bundle: "b", Kind: trust.ItemKind("command"), Name: "c"}}}},
		DenyTools:  []string{"Task"},
		Statusline: true,
		Selection:  Selection{Profiles: []string{"base"}, LLM: "primary", Preference: map[string]string{"context": "file"}},
		Loaded:     []string{"b/f"},
		Findings:   []Finding{{Kind: FindingDuplicate, Ref: "b/f", Message: "dup"}},
		attestation: Attestation{
			Items:    []ItemAttestation{{Ref: "b/f", Decision: trust.Decision("approved"), Hash: "h"}},
			Withheld: []string{"b/w"},
		},
	}
}

// TestEncode_Decode_RoundTripsThePackageWithItsAttestation: Decode is the
// only constructor besides Assemble, and what it constructs is the package
// Encode was given — attestation included — once the bytes prove the digest.
func TestEncode_Decode_RoundTripsThePackageWithItsAttestation(t *testing.T) {
	pkg := attestedPackage()
	enc, err := Encode(pkg)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(enc.Bytes), enc.Digest, "the digest is of the encoded bytes")

	back, err := Decode(enc)
	require.NoError(t, err)
	require.Equal(t, pkg, back)
	require.Equal(t, pkg.Attestation(), back.Attestation(), "the decoded package proves what the assembled one did")
}

// TestDecode_RefusesBytesThatDoNotHashToTheirDigest: one altered byte is a
// different package; Decode refuses rather than deliver it.
func TestDecode_RefusesBytesThatDoNotHashToTheirDigest(t *testing.T) {
	enc, err := Encode(attestedPackage())
	require.NoError(t, err)
	enc.Bytes[len(enc.Bytes)/2] ^= 0x01
	_, err = Decode(enc)
	require.ErrorIs(t, err, ErrDigestMismatch)
}

// TestInline_CarriesUnderMax_RefusesAbove: the inline adapter is bounded by
// its Max and redeems only its own shape.
func TestInline_CarriesUnderMax_RefusesAbove(t *testing.T) {
	enc, err := Encode(attestedPackage())
	require.NoError(t, err)
	ctx := context.Background()

	c, err := Inline{Max: len(enc.Bytes)}.Carry(ctx, enc)
	require.NoError(t, err)
	require.Equal(t, enc.Bytes, c.Inline)
	require.Nil(t, c.Claim)
	require.Equal(t, enc.Digest, c.Digest)

	_, err = Inline{Max: len(enc.Bytes) - 1}.Carry(ctx, enc)
	require.ErrorIs(t, err, ErrTooLargeForInline)

	_, err = Inline{}.Redeem(ctx, Carrier{Claim: &Claim{Location: "x"}, Digest: enc.Digest})
	require.ErrorIs(t, err, ErrWrongCarrier, "an inline transport cannot redeem a claim")
	_, err = ClaimCheck{Store: memStore{}}.Redeem(ctx, c)
	require.ErrorIs(t, err, ErrWrongCarrier, "a claim check cannot redeem inline bytes")
}

// TestClaimCheck_StowsAndRedeems_ATamperedClaimIsRefusedNamingTheClaim: the
// claim check stows the bytes content-addressed and redeems the same bytes;
// a claim whose stored bytes were altered by one byte is refused, and the
// refusal names the claim so the operator can find what was touched.
func TestClaimCheck_StowsAndRedeems_ATamperedClaimIsRefusedNamingTheClaim(t *testing.T) {
	pkg := attestedPackage()
	enc, err := Encode(pkg)
	require.NoError(t, err)
	ctx := context.Background()
	store := memStore{}
	claim := ClaimCheck{Store: store}

	c, err := claim.Carry(ctx, enc)
	require.NoError(t, err)
	require.Nil(t, c.Inline)
	require.NotNil(t, c.Claim)
	require.Equal(t, int64(len(enc.Bytes)), c.Claim.Size)
	require.Equal(t, enc.Digest, c.Digest)

	back, err := Open(ctx, Inline{}, claim, c)
	require.NoError(t, err)
	require.Equal(t, pkg, back)

	tampered := append([]byte(nil), enc.Bytes...)
	tampered[0] ^= 0x01
	store[c.Claim.Location] = tampered
	_, err = Open(ctx, Inline{}, claim, c)
	require.ErrorIs(t, err, ErrDigestMismatch)
	require.ErrorContains(t, err, c.Claim.Location, "the refusal names the claim")

	delete(store, c.Claim.Location)
	_, err = Open(ctx, Inline{}, claim, c)
	require.ErrorIs(t, err, ErrClaimMissing)
}

// TestOpen_RedeemsByTheCarriersShape: the consumer's conditional mirrors
// Resolve's — a claim present names ClaimCheck, otherwise Inline — and
// neither side is told which answered.
func TestOpen_RedeemsByTheCarriersShape(t *testing.T) {
	pkg := attestedPackage()
	enc, err := Encode(pkg)
	require.NoError(t, err)
	ctx := context.Background()
	inline, claim := Inline{Max: 1 << 20}, ClaimCheck{Store: memStore{}}

	viaInline, err := inline.Carry(ctx, enc)
	require.NoError(t, err)
	viaClaim, err := claim.Carry(ctx, enc)
	require.NoError(t, err)
	for _, c := range []Carrier{viaInline, viaClaim} {
		back, err := Open(ctx, inline, claim, c)
		require.NoError(t, err)
		require.Equal(t, pkg, back)
	}
}

type memStore map[string][]byte

func (m memStore) Put(_ context.Context, digest [32]byte, b []byte) (string, error) {
	m[string(digest[:])] = b
	return string(digest[:]), nil
}
func (m memStore) Get(_ context.Context, loc string) ([]byte, error) { return m[loc], nil }
