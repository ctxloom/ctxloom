package signing

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// LoadoutContract is the ONLY companion-loadout envelope contract version
// this build understands (signature-envelope spec §4.3, §12). A verifier
// MUST reject any other contract string outright — never best-effort parse
// an unknown version (spec §12's contract-versioning rule).
const LoadoutContract = "ctxloom-loadout/1"

// LoadoutEnvelope is the JSON object a companion binary emits on
// `<bin> loadout --format json` (spec §4.3). It is the SAME shape recommended
// for an org-published loadout delivered over a registry/object-store/MDM
// channel (spec §4.4), since it keeps the (content bytes, detached signature)
// pair together in one transportable object — the artifact is transport-
// agnostic (spec §3.0) whether it arrives on a companion's stdout or from an
// MDM push.
type LoadoutEnvelope struct {
	// Contract MUST equal LoadoutContract, matched exactly (spec §12: a
	// version is an identity, not a constraint).
	Contract string `json:"contract"`

	// Bundle is the exact bundle YAML bytes, base64 (standard, padded) ONLY
	// to survive JSON transport. The SIGNED payload — and the bytes ctxloom
	// parses — are the DECODED bytes, verbatim (identical in kind to the
	// publisher-payload rule, spec §3.1): no re-serialization happens
	// anywhere between decode and use (implementer trap "re-serializing
	// between verify and use").
	Bundle string `json:"bundle"`

	// Signature is an armored PROTOCOL.sshsig blob over the DECODED bundle
	// bytes, under namespace NamespacePublish. Empty means unsigned — legal,
	// ordinary, and takes the review path (spec §10.1). Produced at companion
	// BUILD time and embedded in the binary; the companion holds no private
	// key at runtime (spec §4.3).
	Signature string `json:"signature,omitempty"`

	// Signer is ADVISORY ONLY — a hint for error messages, never trusted. The
	// verified identity NEVER comes from this field; it comes from resolving
	// the signature's key against the caller's TrustRoot (implementer trap
	// #3: "Trusting the signer field in the envelope instead of
	// allowed_signers"). An envelope naming a signer that is not in the trust
	// root is unsigned content to the verifier, full stop.
	Signer string `json:"signer,omitempty"`
}

// EncodeLoadoutEnvelope builds the JSON bytes a companion's `loadout
// --format json` command writes to stdout. armoredSig may be nil/empty to
// emit unsigned (the common case for a companion with no build-time signing
// pipeline yet — this is legal and takes the review path, spec §10.1); when
// non-empty it is carried alongside signer (advisory only — see
// LoadoutEnvelope.Signer) so the shape supports a companion attaching a
// signature produced at build time without any change to this function.
func EncodeLoadoutEnvelope(bundleBytes []byte, armoredSig []byte, signer string) ([]byte, error) {
	// U134-F03: an empty bundle attests to nothing (the same principle F01
	// applies to Sign itself) — floor it here so every caller of the envelope
	// primitive gets the protection, not just companionloadout.Emit's own
	// separate guard.
	if len(bundleBytes) == 0 {
		return nil, fmt.Errorf("encode loadout envelope: refusing to encode an empty bundle — a loadout contributing nothing must fail loud, not look like a healthy envelope")
	}
	env := LoadoutEnvelope{
		Contract: LoadoutContract,
		Bundle:   base64.StdEncoding.EncodeToString(bundleBytes),
	}
	if len(armoredSig) > 0 {
		env.Signature = string(armoredSig)
		env.Signer = signer
	}
	out, err := json.MarshalIndent(&env, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode loadout envelope: %w", err)
	}
	return out, nil
}

// DecodeLoadoutEnvelope parses raw envelope bytes (as read from a companion's
// stdout, or any other channel carrying the same JSON shape — spec §4.4) and
// resolves the loadout's verified publisher, exactly mirroring the remote-
// bundle verification path (config.verifyBundlePublisher): the KEY comes from
// the signature, TRUST in that key comes only from root, and the advisory
// Signer field is never consulted for anything but error messages.
//
// Three outcomes, matching VerifyPublisher's own contract:
//
//   - (bytes, "", nil)       UNSIGNED TO THE CALLER — no signature, or one by
//     a key root does not trust to publish. Ordinary; the review path
//     handles it (spec §10.1). This is NOT an error.
//   - (bytes, principal, nil) VERIFIED — a key root trusts for the publish
//     namespace signed exactly these bytes.
//   - (nil, "", err)          WITHHELD — the envelope is not valid JSON, the
//     contract string is unrecognized, "bundle" is not valid base64, or a
//     TRUSTED key's signature does not actually cover these bytes (tamper,
//     spec §10.2). The caller must withhold the loadout entirely — never
//     degrade a parse/tamper failure to "unsigned, please review" (that would
//     let corrupting the envelope silently downgrade or forge trust), and
//     never crash: a hostile or buggy companion binary must not be able to
//     take ctxloom down by printing garbage.
func DecodeLoadoutEnvelope(raw []byte, root TrustRoot, now time.Time) (bundleBytes []byte, verifiedSigner string, err error) {
	var env LoadoutEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "", fmt.Errorf("parse loadout envelope: %w", err)
	}
	if env.Contract != LoadoutContract {
		return nil, "", fmt.Errorf("unrecognized loadout contract %q (this build understands %q)", env.Contract, LoadoutContract)
	}
	decoded, err := base64.StdEncoding.DecodeString(env.Bundle)
	if err != nil {
		return nil, "", fmt.Errorf("decode loadout bundle: %w", err)
	}
	// U134-F03: a well-formed envelope that decodes to ZERO bundle bytes is a
	// malfunctioning (or hostile) companion contributing nothing while still
	// looking like a successfully-decoded, possibly-verified probe. Withhold
	// it the same way an unparseable envelope is withheld, rather than handing
	// the caller (bytes, signer, nil) for an empty payload.
	if len(decoded) == 0 {
		return nil, "", fmt.Errorf("loadout envelope decodes to an empty bundle — a companion contributing nothing must fail loud, not decode successfully")
	}

	// VerifyPublisher gates on len(armoredSig)==0, so a nil and an empty
	// slice are indistinguishable to it -- no conditional needed.
	sig := []byte(env.Signature)
	signer, verr := VerifyPublisher(decoded, sig, root, now)
	if verr != nil {
		return nil, "", fmt.Errorf("loadout signature does not verify: %w", verr)
	}
	return decoded, signer, nil
}
