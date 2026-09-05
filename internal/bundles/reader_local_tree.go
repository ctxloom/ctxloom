package bundles

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
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
	if filepath.Base(manifestPath) != DirectoryFormManifest {
		return nil, nil, nil
	}
	if len(inlineKeys(env)) > 0 {
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

// openLocalTree opens the directory holding manifestPath as a content.Bundle.
//
// The store is rooted at the bundle directory's PARENT and the id is that
// directory's base name, because a bundle id must be a single path segment
// (content.validateBundleID) — a nested authored bundle ("lang/go") is absorbed
// by the root rather than smuggled into the id. This is the same rooting rule
// config.treeBundleReader uses for an installed tree; the two must agree or a
// bundle would read differently depending on how it arrived.
func (r *localFSReader) openLocalTree(ctx context.Context, manifestPath string) (content.Bundle, error) {
	dir := filepath.Dir(manifestPath)
	prov, err := r.treeProvenance()
	if err != nil {
		return nil, err
	}
	tfs, err := content.NewAferoTreeFS(r.fsys, filepath.Dir(dir))
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
