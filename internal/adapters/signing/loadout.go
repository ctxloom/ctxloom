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
	// An empty bundle attests to nothing (the same principle applies to Sign
	// itself) — floor it here so every caller of the envelope primitive gets
	// the protection, not just companionloadout.Emit's own separate guard.
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
	decoded, sig, _, err := ParseLoadoutEnvelope(raw)
	if err != nil {
		return nil, "", err
	}
	// VerifyPublisher gates on len(armoredSig)==0, so a nil and an empty
	// slice are indistinguishable to it -- no conditional needed.
	signer, verr := VerifyPublisher(decoded, sig, root, now)
	if verr != nil {
		return nil, "", fmt.Errorf("loadout signature does not verify: %w", verr)
	}
	return decoded, signer, nil
}

// ParseLoadoutEnvelope is the STRUCTURAL half of DecodeLoadoutEnvelope: it
// unwraps the envelope and hands back the bundle bytes, the armored signature
// (empty when unsigned) and the advisory Signer field, WITHOUT verifying
// anything. The advisory signer is returned for diagnostics only — it is a
// value the companion wrote about itself and must never be treated as an
// identity.
//
// It is split out because the two source classes that read this envelope need
// the SAME parse and DIFFERENT postures toward a signature that fails to
// verify. A remote bundle must withhold (an intermediary could have tampered
// with the bytes in transit — that is exactly what the signature is for). A
// COMPANION loadout must not: its bytes arrive directly on the stdout of a
// binary the user consented to execute, with no intermediary in between, so a
// signature that does not verify there is a broken or stale signature in the
// companion's own release — a bug signal, not an attack signal. Reporting it
// is right; dropping the loadout over it is not. See
// config.ProbeCompanionLoadouts.
//
// Structural failures are errors for BOTH callers, and stay that way: an
// envelope that is not valid JSON, carries an unrecognized contract, whose
// bundle is not valid base64, or that decodes to zero bytes has produced no
// content to admit in the first place.
func ParseLoadoutEnvelope(raw []byte) (bundleBytes, armoredSig []byte, advisorySigner string, err error) {
	var env LoadoutEnvelope
	if uerr := json.Unmarshal(raw, &env); uerr != nil {
		return nil, nil, "", fmt.Errorf("parse loadout envelope: %w", uerr)
	}
	if env.Contract != LoadoutContract {
		return nil, nil, "", fmt.Errorf("unrecognized loadout contract %q (this build understands %q)", env.Contract, LoadoutContract)
	}
	decoded, derr := base64.StdEncoding.DecodeString(env.Bundle)
	if derr != nil {
		return nil, nil, "", fmt.Errorf("decode loadout bundle: %w", derr)
	}
	// A well-formed envelope that decodes to ZERO bundle bytes is a
	// malfunctioning (or hostile) companion contributing nothing while still
	// looking like a successfully-decoded, possibly-verified probe. Withhold
	// it the same way an unparseable envelope is withheld, rather than handing
	// the caller (bytes, signer, nil) for an empty payload.
	if len(decoded) == 0 {
		return nil, nil, "", fmt.Errorf("loadout envelope decodes to an empty bundle — a companion contributing nothing must fail loud, not decode successfully")
	}
	return decoded, []byte(env.Signature), env.Signer, nil
}
