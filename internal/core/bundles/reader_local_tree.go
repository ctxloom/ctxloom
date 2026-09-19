package bundles

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// readLocalTreeForm returns the bundle a LOCALLY AUTHORED tree-form directory
// holds, or (nil, nil) when the directory is not tree form and the caller
// should keep reading bundle.yaml as the whole document.
//
// # Why a form decision exists here at all
//
// Three shapes reach localFSReader, and exactly one of them is new:
//
//	<name>.yaml                     single-file document        (unchanged)
//	<name>/bundle.yaml + inline     retired inline directory    (unchanged)
//	<name>/bundle.yaml + item files TREE form                   (this function)
//
// The first two are decided WITHOUT opening a content store, and the order of
// the guards below is what guarantees it: a single-file document fails the
// manifest check, and an envelope that still declares items inline fails the
// inlineKeys check. Neither can reach the tree path, so neither can change
// behaviour because of anything here — the retired inline shape keeps loading
// exactly as it did, which is what lets the migration proceed one bundle at a
// time instead of as a flag day.
//
// This is NOT the refusal in readEnvelope being relaxed. That refusal is about
// a tree that holds item files AND inline keys — genuinely ambiguous, two
// answers for one item. Here an envelope with inline keys is not a tree at all;
// it is the old document form, and it is read as one.
//
// # Why an empty tree falls through rather than failing
//
// A directory bundle whose envelope declares nothing and whose tree holds no
// item files is today a metadata-only bundle that reads as an empty document.
// ReadTree refuses that shape outright ("declares no items"), so routing it here
// would turn a bundle that loads today into a hard failure. It is not tree form
// — nothing was migrated — so it keeps its current meaning.
//
// # Errors are never swallowed
//
// Once the shape IS tree form, every failure propagates. Falling back to the
// document read on error would parse an envelope that deliberately carries no
// items and hand back an empty bundle: the exit-0/zero-bytes shape this
// function exists to remove.
func (r *localFSReader) readLocalTreeForm(ctx context.Context, manifestPath string, env *Bundle) (content.Bundle, *Bundle, error) {
	if !treeFormEnvelope(manifestPath, env) {
		return nil, nil, nil
	}
	tree, err := r.openLocalTree(ctx, manifestPath)
	if err != nil {
		return nil, nil, err
	}
	refs, err := tree.Refs(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("bundles: enumerating the tree at %s: %w", filepath.Dir(manifestPath), err)
	}
	if len(refs) == 0 {
		return nil, nil, nil
	}
	b, err := ReadTree(ctx, tree)
	if err != nil {
		return nil, nil, fmt.Errorf("bundles: reading the tree at %s: %w", filepath.Dir(manifestPath), err)
	}
	return tree, b, nil
}

// treeIntegrityFacts folds a locally authored TREE's manifest into the
// signature facts its envelope sibling established.
//
// # The hole this closes
//
// signatureFactsFor checks `bundle.yaml.sig` against the raw bytes of
// `bundle.yaml`. For a tree that envelope declares NO ITEMS — every fragment,
// command and skill lives in a file beside it — so the sibling signature covers
// a document whose entire payload is elsewhere. Mutating an item file left the
// bundle reporting SignatureValid, with the SHA256SUMS manifest that would have
// caught it sitting on disk unread.
//
// # Why the two are COMPOSED rather than one replacing the other
//
// They cover different bytes and both must hold. The manifest covers the item
// files (and, because operations.signBundleTree writes the sibling FIRST, the
// envelope and its signature too); the sibling covers the envelope. Taking the
// manifest's answer alone would discard the envelope fact for trees carrying no
// manifest, which is every directory bundle authored before this; taking the
// sibling's alone is the hole above. So the envelope's answer stands and the
// manifest can only DOWNGRADE it.
//
// # Why an unsigned manifest still decides
//
// attest.VerifyBundle computes Contents — the tree-against-manifest check, in
// both directions — whenever a manifest exists at all, independently of whether
// anything signed or trusts it. Local content is trusted by LOCALITY, so these
// facts never gate a load; they are the diagnostic that tells an author their
// bytes and their manifest have parted company, which is worth reporting
// whether or not a key was involved.
func (r *localFSReader) treeIntegrityFacts(ctx context.Context, tree content.Bundle, envelope signatureFacts) signatureFacts {
	verdict, err := attest.VerifyBundle(ctx, tree, r.trustRoot(), time.Now())
	switch {
	case err != nil:
		return invalidTreeFacts("its tree could not be checked against its manifest: %v", err)
	case verdict.Contents != nil:
		// The item-file mutation. Reported against the manifest by name so the
		// remedy — re-sign the tree — is the obvious next move.
		return invalidTreeFacts("its files no longer match %s: %v", content.ManifestPath, verdict.Contents)
	case verdict.Status == attest.StatusTampered:
		return invalidTreeFacts("its %s is present but does not honestly cover this tree: %s", content.ManifestPath, verdict.Detail)
	}
	return envelope
}

