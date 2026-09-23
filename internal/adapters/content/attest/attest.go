// Package attest is layer 2 of the content access surface: it decides WHO
// attests a bundle's bytes, over the layer-0 store and the layer-1 manifest.
//
// It lives beside package content rather than inside it so that layer 0 never
// imports a trust root. Layer 0 knows where signature bytes live; layer 1 knows
// whether the tree matches its manifest; only this package knows whether any of
// that was said by someone you trust.
//
// # The one attestation
//
// A bundle carries a MANIFEST (path -> hash, plus the signed release header)
// and the publisher signatures filed against it. One signature over the
// manifest attests every file in the tree; there is no per-item signature.
//
// # Only NamespacePublish is ever consulted
//
// Publish is the one assertion that must travel to a consumer who has never
// seen the content, and it is the only one stored in the tree. Approvals and
// rejections live in the countersignature store and are a different question
// asked by a different layer. This package therefore reads signatures under
// NamespacePublish and nothing else, and it verifies them only against keys the
// trust root authorizes for THAT namespace — so an approve-only key can never
// satisfy a publish slot, in either direction (storage or trust).
package attest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/release"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// publishNS is the ONE namespace this package reads or writes. It is a constant
// rather than a parameter on purpose: making it an argument is what would let a
// caller pass NamespaceApprove and turn a review record into a publication.
const publishNS = content.Namespace(signing.NamespacePublish)

// Status is the outcome of verifying one bundle or one item form.
type Status string

const (
	// StatusUnattested: nothing here was said by a key this trust root
	// authorizes to publish. Ordinary and quiet — an unsigned bundle, or one
	// signed by a publisher you have not trusted yet, takes the review path.
	StatusUnattested Status = "unattested"
	// StatusManifestSigned: covered by a bundle manifest that a trusted
	// publisher signed, with bytes that match.
	StatusManifestSigned Status = "manifest-signed"
	// StatusTampered: an attestation is present and does NOT honestly cover
	// what it claims — a signed manifest contradicted by the bytes, a manifest
	// signed as another bundle, or a signature blob that is not one. Withhold
	// the content; never degrade this to unsigned, or corrupting a signature
	// would downgrade a signed bundle to an unsigned one.
	StatusTampered Status = "tampered"
)

// Authority names which of the two layers produced a verdict's principal.
type Authority string

const (
	AuthorityNone     Authority = ""
	AuthorityManifest Authority = "manifest"
)

// Verdict is one attestation outcome.
type Verdict struct {
	Status Status
	// Principal is the trust-root principal whose attestation governs, resolved
	// from the trust root and NEVER from any "signer" field the artifact itself
	// carries. Empty unless Status is manifest-signed or item-signed.
	Principal string
	Authority Authority
	// Detail is a human-facing explanation. It is populated for every outcome
	// that is not plainly good, and it names both parties for a substitution.
	Detail string
	// UntrustedSignerFingerprint names the key behind a StatusUnattested
	// verdict that is unattested only because nothing TRUSTS the signature it
	// carries — as opposed to carrying no signature at all. The two look
	// identical in Status and Principal by design (signing.VerifyPublisher
	// collapses them), and they are different diagnoses to a human.
	//
	// DISPLAY ONLY, never an identity, and never an input to any decision: see
	// signing.SignatureKeyFingerprint. Empty for every other outcome, and set
	// only for a bundle's MANIFEST authority (bundleAuthority) — an item form's
	// verdict does not carry it.
	UntrustedSignerFingerprint string
}

// OK reports whether the content may be treated as attested by a trusted
// publisher.
func (v Verdict) OK() bool {
	return v.Status == StatusManifestSigned
}

// BundleVerdict is a whole bundle's outcome: the manifest's own attestation and
// the integrity of the tree beneath it.
type BundleVerdict struct {
	Bundle content.BundleID
	Verdict
	// Manifest is the parsed manifest, zero when the bundle has none.
	Manifest content.Manifest
	// Contents is Manifest.VerifyContents's result: nil when every claimed file
	// hashes AND every file on disk is claimed. It is reported SEPARATELY from
	// Status because the two failures are different — a valid signature over a
	// manifest that no longer describes the tree is not the same as no
	// signature at all, and collapsing them would lose which one happened.
	Contents error
}

