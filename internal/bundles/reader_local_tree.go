package bundles

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/content"
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
func (r *localFSReader) readLocalTreeForm(ctx context.Context, manifestPath string, env *Bundle) (*Bundle, error) {
	if filepath.Base(manifestPath) != DirectoryFormManifest {
		return nil, nil
	}
	if len(inlineKeys(env)) > 0 {
		return nil, nil
	}
	tree, err := r.openLocalTree(ctx, manifestPath)
	if err != nil {
		return nil, err
	}
	refs, err := tree.Refs(ctx)
	if err != nil {
		return nil, fmt.Errorf("bundles: enumerating the tree at %s: %w", filepath.Dir(manifestPath), err)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	b, err := ReadTree(ctx, tree)
	if err != nil {
		return nil, fmt.Errorf("bundles: reading the tree at %s: %w", filepath.Dir(manifestPath), err)
	}
	return b, nil
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
