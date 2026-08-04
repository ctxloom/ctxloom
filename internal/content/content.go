// Package content is the storage-agnostic access surface for ctxloom bundle
// contents: the L0 foundation the signing, trust, distillation, search and
// materialize layers are meant to be built ON TOP OF rather than beside.
//
// The shape is four nested interfaces — Store -> Bundle -> Item -> Form — plus
// a registry of SurfaceType values that own their own on-disk recognition and
// codec. Nothing in Store/Bundle/Item/Form branches on an item kind: every
// kind-specific fact (which directory, which extension, how metadata is
// stored, how a path becomes a ref) lives in that kind's registered
// SurfaceType. A new kind therefore plugs in by calling Register, with no edit
// to this file.
//
// # An Item is addressable; a Form is attestable
//
// Signatures and approvals key on (assertion, kind, form, bytes), so the
// bytes, the components and the signatures all hang off Form — never off Item.
// A fragment with a published distilled rewrite has ONE Item and TWO Forms,
// stored as two sibling files, and the existing rule that "blessing the raw
// form can never validate a distilled exposure" therefore falls out of the
// storage model instead of needing enforcement in code.
//
// # Content is ALWAYS a digest, never raw bytes
//
// Form.Content returns a deterministic SHA256SUMS-shaped manifest of the
// form's components, even when there is exactly one component. Raw bytes are
// reached ONLY through Form.Components. There is one rule and no single-file
// special case, because a special case is a second code path that the
// multi-component path's tests never cover. See digest.go for the format and
// the reasons no mode bit appears in it.
//
// # What this package deliberately does NOT do
//
// It does not sign, verify, approve or reject anything: Form.Signatures and
// Bundle.BundleSignatures hand back signature BYTES and nothing else, because
// layer 0 knows only WHERE signature bytes live and layer 2 owns what they
// mean. It does not read or write bundle.yaml, and no existing consumer is
// wired to it yet — the tree implementation here reads a layout that no shipped
// bundle currently uses.
//
// # The manifest (layer 1) lives here; trust (layer 2) does not
//
// manifest.go adds the bundle-level manifest — Bundle.Manifest,
// Writer.PutManifest, BuildManifest and Manifest.VerifyContents. It is layer 1,
// integrity only: it answers "is this tree exactly the set of files, with
// exactly the bytes, that the manifest claims", in both directions, and it
// answers nothing at all about WHO said so. That question belongs to layer 2,
// which lives in the attest subpackage so that this package never imports a
// trust root.
package content