// OK reports a bundle that is both attested and intact.
func (v BundleVerdict) OK() bool { return v.Verdict.OK() && v.Contents == nil }

// SignBundle builds the bundle's manifest for release rel, writes it, and signs
// it under NamespacePublish. One action, covering every file in the tree and
// the release header naming which bundle and which version it is.
//
// It REFUSES a tree whose kind directories hold files no surface type
// recognises. Publishing is the last moment a mis-extensioned hook is cheap to
// fix: the manifest would happily cover `guard.yml` by path, producing a
// perfectly signed bundle in which the guardrail silently does not exist. The
// publisher's own machine is where that must be caught.
func SignBundle(ctx context.Context, w content.Writer, b content.Bundle, rel release.Release, signer ssh.Signer) error {
	if signer == nil {
		return errors.New("attest: nil signer")
	}
	if _, err := b.Refs(ctx); err != nil {
		return fmt.Errorf("attest: refusing to sign bundle %q: %w", b.ID(), err)
	}
	m, err := content.BuildManifest(ctx, b, rel)
	if err != nil {
		return err
	}
	if err := w.PutManifest(ctx, b.ID(), m); err != nil {
		return err
	}
	sig, err := signing.Sign(m.Bytes(), signer, signing.NamespacePublish)
	if err != nil {
		return fmt.Errorf("attest: signing manifest of %q: %w", b.ID(), err)
	}
	return w.PutBundleSignature(ctx, b.ID(), publishNS, signer.PublicKey(), sig)
}

// VerifyBundle resolves a bundle's manifest attestation and checks the tree
// against it in both directions.
//
// It also enumerates the bundle's items, and that is not incidental: a file in
// a kind directory that no surface type recognises (a mis-extensioned hook) is
// covered by the manifest by PATH, so the tree is intact and the signature
// good — and the guardrail does not exist. Enumeration refuses it
// (content.ErrUnclaimed) where integrity alone would call it healthy.
//
// An error return means the bundle could not be READ. An unattested or tampered
// bundle is a verdict, not an error: refusing to produce a verdict for a
// suspicious bundle would leave the caller with nothing to show a user.
func VerifyBundle(ctx context.Context, b content.Bundle, root trust.TrustRoot, now time.Time) (BundleVerdict, error) {
	out := BundleVerdict{Bundle: b.ID()}
	m, mv, err := bundleAuthority(ctx, b, root, now)
	if err != nil {
		return BundleVerdict{}, err
	}
	out.Manifest, out.Verdict = m, mv
	if !m.IsZero() {
		out.Contents = m.VerifyContents(ctx, b)
	}
	if _, err := b.Refs(ctx); err != nil {
		return BundleVerdict{}, err
	}
	return out, nil
}

// bundleAuthority loads the manifest and resolves who, if anyone, signed it.
func bundleAuthority(ctx context.Context, b content.Bundle, root trust.TrustRoot, now time.Time) (content.Manifest, Verdict, error) {
	m, err := b.Manifest(ctx)
	switch {
	case errors.Is(err, content.ErrManifestMissing):
		return content.Manifest{}, Verdict{Status: StatusUnattested, Detail: "bundle carries no manifest"}, nil
	case errors.Is(err, content.ErrManifestFormat):
		// A manifest that exists and cannot be parsed is not an absent one. Any
		// signature over it covers bytes we refuse to interpret, so the honest
		// answer is tampered, not unsigned.
		return content.Manifest{}, Verdict{Status: StatusTampered, Detail: err.Error()}, nil
	case err != nil:
		return content.Manifest{}, Verdict{}, err
	}
	sigs, err := b.BundleSignatures(ctx)
	if err != nil {
		return content.Manifest{}, Verdict{}, err
	}
	v := manifestAuthority(m, sigs, root, now)
	// The signed name is the bundle's identity. A tree whose signature is
	// perfect but which is served under another bundle's path is a trusted
	// publisher's release re-homed by whoever controls the repository, and
	// every hash in it will match — only the name refuses it.
	if name := m.Release().Name; name != string(b.ID()) {
		return m, Verdict{Status: StatusTampered, Detail: fmt.Sprintf("manifest is signed as %q but the bundle is served as %q", name, b.ID())}, nil
	}
	return m, v, nil
}