// invalidTreeFacts is the one shape a failed tree check reports: the signature
// axis is INVALID rather than absent, because a manifest that exists and does
// not describe the tree is a different fact from no manifest at all, and
// collapsing them loses which one happened.
func invalidTreeFacts(format string, args ...any) signatureFacts {
	return signatureFacts{
		signature: SignatureInvalid,
		signer:    SignerUntrusted,
		detail:    fmt.Sprintf(format, args...),
	}
}

// treeFormEnvelope is the ONE rule for "is this bundle tree form": a
// directory-form envelope that declares NO items inline, so its payload is in
// files beside it.
//
// It is shared by the read path above and by IsTreeFormBundle below because a
// second copy of this rule is how a writer comes to disagree with the reader
// about what it is writing — which is the defect that made ctxloom's only
// directory-form authoring verb produce the shape its own reader refuses.
func treeFormEnvelope(manifestPath string, env *Bundle) bool {
	return filepath.Base(manifestPath) == DirectoryFormManifest && len(inlineKeys(env)) == 0
}

// BundleLayoutFor reports which FORMAT root a bundle document belongs in, given
// the path of that document and its PARSED-BUT-NOT-READ envelope.
//
// Placement follows FORMAT, not file shape. A directory is not by itself v2: a
// directory whose bundle.yaml still declares inline item keys is a format-v1
// bundle in a directory wrapper, and filing it under v2 says a migration
// happened that did not. So this defers to treeFormEnvelope — the same
// predicate the READ path uses to decide whether to open a tree — rather than
// asking whether the entry is a directory.
//
// It takes the envelope rather than a loaded *Bundle for the reason
// IsTreeFormBundle spells out: a tree that has already been read carries its
// items in the same maps an inline bundle does, so a loaded value cannot answer
// this. Only the document's own bytes can.
func BundleLayoutFor(docPath string, env *Bundle) paths.BundleLayout {
	if treeFormEnvelope(docPath, env) {
		return paths.LayoutV2
	}
	return paths.LayoutV2
}

// ErrEnvelopeRead and ErrEnvelopeParse classify the two ways EnvelopeAt fails.
//
// They exist so a caller can keep its own diagnosis of an unreadable versus an
// unparsable document without re-deriving where the bytes came from: `import`
// tells the user their source file is missing, `push` that the bundle it was
// pointed at cannot be read. Branching on those needs the STAGE, and a caller
// that cannot ask for the stage ends up reading the file itself to find out —
// which is the duplication this seam removes.
var (
	ErrEnvelopeRead  = errors.New("bundles: reading the envelope")
	ErrEnvelopeParse = errors.New("bundles: parsing the envelope")
)

// EnvelopeAt reads the bundle DOCUMENT at path and parses it, returning the
// document's exact bytes alongside the parsed-but-NOT-read envelope.
//
// This is the one place a bundle document is turned into a *Bundle from a PATH.
// Every caller needs the same two things together and for the same reason: the
// envelope to judge the bundle by, and the bytes it was judged from. A
// publisher signature covers those exact bytes (spec §3.1) and BundleLayoutFor
// answers only from an envelope that has not been read, so a caller handed just
// one of the two goes back to the filesystem for the other — and then the file
// on disk has been read twice, with nothing making the two reads agree.
//
// It is deliberately NOT a Reader. A Reader enumerates everything one SOURCE
// holds; this answers for a single document at a path the user named, which may
// sit outside any store at all (an import source, a move destination).
//
// On a PARSE failure the bytes are still returned: the read succeeded, and a
// caller that has its own verdict on the raw document must be able to reach it
// without going back to the filesystem. `push` is the live case — it refuses a
// zero-byte file by name before it will call the same file unparsable — and
// withholding the bytes there would silently retire that refusal in favour of a
// parse error, which is a worse sentence about a file the user can fix.
func EnvelopeAt(fsys afero.Fs, path string) ([]byte, *Bundle, error) {
	data, err := afero.ReadFile(fsys, path)
	if err != nil {
		return nil, nil, fmt.Errorf("%w at %s: %w", ErrEnvelopeRead, path, err)
	}
	env, err := ParseBundle(data)
	if err != nil {
		return data, nil, fmt.Errorf("%w at %s: %w", ErrEnvelopeParse, path, err)
	}
	return data, env, nil
}

