package bundles

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/core/paths"
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
	case ProvenanceCompanion:
		return "companion"
	case ProvenanceRemote:
		return "remote"
	default:
		return "unset"
	}
}

// TrustCtx is whether these bytes crossed an intermediary on their way here,
// or not.
//
// Two values, deliberately. Project, builtin and companion content are all
// LOCAL — a builtin was compiled into this binary and a companion loadout came
// straight off the stdout of a binary on the user's PATH. Remote content came
// from a repository the user registered, and registering it was the trust act.
type TrustCtx int

// The trust contexts. Zero is UNSET and no reader emits it; anything consuming
// a BundleRead treats unset as withhold, because "no context" is not "local".
const (
	TrustCtxUnset TrustCtx = iota
	// TrustCtxLocal is content that reached this machine without an
	// intermediary: a human with write access placed it.
	TrustCtxLocal
	// TrustCtxRemote is content that crossed a network and a forge, from a
	// registered remote.
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

// BundleRead is one bundle as a reader found it: the content, the label saying
// where it came from, and its trust context as a FACT.
//
// The trust context is UNEXPORTED and settable only through newRead, which only
// this package's readers call: an exported trustCtx would let any caller
// anywhere mint local-context content out of a struct literal.
type BundleRead struct {
	// Bundle is the parsed content. Never nil in a read a reader emitted.
	Bundle *Bundle

	// Init is the typed INIT loadout that arrived beside Bundle in the same
	// document — set only by the companion reader, the zero value for
	// every other source class. It is content, not a trust axis, which is
	// why it is exported like Bundle: the facts that decide whether it may
	// be delivered are the read's, established once for both halves.
	Init InitLoadout

	// Provenance labels the source class. No reader takes it as a constructor
	// argument: each hard-codes its own, so a caller cannot ask a reader for
	// builtin-labelled content.
	//
	// It stays EXPORTED: what it cannot do is change the trust context, which
	// is unexported, so no caller can write that axis.
	Provenance ProvenanceClass

	// ref is this bundle's RESOLUTION identity — the name a profile or a
	// caller asks for it by. For project content that is its path-relative
	// name; for remote content the canonical lockfile ref; for a companion its
	// ctxloom:companion@<bin> ref. It is unexported for the same reason the
	// axes are: identity decides which content answers to a name.
	ref string

	// layout is the on-disk bundle layout that answered for this read, or
	// paths.LayoutUnknown for a read that came from somewhere with no layout at
	// all — an installed remote tree, a companion, a builtin compiled into the
	// binary. It is unexported for the same reason the axes are, and reaches a
	// caller through Catalog.Locate.
	layout paths.BundleLayout
	// alsoIn names every OTHER layout the same resolution name was found in.
	// A bundle present in both layouts is not an error — it is the expected
	// steady state of a migration that is deliberately non-destructive — but a
	// caller that cannot see it has no way to tell a migrated bundle from one
	// whose migration silently changed nothing.
	alsoIn []paths.BundleLayout

	trustCtx TrustCtx
}

// DisplayName reports the name a listing shows for this bundle and a user may
// type back at it — "lang/go", "alice/go-tools".
//
// It is a LABEL, not an identity. Two bundles read from different sources may
// share one, and neither displaces the other; what tells them apart is Key.
// Anything that must address exactly one bundle — a trust ref, a resolution,
// a "<source>#<kind>/<name>" gate ref — wants Key or SourceRef below.
func (r BundleRead) DisplayName() string { return r.ref }

// Key reports this read's RESOLUTION identity: SourceRef().BundleIdentity(),
// the version-less, item-less canonical URI a caller round-trips through
// Catalog.LookupKey. It is location-derived, exactly as SourceRef is — it
// never depends on which reader composed last or on any spelling a bundle
// declares.
func (r BundleRead) Key() ident.BundleKey {
	return r.SourceRef().BundleIdentity()
}

// SourceRef reports the source component of this bundle's content trust
// refs — the structured ident.BundleRef a "<source>#<kind>/<name>" gate ref
// is built from.
//
// It exists so the routes a bundle's content reaches a session by cannot key
// differently. The loader-resolved route builds its ref from
// Bundle.contentSourceRef(); the hooks/MCP resolvers read the trust key HERE
// rather than reconstructing one from a display name. Two constructions of
// one identity is exactly how a rejection recorded against one route stops
// withholding the other.
//
// Location-derived without exception: the readers stamp it, nothing a bundle
// DECLARES reaches it (outdated-recoil).
//
// The zero BundleRef means either an unclaimed read (r.Bundle == nil) or a
// reader that has not stamped this field yet; BundleRead.Claimed does not
// cover it.
func (r BundleRead) SourceRef() ident.BundleRef {
	if r.Bundle == nil {
		return ident.BundleRef{}
	}
	return r.Bundle.contentSourceRef()
}

// warnUnmintableSource reports a source ref that could not be minted into the
// canonical grammar, NAMING the string and the reason.
//
// It exists because the alternative was measured and cost 402 withheld items.
// Every stamp site used to write `if typed, err := mint(...); err == nil` and
// drop the error, so a ref the grammar refused produced a zero BundleRef, which
// produced an unaddressable item ref, which the pipeline WITHHELD — three
// layers from the cause, with the only evidence a %#v of an all-empty struct
// that named neither the bundle nor the string that failed.
//
// It warns rather than failing the read: withholding is already fail-closed, so
// the safe outcome is reached either way. What was missing was never safety, it
// was ATTRIBUTION.
func warnUnmintableSource(rep report.Reporter, source string, err error) {
	rep.Warnf("cannot address source %q: %v — items under it will be withheld", source, err)
}

// ItemRefFor mints the canonical "<source>#<kind>/<item>" reference an item's
// TrustRef is built from, and REFUSES a source it cannot address. The grammar
// lives in ident.ItemRef, so every producer that mints an item ref from a
// bundle's structured source — this package's own loaders, config's
// executable-surface extractors, managedhooks' profile gate — cannot drift on
// what an item ref is.
//
// Exported because config and managedhooks are the same kind of caller this
// package's own loaders are: each holds an ident.BundleRef (a BundleRead's
// SourceRef, or the structured counterpart of one) and needs the identical
// mint behavior, not a private copy of it.
//
// The error arm is reachable in exactly the shape AsBundleRef's doc describes:
// src is the zero BundleRef, which BundleRead.SourceRef reports as-is for a
// read whose typed source could not be minted (warnUnmintableSource has
// already named the string that failed, at the boundary where it failed). The
// caller drops THAT ITEM and keeps going: one unaddressable bundle costs its
// own items, never the rest of the assembly.
func ItemRefFor(src ident.BundleRef, kind ident.ItemKind, item string) (string, error) {
	return ident.ItemRef(src, kind, item)
}

// TrustCtx reports the only axis a gate keys on.
func (r BundleRead) TrustCtx() TrustCtx { return r.trustCtx }

// Claimed reports whether every axis of this read was actually populated by a
// reader.
//
// An unpopulated BundleRead claims nothing, and a consumer must treat it as a
// withhold rather than as "local, unsigned, no signer" — which is precisely the
// claim a zero value would otherwise make. This is the check that turns "zero
// means unset" from a comment into behaviour.
func (r BundleRead) Claimed() bool {
	return r.Bundle != nil && r.ref != "" && r.Provenance != ProvenanceUnset &&
		r.trustCtx != TrustCtxUnset
}

// newRead builds a read with its axes set. It is unexported because it is the
// only way the axes are ever populated: outside this package a read comes
// from a reader, so every value on it was established over the bytes rather
// than claimed by the caller.
//
// It also STAMPS the content trust key (Bundle.sourceRef) from ref when the
// caller left it empty, which makes that key location-derived for every read
// without exception. ref is the bundle's RESOLUTION identity, and resolution
// identity is decided by where the bundle was found — the path-relative name
// under the project tree, the lockfile's canonical ref, the companion's
// ctxloom:companion ref — never by the document's own `name:`. A key that fell
// back to Bundle.Name would take an input from the content being judged: a
// project bundle declaring `name: ctxloom:companion@ltk` would key as that
// companion.
//
// Only-when-empty, so a ref the caller already established deliberately wins.
// Every caller that reaches this fallback with sourceRefSet still false is, by
// construction, a genuinely local resolution ref — every non-local caller
// stamps sourceRef (and sourceRefSet) itself before calling newRead, so the
// only ones left unset here are localFSReader's project-provenance bundles.
// The stamp below is minted with ident.LocalRef accordingly, not re-derived by
// inspecting prov/tctx: another reader that reached this fallback for a
// non-local ref would be a bug in THAT reader, not something this function
// could detect from its own arguments.
//
// Gated on sourceRefSet, not on sourceRef's value: a reader whose mint FAILED
// still stamped sourceRefSet, and that failure (the zero BundleRef) must
// stick — checking sourceRef itself would be unable to tell "unmintable" from
// "untouched" and would silently paper over the failure as a local bundle of
// that name.
func newRead(ref string, b *Bundle, prov ProvenanceClass, tctx TrustCtx) BundleRead {
	if b != nil && !b.sourceRefSet {
		// The mint failure is not reported here: every reader stamps its
		// own ref (and reports an unmintable one at that site). A zero ref
		// still sticks, so a caller that reaches this arm with a bad ref is
		// withheld, not papered over as a local bundle of that name.
		typed, _ := ident.LocalRef(ref)
		b.sourceRef = typed
		b.sourceRefSet = true
	}
	return BundleRead{
		Bundle:     b,
		ref:        ref,
		Provenance: prov,
		trustCtx:   tctx,
	}
}

// ReaderOption configures a reader with something that is NEITHER its
// provenance NOR its trust context — those two are hard-coded by each
// constructor and are deliberately not expressible here.
type ReaderOption func(*readerConfig)

// readerConfig is the shared configurable state of the reader implementations.
type readerConfig struct {
	installDir string
	repoURL    string
	revision   string
	rep        report.Reporter // where this reader's user-facing diagnostics go
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

// WithReaderReporter names the sink a reader's user-facing diagnostics go to;
// without one they are discarded. Tests use it to read what the user was told.
func WithReaderReporter(sink report.Sink) ReaderOption {
	return func(c *readerConfig) { c.rep = report.To(sink) }
}

func newReaderConfig(opts []ReaderOption) readerConfig {
	var c readerConfig
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// warnOnce is warn for a diagnostic whose repetition is noise rather than
// information — the same line about the same companion in one process. The
// dedup is the process's business; the sink is still the caller's.
func (c readerConfig) warnOnce(format string, args ...any) {
	c.rep.WarnOncef(format, args...)
}