// VerifyManifest resolves who, if anyone this trust root authorizes to
// publish, signed raw — a SHA256SUMS fetched on its own, with its bundle
// signatures, and no tree beside it. It is how a reader learns what the
// newest signed release SAYS (its version, its retractions) without trusting
// anything the repository serves unsigned.
//
// It says nothing about a tree: a caller that has one calls VerifyBundle,
// which also checks the files and the served name. Bytes that do not parse as
// a manifest are StatusTampered, never unattested — something is published at
// the manifest's path and it is not one.
func VerifyManifest(raw []byte, sigs content.SigSet, root trust.TrustRoot, now time.Time) (content.Manifest, Verdict) {
	m, err := content.ParseManifest(raw)
	if err != nil {
		return content.Manifest{}, Verdict{Status: StatusTampered, Detail: err.Error()}
	}
	return m, manifestAuthority(m, sigs, root, now)
}

// manifestAuthority folds a parsed manifest's publish signatures into a
// verdict. m.Bytes() is exactly the bytes that were parsed (ParseManifest is
// byte-strict), so this is the signature over what was read.
func manifestAuthority(m content.Manifest, sigs content.SigSet, root trust.TrustRoot, now time.Time) Verdict {
	att := resolvePublisher(m.Bytes(), sigs, root, now)
	switch {
	case att.verified():
		return Verdict{Status: StatusManifestSigned, Principal: att.principal, Authority: AuthorityManifest}
	case att.tampered():
		return Verdict{Status: StatusTampered, Detail: att.detail}
	default:
		return Verdict{
			Status: StatusUnattested,
			Detail: att.detail,
			// Display only, and only here: "no signature" and "a signature by
			// a key you do not trust" are both unattested, and a surface that
			// asks a human to admit content must be able to say which.
			UntrustedSignerFingerprint: att.fingerprint,
		}
	}
}

// attestation is resolvePublisher's answer, kept as its own tiny type so the
// three outcomes stay distinguishable.
type attestation struct {
	principal string // non-empty when a trusted key's signature covers the payload
	detail    string
	tamper    bool
	// fingerprint is DISPLAY ONLY (signing.SignatureKeyFingerprint): the key
	// behind the first stored signature nothing trusted, kept so an unattested
	// verdict can say "signed by a key you do not trust" instead of the
	// indistinguishable "unsigned". It never participates in the precedence
	// below and is discarded whenever a signature does verify.
	fingerprint string
}

func (a attestation) verified() bool { return a.principal != "" }
func (a attestation) tampered() bool { return a.principal == "" && a.tamper }

// resolvePublisher runs every stored publish signature past
// signing.VerifyPublisher and folds the results.
//
// It reads ONLY the publish namespace (see publishNS), and VerifyPublisher in
// turn only verifies against keys the trust root authorizes for that namespace.
// The pinning is therefore doubled and structural: a signature filed under
// approve is never looked at, and an approve-only key never verifies one.
//
// Precedence among several stored signatures is VERIFIED over TAMPERED over
// UNATTESTED. A bundle legitimately carries several signatures (two
// maintainers), so one that does not verify must not veto one that does — but a
// tamper signal must still outrank silence, or an attacker could bury a
// corrupted signature behind an absent one.
func resolvePublisher(payload []byte, sigs content.SigSet, root trust.TrustRoot, now time.Time) attestation {
	out := attestation{detail: "no signature by a key trusted to publish"}
	for _, blob := range sigs.ForNamespace(publishNS) {
		principal, err := signing.VerifyPublisher(payload, blob, root, now)
		switch {
		case err != nil:
			out.tamper, out.detail = true, err.Error()
		case principal != "":
			return attestation{principal: principal}
		default:
			// Untrusted key. Remember the FIRST one only, for display: a
			// fingerprint is a string a human compares out of band, and a
			// second one appended would be an invitation to trust whichever
			// looks familiar. Recorded here and nowhere else — this branch
			// deliberately does not touch tamper, detail, or precedence.
			if out.fingerprint == "" {
				if fp, fperr := signing.SignatureKeyFingerprint(blob); fperr == nil {
					out.fingerprint = fp
				}
			}
		}
	}
	return out
}
