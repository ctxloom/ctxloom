// Package attest is layer 2 of the content access surface: it decides WHO
// attests a bundle's bytes, over the layer-0 store and the layer-1 manifest.
//
// It lives beside package content rather than inside it so that layer 0 never
// imports a trust root. Layer 0 knows where signature bytes live; layer 1 knows
// whether the tree matches its manifest; only this package knows whether any of
// that was said by someone you trust.
//
// # The two layers a verdict rests on
//
// A bundle carries a MANIFEST (path -> hash, the tree's shape) and a SIGNATURE
// STORE (key -> signatures). Whole-bundle signing is the common case: one
// signature over the manifest attests every file in the tree, and MOST items
// therefore have no signature of their own. That absence must read as NORMAL,
// never as "unsigned" — getting it wrong either floods users with false
// warnings or silently treats manifest-covered items as unverified.
//
// # Precedence, stated so it cannot be read as a downgrade
//
// The rule is most-specific-wins, but only where the two attestations AGREE
// about the bytes. A per-item signature governs when the manifest also covers
// the item's actual bytes; it is mixed provenance, and the verdict names the
// item's signer. When a signed manifest CONTRADICTS the bytes on disk, a
// per-item signature does not override it — the disagreement is itself the
// finding, and it surfaces as StatusContentSubstituted. Conflating that with
// absence is what would let any co-trusted key substitute content inside
// another publisher's bundle with no diagnostic.
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

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
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
	// publisher signed, with bytes that match. The COMMON case.
	StatusManifestSigned Status = "manifest-signed"
	// StatusItemSigned: attested by a signature over this item form's own
	// content, from a trusted publisher, with no disagreement from the
	// manifest. The mixed-provenance case.
	StatusItemSigned Status = "item-signed"
	// StatusContentSubstituted: a trusted publisher's signed manifest claims
	// DIFFERENT bytes for this item than are on disk, AND the bytes on disk
	// carry a valid publish signature from another trusted key.
	//
	// This verdict exists because the alternative is silence. Both signatures
	// are cryptographically fine; what is wrong is that two trusted parties
	// disagree about what belongs here, and a verifier that simply preferred
	// the more specific one would let any co-trusted key substitute content
	// inside another publisher's bundle without a word. It is NOT trusted (see
	// Verdict.OK) — it is surfaced so a human can arbitrate.
	StatusContentSubstituted Status = "content-substituted"
	// StatusTampered: an attestation is present and does NOT honestly cover
	// what it claims — a signed manifest contradicted by the bytes with no
	// competing signature, or a signature blob that is not one. Withhold the
	// content; never degrade this to unsigned, or corrupting a signature would
	// downgrade a signed bundle to an unsigned one.
	StatusTampered Status = "tampered"
)

// Authority names which of the two layers produced a verdict's principal.
type Authority string

const (
	AuthorityNone     Authority = ""
	AuthorityManifest Authority = "manifest"
	AuthorityItem     Authority = "item"
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
// publisher. Exactly two statuses qualify. In particular
// StatusContentSubstituted does NOT: a disagreement between two trusted keys is
// a finding, not an attestation.
func (v Verdict) OK() bool {
	return v.Status == StatusManifestSigned || v.Status == StatusItemSigned
}

// ItemVerdict is one item form's outcome within a bundle.
type ItemVerdict struct {
	Ref  trust.Ref
	Form signing.Form
	Verdict
}

// BundleVerdict is a whole bundle's outcome: the manifest's own attestation,
// the integrity of the tree beneath it, and a verdict per item form.
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
	Items    []ItemVerdict
}

// OK reports a bundle that is both attested and intact.
func (v BundleVerdict) OK() bool { return v.Verdict.OK() && v.Contents == nil }

// SignBundle builds the bundle's manifest, writes it, and signs it under
// NamespacePublish. One action, covering every file in the tree — the default
// path, and the reason most items never need a signature of their own.
//
// It REFUSES a tree whose kind directories hold files no surface type
// recognises. Publishing is the last moment a mis-extensioned hook is cheap to
// fix: the manifest would happily cover `guard.yml` by path, producing a
// perfectly signed bundle in which the guardrail silently does not exist. The
// publisher's own machine is where that must be caught.
func SignBundle(ctx context.Context, w content.Writer, b content.Bundle, signer ssh.Signer) error {
	if signer == nil {
		return errors.New("attest: nil signer")
	}
	if _, err := b.Refs(ctx); err != nil {
		return fmt.Errorf("attest: refusing to sign bundle %q: %w", b.ID(), err)
	}
	m, err := content.BuildManifest(ctx, b)
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
	return w.PutBundleSignature(ctx, b.ID(), publishNS, sig)
}

