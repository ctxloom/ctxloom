package bundles

import (
	"context"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Reader is the read half of the delivery seam: everything one SOURCE of
// bundles holds, reported as facts, with no policy applied.
//
// It is Read(), not Lookup(ref), on purpose. A per-ref query interface is a
// registry, and a registry lets an implementation decide what to hand back on
// each call — which is a filtering hook wearing a lookup's clothes. A Reader
// reports EVERYTHING it holds, every time, and nothing anywhere in an
// implementation may drop an item because of what it turned out to be. What a
// reader may do is REPORT: a bundle whose bytes never parsed produced no
// content at all and is warned about, but nothing that became a bundle is
// withheld here (docs/design/engine-delivery-seam.design.md, "ALL processing
// lives in the middle").
//
// Read takes a context because one implementation — the companion reader —
// EXECS foreign binaries to obtain its bytes, so a read can genuinely hang. An
// interface that assumed reads resolve promptly would have foreclosed it.
type Reader interface {
	Read(ctx context.Context) ([]BundleRead, error)
}

// ProvenanceClass labels WHERE a bundle came from. It is a LABEL, for
// diagnostics and for the reason a user is told, never the axis a gate keys on
// — that is TrustCtx, and the two are kept separate precisely so that adding a
// new source class cannot silently invent a new trust posture.
type ProvenanceClass int

// The provenance classes. The zero value is UNSET and no reader ever emits it:
// an unpopulated BundleRead must not read as though it came from somewhere.
const (
	ProvenanceUnset ProvenanceClass = iota
	// ProvenanceProject is content authored in this project's own tree.
	ProvenanceProject
	// ProvenanceBuiltin is content embedded in the ctxloom binary.
	ProvenanceBuiltin
	// ProvenanceCompanion is a loadout a companion application advertised
	// about itself.
	ProvenanceCompanion
	// ProvenanceRemote is content pulled from a publisher's repository.
	ProvenanceRemote
)

func (p ProvenanceClass) String() string {
	switch p {
	case ProvenanceProject:
		return "project"
	case ProvenanceBuiltin:
		return "builtin"
	case ProvenanceCompanion:
		return "companion"
	case ProvenanceRemote:
		return "remote"
	default:
		return "unset"
	}
}

// TrustCtx is the ONLY axis a trust gate keys on: did these bytes cross an
// intermediary on their way here, or not.
//
// Two values, deliberately. Project, builtin and companion content are all
// LOCAL — a builtin was compiled into this binary and a companion loadout came
// straight off the stdout of a binary the user consented to execute — and a
// publisher signature exists to catch an intermediary that none of those three
// have. Remote content is the one class that must EARN trust rather than
// inherit it.
type TrustCtx int

// The trust contexts. Zero is UNSET and no reader emits it; anything consuming
// a BundleRead treats unset as withhold, because "no context" is not "local".
const (
	TrustCtxUnset TrustCtx = iota
	// TrustCtxLocal is content that reached this machine without an
	// intermediary. Signature facts on it are DIAGNOSTICS, not gates.
	TrustCtxLocal
	// TrustCtxRemote is content that crossed a network and a forge. Signature
	// facts on it are the gate's inputs.
	TrustCtxRemote
)

func (t TrustCtx) String() string {
	switch t {
	case TrustCtxLocal:
		return "local"
	case TrustCtxRemote:
		return "remote"
	default:
		return "unset"
	}
}

// Signature is what a detached signature over these exact bytes turned out to
// be — a fact about BYTES, saying nothing about who made it.
type Signature int

// The signature states. Zero is UNSET and no reader emits it: an unpopulated
// BundleRead must not read as "no signature", which is a claim.
const (
	SignatureUnset Signature = iota
	// SignatureNone means no signature accompanied these bytes.
	SignatureNone
	// SignatureInvalid means a signature exists and does not cover these bytes
	// (or is not a signature at all, or could not be read). Tamper on remote
	// content; on local content, usually an author who edited and did not
	// re-sign.
	SignatureInvalid
	// SignatureValid means a signature cryptographically covers exactly these
	// bytes, by whatever key made it.
	SignatureValid
)

func (s Signature) String() string {
	switch s {
	case SignatureNone:
		return "none"
	case SignatureInvalid:
		return "invalid"
	case SignatureValid:
		return "valid"
	default:
		return "unset"
	}
}

// Signer is what this machine's trust root says about the KEY behind that
// signature — a fact about IDENTITY, orthogonal to whether the signature holds.
//
// The two axes are separate because a merged verdict collapses "a trusted key
// whose signature no longer covers these bytes" (someone edited and did not
// re-sign — recurring, non-adversarial, wants "re-sign this") into the same
// bucket as a stranger's bad blob (wants "do not trust this"). Keeping them
// apart is what lets those be different sentences.
type Signer int

// The signer states. Zero is UNSET and no reader emits it.
const (
	SignerUnset Signer = iota
	// SignerNone means there was no signature, so there is no key to speak of.
	SignerNone
	// SignerUntrusted means a key made this signature and this machine does
	// not trust it to publish. Display its fingerprint; never treat it as an
	// identity.
	SignerUntrusted
	// SignerTrusted means the trust root authorizes this key for the publish
	// namespace.
	SignerTrusted
)

func (s Signer) String() string {
	switch s {
	case SignerNone:
		return "none"
	case SignerUntrusted:
		return "untrusted"
	case SignerTrusted:
		return "trusted"
	default:
		return "unset"
	}
}

// BundleRead is one bundle as a reader found it: the content, the label saying
// where it came from, and the three trust axes as FACTS.
//
// The three axes are UNEXPORTED and settable only through newRead, which only
// this package's readers call. That is not ceremony: an exported trustCtx would
// let any caller anywhere mint local-context content out of a struct literal —
// a trust bypass that reviews as data. It is the same rule, for the same
// reason, that keeps Bundle.untrustedSignerFingerprint unexported with
// `yaml:"-"` so a bundle file cannot forge its own signer
// (TestParseBundle_YAMLCannotForgeUntrustedSignerFingerprint).
type BundleRead struct {
	// Bundle is the parsed content. Never nil in a read a reader emitted.
	Bundle *Bundle

	// Provenance labels the source class. No reader takes it as a constructor
	// argument: each hard-codes its own, so a caller cannot ask a reader for
	// builtin-labelled content.
	//
	// It stays EXPORTED, and that is now a deliberate line rather than a free
	// one: the decision function reads it to name WHICH first-party exemption
	// allowed an item (local / builtin / companion). What it cannot do is create
	// an exemption, because the exemption is gated on trustCtx, which is
	// unexported. Flipping this field on a REMOTE read buys nothing — that read
	// reaches no first-party arm at all — and flipping it on a local read only
	// changes which true first-party name the allow is reported under. The axis
	// that decides is still one no caller can write.
	Provenance ProvenanceClass

	// ref is this bundle's RESOLUTION identity — the name a profile or a
	// caller asks for it by. For project content that is its path-relative
	// name; for remote content the canonical lockfile ref; for a companion its
	// ctxloom:companion@<bin> ref. It is unexported for the same reason the
	// axes are: identity decides which content answers to a name.
	ref string

	trustCtx  TrustCtx
	signature Signature
	signer    Signer

	// signatureDetail is the human-readable reason a signature is invalid,
	// carried so the diagnostic can say WHAT went wrong rather than only that
	// something did. Empty for every other state.
	signatureDetail string

	// untrustedFingerprint is the DISPLAY-ONLY fingerprint of the key that
	// made an untrusted signature (signing.SignatureKeyFingerprint). Never an
	// identity, never a trust input; it exists so a review surface can show a
	// human which key they would be trusting.
	untrustedFingerprint string
}

// Ref reports this bundle's resolution identity — what a caller asks for it by.
//
// It is NOT the trust key, and since a builtin's resolution ref stopped
// carrying its source class the two genuinely differ: `isolation` resolves the
// builtin, `builtin:isolation` gates it. Anything building a
// "<source>#<kind>/<name>" gate ref wants TrustSourceRef below.
func (r BundleRead) Ref() string { return r.ref }

// TrustSourceRef reports the source component of this bundle's content trust
// refs — the string a "<source>#<kind>/<name>" gate ref is built from.
//
// It exists so the two routes a builtin's content reaches a session by cannot
// key differently. The loader-resolved route builds its ref from
// Bundle.contentSourceRef(); the unconditional injection route
// (config.ResolveBuiltinBundleFragments and its hooks/MCP siblings) used to
// build its own from BundleRead.Ref(), and that only agreed because a builtin's
// resolution ref happened to be spelled "builtin:<name>" too. It no longer is,
// so the injection route reads the trust key HERE rather than reconstructing
// it. Two constructions of one identity is exactly how a rejection recorded
// against one route stops withholding the other (crispy-scoop).
//
// Location-derived without exception: the readers stamp it, nothing a bundle
// DECLARES reaches it (outdated-recoil).
func (r BundleRead) TrustSourceRef() string {
	if r.Bundle == nil {
		return ""
	}
	return r.Bundle.contentSourceRef()
}

// SourceRef reports TrustSourceRef's structured counterpart: the same
// location-derived source, as a trust.BundleRef instead of a string a caller
// would have to reparse. It is minted by the reader at the same point it
// stamps TrustSourceRef, through the class matching where the bundle was
// actually found — never by parsing TrustSourceRef's string back apart.
//
// The zero BundleRef means either an unclaimed read (r.Bundle == nil) or a
// reader that has not stamped this field yet; BundleRead.Claimed does not
// cover it, so a caller that must distinguish those two should check
// TrustSourceRef first.
func (r BundleRead) SourceRef() trust.BundleRef {
	if r.Bundle == nil {
		return trust.BundleRef{}
	}
	return r.Bundle.contentSourceRefTyped()
}

// itemRefFor mints the canonical "<source>#<kind>/<item>" reference an item's
// TrustRef is built from: src.WithItem(kind, item).String(), rendered through
// the canonical bundle-reference grammar rather than hand-concatenated.
//
// WithItem can fail only if src itself is not a validly-minted BundleRef —
// unreachable for a source ref a reader has already stamped, since every
// stamp site mints through the SAME class minters (BuiltinRef/LocalRef/
// CompanionRef/GitRef/FileRef) WithItem itself round-trips through. It is
// reachable in exactly the shape AsBundleRef's own doc describes: src is the
// zero BundleRef, which BundleRead.SourceRef reports as-is for a read whose
// typed source was never established. Degrading to a stable, well-formed,
// UNADDRESSABLE-looking string — rather than the empty string, or the
// hand-concatenated fallback the caller could no longer construct without
// src — is deliberate and mirrors operations.CountersignRef's identical
// fallback for the identical unreachable case: an item must key SOMEWHERE
// stable, never silently collide with another unaddressable item by both
// flattening to "".
func itemRefFor(src trust.BundleRef, kind trust.ItemKind, item string) string {
	br, err := src.WithItem(kind, item)
	if err != nil {
		// kind and item are appended, not just src: WithItem fails BEFORE
		// they land on the BundleRef, so a %#v of src alone is identical for
		// every item of the same unaddressable bundle. Without them here, a
		// fragment and a command sharing one unaddressable source would
		// degrade to the SAME withheld key — meaning only one of the two
		// would ever be tallied, and the other's withhold would look like it
		// never happened. Never mint an unaddressable address whose only
		// axis of uniqueness the caller can lose.
		return fmt.Sprintf("ctxloom+unaddressable:%#v#%s/%s", src, kind.Dir(), item)
	}
	return br.String()
}

// TrustCtx reports the only axis a gate keys on.
func (r BundleRead) TrustCtx() TrustCtx { return r.trustCtx }

// Signature reports what the detached signature over these bytes turned out to be.
func (r BundleRead) Signature() Signature { return r.signature }

// Signer reports what the trust root says about the key behind that signature.
func (r BundleRead) Signer() Signer { return r.signer }

// SignatureDetail reports why a signature is invalid, for diagnostics. Empty
// unless Signature is SignatureInvalid.
func (r BundleRead) SignatureDetail() string { return r.signatureDetail }

// UntrustedSignerFingerprint reports the display-only fingerprint of an
// untrusted signing key, or "" when there is none. It is never a trust input;
// see signing.SignatureKeyFingerprint.
func (r BundleRead) UntrustedSignerFingerprint() string { return r.untrustedFingerprint }

// Claimed reports whether every axis of this read was actually populated by a
// reader.
//
// An unpopulated BundleRead claims nothing, and a consumer must treat it as a
// withhold rather than as "local, unsigned, no signer" — which is precisely the
// claim a zero value would otherwise make. This is the check that turns "zero
// means unset" from a comment into behaviour.
func (r BundleRead) Claimed() bool {
	return r.Bundle != nil && r.ref != "" && r.Provenance != ProvenanceUnset &&
		r.trustCtx != TrustCtxUnset && r.signature != SignatureUnset && r.signer != SignerUnset
}

// newRead builds a read with its axes set. Unexported by design: it is the
// only way the axes are ever populated, so every value on them came from a
// reader that established it.
//
// It also STAMPS the content trust key (Bundle.sourceRef) from ref when a
// reader left it empty, which makes that key location-derived for every read
// without exception. ref is the bundle's RESOLUTION identity, and resolution
// identity is decided by where the bundle was found — the path-relative name
// under the project tree, the lockfile's canonical ref, the companion's
// ctxloom:companion ref — never by the document's own `name:`. Without this,
// contentSourceRef fell back to Bundle.Name, which a bundle DECLARES, so the
// content being judged supplied an input to its own trust key: a project
// bundle declaring `name: builtin:isolation` keyed as that builtin
// (outdated-recoil).
//
// Only-when-empty, so a ref a reader already established deliberately wins:
// WithSeededBundles' lockfile ref, the repofs reader's, the companion
// reader's, and localFSReader's "builtin:<name>" for embedded content.
//
// Every caller that reaches this fallback with sourceRef still empty is, by
// construction, a genuinely local resolution ref — the companion, repofs and
// builtin call sites all stamp sourceRef (string AND typed) themselves before
// calling newRead, so the only ones left empty here are localFSReader's
// project-provenance bundles and the package's other exported constructor for
// project-authored (non-Reader) content above. The typed stamp below is
// minted with trust.LocalRef accordingly, not re-derived by inspecting
// prov/tctx: a fifth reader that reached this fallback for a non-local ref
// would be a bug in THAT reader, not something this function could detect
// from its own arguments.
func newRead(ref string, b *Bundle, prov ProvenanceClass, tctx TrustCtx, facts signatureFacts) BundleRead {
	if b != nil && b.sourceRef == "" {
		b.sourceRef = ref
		if typed, err := trust.LocalRef(ref); err == nil {
			b.sourceRefTyped = typed
		}
	}
	return BundleRead{
		Bundle:               b,
		ref:                  ref,
		Provenance:           prov,
		trustCtx:             tctx,
		signature:            facts.signature,
		signer:               facts.signer,
		signatureDetail:      facts.detail,
		untrustedFingerprint: facts.fingerprint,
	}
}

// signatureFacts is the (signature, signer) pair a reader established over one
// bundle's bytes, plus the two display-only strings that go with them.
type signatureFacts struct {
	signature Signature
	signer    Signer
	// principal is the VERIFIED publisher identity, resolved from the trust
	// root and never from anything the artifact says about itself. It is
	// non-empty only for valid/trusted.
	principal   string
	detail      string
	fingerprint string
}

// readSignatureFacts resolves both signature axes for one bundle's bytes and
// its detached signature, against this machine's trust root.
//
// EVERY reader calls it, local ones included. Under TrustCtxLocal the answer is
// a diagnostic and under TrustCtxRemote it is a gate input, but the FACT is the
// same fact and establishing it in one function is what keeps the two from
// drifting into different notions of "valid".
//
// The five reachable outcomes:
//
//	no signature                          -> none/none
//	trusted key, covers these bytes       -> valid/trusted
//	untrusted key, covers these bytes     -> valid/untrusted (+fingerprint)
//	trusted key, does NOT cover the bytes -> invalid/trusted   (edited, not re-signed)
//	untrusted key or unreadable blob      -> invalid/untrusted
//
// A blob that will not even parse is invalid/untrusted rather than none/none:
// "there is no signature here" and "there is a signature I cannot read" are
// different facts, and reporting the second as the first is the downgrade
// spec §10.2 forbids.
func readSignatureFacts(payload, armoredSig []byte, root signing.TrustRoot) signatureFacts {
	if len(armoredSig) == 0 {
		return signatureFacts{signature: SignatureNone, signer: SignerNone}
	}
	principal, err := signing.VerifyPublisher(payload, armoredSig, root, time.Now())
	switch {
	case err != nil:
		// VerifyPublisher only reaches its byte check once the key is trusted,
		// so a tamper verdict with a readable key means trusted-key/wrong-bytes;
		// an unreadable blob names no key at all and cannot be called trusted.
		facts := signatureFacts{signature: SignatureInvalid, detail: err.Error(), signer: SignerUntrusted}
		if _, fperr := signing.SignatureKeyFingerprint(armoredSig); fperr == nil {
			facts.signer = SignerTrusted
		}
		return facts
	case principal != "":
		return signatureFacts{signature: SignatureValid, signer: SignerTrusted, principal: principal}
	}
	// Unsigned TO US: a signature exists, made by a key this machine does not
	// trust to publish. Whether it covers the bytes is still a fact worth
	// having — a stranger's stale blob and a stranger's good blob are different
	// diagnoses — and the fingerprint is what lets a human compare it against
	// what the publisher told them out of band.
	facts := signatureFacts{signer: SignerUntrusted}
	if err := signing.CoversBytes(payload, armoredSig, signing.NamespacePublish); err != nil {
		facts.signature = SignatureInvalid
		facts.detail = err.Error()
	} else {
		facts.signature = SignatureValid
	}
	if fp, fperr := signing.SignatureKeyFingerprint(armoredSig); fperr == nil {
		facts.fingerprint = fp
	}
	return facts
}

// stamp records on the bundle what the reader established about its signer, so
// the existing consumers of Bundle.Signer()/UntrustedSignerFingerprint() keep
// seeing exactly what they saw before the readers existed.
//
// Only a VALID signature by a TRUSTED key yields a signer; everything else is
// unsigned-to-us, which is the review path, not an identity.
func (f signatureFacts) stamp(b *Bundle) {
	if f.signature == SignatureValid && f.signer == SignerTrusted {
		b.StampSigner(f.principal)
		b.StampUntrustedSignerFingerprint("")
		return
	}
	b.StampSigner("")
	b.StampUntrustedSignerFingerprint(f.fingerprint)
}

// ReaderOption configures a reader with something that is NEITHER its
// provenance NOR its trust context — those two are hard-coded by each
// constructor and are deliberately not expressible here.
type ReaderOption func(*readerConfig)

// readerConfig is the shared configurable state of the reader implementations.
type readerConfig struct {
	root        signing.TrustRoot
	installDir  string
	repoURL     string
	revision    string
	warnHandler func(format string, args ...any)
}

// WithTrustRoot supplies the trust root a reader resolves signer identity
// against (embedded + user + project allowed_signers). Without one, no key is
// trusted and every signature reads as untrusted — the fail-toward-less-
// exposure direction, and never a silent claim of trust.
func WithTrustRoot(root signing.TrustRoot) ReaderOption {
	return func(c *readerConfig) { c.root = root }
}

// WithInstalledDir tells a repofs reader the on-disk directory a pinned tree
// was installed into, so a tree-form bundle's SKILL packages — which are files,
// not bytes in a document — can resolve. It carries no trust meaning: the
// content is remote either way.
func WithInstalledDir(dir string) ReaderOption {
	return func(c *readerConfig) { c.installDir = dir }
}

// WithPinnedRevision records the revision a pinned tree's bytes were fetched
// at, so a diagnostic can say WHICH bytes it is talking about. It is provenance
// detail for humans, never an input to any decision.
func WithPinnedRevision(rev string) ReaderOption {
	return func(c *readerConfig) { c.revision = rev }
}

// WithRepoURL supplies the publisher repository a pinned tree came from, which
// content provenance requires and refuses to default.
func WithRepoURL(url string) ReaderOption {
	return func(c *readerConfig) { c.repoURL = url }
}

// WithReaderWarnings redirects a reader's user-facing diagnostics. The default
// is the shared clidiag sink; tests use this to read what the user was told.
func WithReaderWarnings(warn func(format string, args ...any)) ReaderOption {
	return func(c *readerConfig) { c.warnHandler = warn }
}

func newReaderConfig(opts []ReaderOption) readerConfig {
	var c readerConfig
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// warn emits one reader diagnostic through the configured sink. A reader that
// could not turn bytes into a bundle says so HERE: nothing about a failed read
// is allowed to be silent, because zero content and zero diagnostics is
// indistinguishable from a source that legitimately holds nothing.
func (c readerConfig) warn(format string, args ...any) {
	if c.warnHandler != nil {
		c.warnHandler(format, args...)
		return
	}
	clidiag.Warn("ctxloom", format, args...)
}

// warnOnce is warn for a diagnostic whose repetition is noise rather than
// information — the same line about the same companion in one process. The
// dedup is the process's business; the sink is still the caller's.
func (c readerConfig) warnOnce(format string, args ...any) {
	if c.warnHandler != nil {
		c.warnHandler(format, args...)
		return
	}
	clidiag.WarnOnce("ctxloom", format, args...)
}