// IsTreeFormBundle reports whether the bundle whose envelope sits at
// manifestPath is TREE form, reading the envelope and the tree from fsys.
//
// It re-reads rather than inspecting a loaded *Bundle on purpose: a tree bundle
// that has already been READ carries its items in the same maps an inline
// bundle does — that is the whole point of the read — so a loaded value cannot
// answer this question. Only the bytes on disk can.
//
// It applies BOTH halves of the rule, exactly as the read path does. The second
// half is the one that is easy to drop: a directory bundle whose envelope
// declares nothing AND whose tree holds no item files is a METADATA-ONLY
// bundle, not a tree — nothing was migrated — and calling it a tree would make
// every brand-new directory bundle claim a form it has no content in.
func IsTreeFormBundle(ctx context.Context, fsys afero.Fs, manifestPath string) (bool, error) {
	_, env, err := EnvelopeAt(fsys, manifestPath)
	if err != nil {
		return false, err
	}
	if !treeFormEnvelope(manifestPath, env) {
		return false, nil
	}
	tree, err := openTreeAt(ctx, fsys, manifestPath, content.Provenance{IsLocal: true})
	if err != nil {
		return false, err
	}
	refs, err := tree.Refs(ctx)
	if err != nil {
		return false, fmt.Errorf("bundles: enumerating the tree at %s: %w", filepath.Dir(manifestPath), err)
	}
	return len(refs) > 0, nil
}

// openLocalTree opens the directory holding manifestPath as a content.Bundle.
//
// The store is rooted at the bundle directory's PARENT and the id is that
// directory's base name, because a bundle id must be a single path segment
// (content.validateBundleID) — a nested authored bundle ("lang/go") is absorbed
// by the root rather than smuggled into the id. This is the same rooting rule
// config.treeBundleReader uses for an installed tree; the two must agree or a
// bundle would read differently depending on how it arrived.
func (r *localFSReader) openLocalTree(ctx context.Context, manifestPath string) (content.Bundle, error) {
	prov, err := r.treeProvenance()
	if err != nil {
		return nil, err
	}
	return openTreeAt(ctx, r.fsys, manifestPath, prov)
}

// openTreeAt opens the directory holding manifestPath as a content.Bundle, on
// any filesystem. It is the ONE place the rooting rule lives, so a writer and a
// reader cannot disagree about which directory a bundle id names.
func openTreeAt(ctx context.Context, fsys afero.Fs, manifestPath string, prov content.Provenance) (content.Bundle, error) {
	dir := filepath.Dir(manifestPath)
	tfs, err := content.NewAferoTreeFS(fsys, filepath.Dir(dir))
	if err != nil {
		return nil, fmt.Errorf("bundles: opening the tree at %s: %w", dir, err)
	}
	store, err := content.NewFSStore(tfs, prov)
	if err != nil {
		return nil, fmt.Errorf("bundles: opening the tree at %s: %w", dir, err)
	}
	tree, err := store.Open(ctx, content.BundleID(filepath.Base(dir)))
	if err != nil {
		return nil, fmt.Errorf("bundles: %s is not readable as a tree bundle: %w", dir, err)
	}
	return tree, nil
}

// treeProvenance translates this reader's hard-coded class into the one the
// content store demands, because content.Provenance REJECTS its own zero value
// and a store cannot be opened without it.
//
// WHAT IT DOES NOT DO, measured rather than assumed: it does not decide this
// bundle's trust identity. The store stamps the value onto every trust.Ref it
// enumerates, but ReadTree folds items by SURFACE and reads nothing off those
// refs except a kind name in an error message, so the stamps are discarded
// before a bundle exists. Trust identity is established where it is for every
// other form — newRead, from r.provenance, in readBundle. A mutation flipping
// the two arms below therefore changes no observable behaviour, and that is a
// property of this path, not a gap in its tests.
//
// The default arm still FAILS rather than picking a class. It guards a third
// reader class arriving later, when the value may well stop being inert;
// defaulting would answer that question silently and wrongly.
func (r *localFSReader) treeProvenance() (content.Provenance, error) {
	switch r.provenance {
	case ProvenanceProject:
		return content.Provenance{IsLocal: true}, nil
	case ProvenanceBuiltin:
		return content.Provenance{IsBuiltin: true}, nil
	default:
		return content.Provenance{}, fmt.Errorf("bundles: no content provenance for reader class %v", r.provenance)
	}
}