// SignItem signs one form of one item under NamespacePublish — the exception,
// for the item whose signer differs from the bundle's.
//
// It signs the form's CONTENT DIGEST, which is what the signature store keys on,
// so a signature over the raw form can never be located for the distilled one.
func SignItem(ctx context.Context, w content.Writer, it content.Item, f signing.Form, signer ssh.Signer) error {
	if signer == nil {
		return errors.New("attest: nil signer")
	}
	form, err := it.Form(ctx, f)
	if err != nil {
		return err
	}
	digest, err := form.Content(ctx)
	if err != nil {
		return err
	}
	sig, err := signing.Sign(digest, signer, signing.NamespacePublish)
	if err != nil {
		return fmt.Errorf("attest: signing %s form %q: %w", it.Ref().Key(), f, err)
	}
	return w.PutSignature(ctx, it.Ref(), f, publishNS, sig)
}

// VerifyBundle resolves a bundle's manifest attestation, checks the tree
// against it in both directions, and reports a verdict for every item form.
//
// An error return means the bundle could not be READ. An unattested or tampered
// bundle is a verdict, not an error: refusing to produce a verdict for a
// suspicious bundle would leave the caller with nothing to show a user.
func VerifyBundle(ctx context.Context, b content.Bundle, root signing.TrustRoot, now time.Time) (BundleVerdict, error) {
	out := BundleVerdict{Bundle: b.ID()}
	m, mv, err := bundleAuthority(ctx, b, root, now)
	if err != nil {
		return BundleVerdict{}, err
	}
	out.Manifest, out.Verdict = m, mv
	if !m.IsZero() {
		out.Contents = m.VerifyContents(ctx, b)
	}
	refs, err := b.Refs(ctx)
	if err != nil {
		return BundleVerdict{}, err
	}
	for _, ref := range refs {
		item, err := b.Item(ctx, ref)
		if err != nil {
			return BundleVerdict{}, err
		}
		forms, err := item.Forms(ctx)
		if err != nil {
			return BundleVerdict{}, err
		}
		for _, f := range forms {
			v, err := verifyForm(ctx, item, f, m, mv, root, now)
			if err != nil {
				return BundleVerdict{}, err
			}
			out.Items = append(out.Items, ItemVerdict{Ref: ref, Form: f, Verdict: v})
		}
	}
	return out, nil
}

// VerifyItem resolves one item form's verdict, resolving the bundle's manifest
// authority for itself.
//
// DIVERGENCE from the design's `VerifyItem(ctx, it Item, ...)`: it takes the
// Bundle and a ref rather than an Item. A verdict is not a property of the item
// alone — the manifest that may attest it is a bundle-level object — and taking
// both a Bundle and an unrelated Item would let a caller verify one bundle's
// item against another bundle's manifest.
//
// SCOPE, stated because it is easy to over-read: this answers for ONE item form.
// It does not enumerate the bundle and therefore does not see files elsewhere in
// the tree — an added directory, or a mis-extensioned hook that is no item at
// all. Those are bundle-level facts and only VerifyBundle reports them. A caller
// deciding whether to trust a whole tree must call VerifyBundle; VerifyItem is
// for deciding about one item in a tree already accepted.
func VerifyItem(ctx context.Context, b content.Bundle, ref trust.Ref, f signing.Form, root signing.TrustRoot, now time.Time) (Verdict, error) {
	item, err := b.Item(ctx, ref)
	if err != nil {
		return Verdict{}, err
	}
	m, mv, err := bundleAuthority(ctx, b, root, now)
	if err != nil {
		return Verdict{}, err
	}
	return verifyForm(ctx, item, f, m, mv, root, now)
}