import (
	"context"
	"errors"
	"sort"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// BundleID identifies one bundle within a Store. For the tree implementation
// it is the bundle directory's name, which is also trust.Ref.Bundle — the
// bundle component of every ref keyed in the countersignature stores.
type BundleID string

// Store is the access surface, independent of where the bytes live. The tree
// implementation in this package reads an authored directory tree off an
// afero.Fs; the backends this interface exists to also span, and which are NOT
// implemented here, are remote-git-at-a-pinned-SHA (bytes-only, and pointedly
// NOT an afero.Fs), a packed archive, and resources embedded in the binary.
//
// Adding one of those must not require changing this interface — which is why
// SurfaceType.Detect and SurfaceType.Decode take a Source rather than a
// filesystem.
type Store interface {
	// Bundles lists every bundle in the store, in a deterministic order.
	Bundles(ctx context.Context) ([]BundleID, error)
	// Open returns the named bundle, or ErrNotFound.
	Open(ctx context.Context, id BundleID) (Bundle, error)
}

// Bundle is one bundle's contents.
type Bundle interface {
	ID() BundleID
	// Refs enumerates the bundle's items, in a deterministic order. An empty
	// kinds list means every registered kind; otherwise only the named kinds
	// are enumerated.
	Refs(ctx context.Context, kinds ...trust.ItemKind) ([]trust.Ref, error)
	// Item resolves one ref, or returns ErrNotFound. Resolution is by PATH,
	// not by a lookup in a parsed document: ref.Kind.Dir() names the
	// directory and ref.Name names the entry within it.
	Item(ctx context.Context, ref trust.Ref) (Item, error)

	// Files enumerates EVERY file in the bundle, bundle-relative and sorted,
	// including dot-prefixed files, the manifest, and the signature store.
	//
	// It is deliberately total and deliberately NOT item-scoped. Refs answers
	// "what items are here"; a file that no SurfaceType claims is invisible to
	// it by construction, and that invisibility is precisely the channel a
	// hostile publisher uses — add a directory nothing enumerates and it rides
	// along through every pull, move and materialize. Files is the primitive
	// that makes "this tree is exactly what was published" checkable at all.
	// It applies no exemption policy of its own: exemptions are stated once, in
	// ManifestCovers, where they can be read.
	Files(ctx context.Context) ([]string, error)

	// ReadFile returns one file's exact stored bytes, by bundle-relative path.
	//
	// It exists so integrity and signing can hash arbitrary NON-ITEM files —
	// the README, the LICENSE, the file a typo left unrecognised — without
	// reaching around the store to a filesystem. A layer that had Files but not
	// ReadFile could name what it must cover and not cover it.
	ReadFile(ctx context.Context, relPath string) ([]byte, error)

	// Manifest reads and parses the bundle's manifest, or returns
	// ErrManifestMissing. The manifest is a BUNDLE-LEVEL object, not an item:
	// it enumerates items and is not one of them.
	Manifest(ctx context.Context) (Manifest, error)

	// BundleSignatures returns the signature BYTES filed against the bundle's
	// manifest, and says nothing about whether any of them verify — the same
	// division of labour Form.Signatures follows.
	BundleSignatures(ctx context.Context) (SigSet, error)
}

// Item is one addressable item. It carries no bytes of its own: the attestable
// unit is (item, form), so bytes live on Form.
type Item interface {
	Ref() trust.Ref
	// Surface returns the decoded, typed representation of the WHOLE item —
	// every form it carries. Use As to recover the concrete type.
	Surface(ctx context.Context) (Surface, error)
	// Forms reports exactly the LAYOUT forms this item actually has on disk. A
	// fragment with no distilled sibling reports only FormRaw, and so does a
	// single-form surface such as an mcp server or a hook.
	Forms(ctx context.Context) ([]signing.Form, error)
	// Form returns one form, or ErrNoSuchForm when the item does not carry it.
	Form(ctx context.Context, f signing.Form) (Form, error)
}

// Form is one attestable materialization of an item.
type Form interface {
	ContentForm() signing.Form
	// Content is ALWAYS the deterministic component digest — never raw bytes,
	// not even when there is a single component. See digest.go.
	Content(ctx context.Context) ([]byte, error)
	// Components returns every component of this form, sorted by path, with
	// its bytes. Every component returned here is covered by Content: there is
	// no partial-coverage tier, because a partial manifest would mean adding a
	// file to a signed tree could not break verification.
	Components(ctx context.Context) ([]Component, error)
	// Signatures returns the signature BYTES stored against this form's
	// content, and says nothing about whether any of them verify.
	Signatures(ctx context.Context) (SigSet, error)
	// Surface returns the decoded surface of the item this form belongs to.
	//
	// DIVERGENCE from the brief, which specifies both `As[T Surface](ctx, f
	// Form)` and Surface only on Item: As cannot reach a Surface from a Form
	// unless Form exposes one. Given that a Form is the unit consumers hold,
	// reaching its surface from it is what callers want anyway.
	Surface(ctx context.Context) (Surface, error)
}

// Writer is the mutating half, deliberately a separate interface: a
// pinned-remote store is read-only by construction and simply does not
// implement it.
type Writer interface {
	// Put writes the components of s that belong to form f. A surface decoded
	// with two forms therefore writes one form per call, and writing the
	// distilled form never rewrites the raw file.
	Put(ctx context.Context, ref trust.Ref, f signing.Form, s Surface) error
	// Delete removes every component of the item, in every form — the content
	// file AND its metadata sidecar. It deliberately does NOT delete stored
	// signatures: signatures are keyed by content hash precisely so that they
	// outlive the file, and a rejection that could be dropped by deleting a
	// file would let you un-blacklist content by removing it.
	Delete(ctx context.Context, ref trust.Ref) error
	// PutSignature stores signature bytes against the given form's content
	// under the given namespace. It performs no verification.
	PutSignature(ctx context.Context, ref trust.Ref, f signing.Form, ns Namespace, sig []byte) error

	// PutManifest writes a bundle's manifest, replacing any existing one. It
	// takes a BundleID rather than a ref because a manifest belongs to the
	// bundle, not to any item in it.
	PutManifest(ctx context.Context, id BundleID, m Manifest) error

	// PutRootFile writes one bundle-ROOT file that is not an item: the bundle
	// envelope, a README, a LICENSE. It is the write-side counterpart to
	// Bundle.ReadFile, added for the same stated reason ReadFile was — a caller
	// reaching around the store to a filesystem is the signal this API is
	// incomplete.
	//
	// It refuses a path with a directory component. Everything below the root is
	// either a kind directory, where an item must go through Put so its surface
	// type does the encoding, or the signature store, which PutBundleSignature
	// owns. Without that guard this would be a way to place an unrecognised file
	// inside a kind directory — which Refs then fails on, turning a write here
	// into a bundle nobody can enumerate.
	PutRootFile(ctx context.Context, id BundleID, name string, data []byte) error

	// PutBundleSignature stores signature bytes over the bundle's manifest
	// under the given namespace. It performs no verification, and it does not
	// check that a manifest is present: layer 0 stores bytes, layer 2 decides
	// what they mean.
	PutBundleSignature(ctx context.Context, id BundleID, ns Namespace, sig []byte) error
}

// ComponentMode is a component's declared, attested mode. It is ONE enum
// rather than a boolean so a further value (readonly, say) can be added
// without a schema break, and it replaces the rejected prompt/executable/asset
// role taxonomy.
//
// It governs what materialize sets on disk and what review emphasises. It
// never governs what the digest covers — coverage is total. And it is DECLARED
// metadata, read from the item's signed bytes rather than from the
// filesystem's mode bits, because a filesystem mode is not portable: on
// Windows Go toggles only the read-only bit, so a mode-bearing digest would be
// platform-dependent and a checkout there would fail its own signature.
type ComponentMode string

const (
	// ModeRegular is a plain file — the default for anything not declared
	// executable.
	ModeRegular ComponentMode = "regular"
	// ModeExecutable is a file whose exec bit is load-bearing (a skill's
	// scripts/ entries).
	ModeExecutable ComponentMode = "executable"
)

// Component is one file of one form of one item.
type Component struct {
	// Path is bundle-relative and always uses forward slashes, e.g.
	// "fragments/solid.md". It is also the path that appears in the digest, so
	// Content can be checked against a bundle root with stock `sha256sum -c`.
	Path string
	// Mode is the declared mode (see ComponentMode).
	Mode ComponentMode
	// Bytes is the component's exact stored bytes.
	Bytes []byte
}

// Namespace is a signature's assertion namespace — the same byte string the
// signing package uses (signing.NamespacePublish and friends). It is a
// filename component in the tree's signature store, so it is validated against
// a conservative charset.
type Namespace string

// Signature is one stored signature: its namespace and its bytes. Nothing here
// claims it verifies.
type Signature struct {
	Namespace Namespace
	Bytes     []byte
}

// SigSet is every signature stored against one form's content, sorted for
// determinism.
type SigSet []Signature

// ForNamespace returns the signature bytes stored under ns, in SigSet order.
func (s SigSet) ForNamespace(ns Namespace) [][]byte {
	var out [][]byte
	for _, sig := range s {
		if sig.Namespace == ns {
			out = append(out, sig.Bytes)
		}
	}
	return out
}

// sortSigs orders a SigSet by namespace, then by bytes, so repeated reads of an
// unchanged store return byte-identical results.
func sortSigs(s SigSet) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Namespace != s[j].Namespace {
			return s[i].Namespace < s[j].Namespace
		}
		return string(s[i].Bytes) < string(s[j].Bytes)
	})
}

// The package's error vocabulary. Callers match with errors.Is.
var (
	// ErrNotFound reports a bundle or item that does not exist.
	ErrNotFound = errors.New("content: not found")
	// ErrNoSuchForm reports a form the item does not carry — asking a
	// never-distilled fragment for FormDistilled, for instance.
	ErrNoSuchForm = errors.New("content: no such form")
	// ErrUnrecognized reports a path no registered SurfaceType claims.
	ErrUnrecognized = errors.New("content: unrecognized item")
	// ErrSurfaceType reports a Surface handed to the wrong SurfaceType, or a
	// surface whose kind has no registered type.
	ErrSurfaceType = errors.New("content: wrong surface type")
	// ErrBadPath reports a component path that cannot be represented safely —
	// one that escapes its bundle, or that the digest format cannot encode.
	ErrBadPath = errors.New("content: invalid component path")
)