// bundleAuthority loads the manifest and resolves who, if anyone, signed it.
func bundleAuthority(ctx context.Context, b content.Bundle, root signing.TrustRoot, now time.Time) (content.Manifest, Verdict, error) {
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
	att := resolvePublisher(m.Bytes(), sigs, root, now)
	switch {
	case att.verified():
		return m, Verdict{Status: StatusManifestSigned, Principal: att.principal, Authority: AuthorityManifest}, nil
	case att.tampered():
		return m, Verdict{Status: StatusTampered, Detail: att.detail}, nil
	default:
		return m, Verdict{
			Status: StatusUnattested,
			Detail: att.detail,
			// Display only, and only here: "no signature" and "a signature by
			// a key you do not trust" are both unattested, and a surface that
			// asks a human to admit content must be able to say which.
			UntrustedSignerFingerprint: att.fingerprint,
		}, nil
	}
}

// attestation is resolvePublisher's answer, kept as its own tiny type so the
// three outcomes stay distinguishable. Folding them onto Status here was tried
// and was worse: it made an ITEM's signature report StatusManifestSigned.
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
func resolvePublisher(payload []byte, sigs content.SigSet, root signing.TrustRoot, now time.Time) attestation {
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

// verifyForm is the precedence rule, in one place.
func verifyForm(ctx context.Context, item content.Item, f signing.Form, m content.Manifest, mv Verdict, root signing.TrustRoot, now time.Time) (Verdict, error) {
	form, err := item.Form(ctx, f)
	if err != nil {
		return Verdict{}, err
	}
	components, err := form.Components(ctx)
	if err != nil {
		return Verdict{}, err
	}
	digest, err := form.Content(ctx)
	if err != nil {
		return Verdict{}, err
	}
	sigs, err := form.Signatures(ctx)
	if err != nil {
		return Verdict{}, err
	}
	att := resolvePublisher(digest, sigs, root, now)

	// Does the signed manifest agree with what is actually on disk?
	agrees, disagreement := manifestAgrees(m, components)

	if mv.Status != StatusManifestSigned {
		// No trusted manifest to defer to. The item's own signature is the only
		// authority available, and its absence is plain unattestedness.
		switch {
		case att.verified():
			return Verdict{Status: StatusItemSigned, Principal: att.principal, Authority: AuthorityItem}, nil
		case att.tampered():
			return Verdict{Status: StatusTampered, Detail: att.detail}, nil
		default:
			return Verdict{Status: StatusUnattested, Detail: att.detail}, nil
		}
	}

	if agrees {
		if att.verified() {
			// Mixed provenance, and both attestations describe the SAME bytes.
			// Most-specific-wins applies here and only here.
			return Verdict{
				Status:    StatusItemSigned,
				Principal: att.principal,
				Authority: AuthorityItem,
				Detail:    fmt.Sprintf("also covered by %s's bundle manifest", mv.Principal),
			}, nil
		}
		return Verdict{Status: StatusManifestSigned, Principal: mv.Principal, Authority: AuthorityManifest}, nil
	}

	// The manifest a trusted publisher signed does not describe these bytes.
	if att.verified() {
		return Verdict{
			Status:    StatusContentSubstituted,
			Authority: AuthorityNone,
			Detail: fmt.Sprintf(
				"%s's signed manifest claims different bytes here (%s), but the bytes on disk carry a valid publish signature from %s; two trusted keys disagree about what belongs at this path",
				mv.Principal, disagreement, att.principal),
		}, nil
	}
	return Verdict{
		Status: StatusTampered,
		Detail: fmt.Sprintf("%s's signed manifest claims different bytes here (%s)", mv.Principal, disagreement),
	}, nil
}

// manifestAgrees reports whether the manifest covers every one of these
// components with the hash they actually have, and describes the first
// disagreement when it does not.
//
// An UNCOVERED component counts as disagreement, not as a gap to be forgiven: a
// file the signed manifest never mentions is an addition, and treating it as
// merely uncovered is the laundering channel this whole layer exists to close.
func manifestAgrees(m content.Manifest, components []content.Component) (bool, string) {
	if m.IsZero() {
		return false, "no manifest"
	}
	for _, c := range components {
		want, ok := m.Lookup(c.Path)
		if !ok {
			return false, fmt.Sprintf("%s is not covered by the manifest at all", c.Path)
		}
		if got := content.SHA256Hex(c.Bytes); got != want {
			return false, fmt.Sprintf("%s hashes to %s, manifest says %s", c.Path, got[:12], want[:12])
		}
	}
	return true, ""
}
