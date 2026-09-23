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
)

// readLocalTree opens the LOCALLY AUTHORED tree whose envelope is at
// manifestPath and returns it together with the bundle it holds.
//
// # Why an item-less tree reads as its envelope rather than failing
//
// `ctxloom bundle create` scaffolds a tree before anything is in it, so a
// bundle.yaml with no item files beside it is an author's bundle mid-creation,
// not a truncated one. ReadTree refuses that shape because a PUBLISHED tree
// with no items delivers nothing to anyone; here the author is the consumer,
// and refusing would make every freshly created bundle unloadable. The
// envelope is still read through readEnvelope, so one that declares items
// inline is refused exactly as it is for a tree with files.
//
// Errors are never swallowed: every failure propagates.
func (r *localFSReader) readLocalTree(ctx context.Context, manifestPath string) (content.Bundle, *Bundle, error) {
	tree, err := r.openLocalTree(ctx, manifestPath)
	if err != nil {
		return nil, nil, err
	}
	b, err := readTreeOrEnvelope(ctx, tree)
	if err != nil {
		return nil, nil, fmt.Errorf("bundles: reading the tree at %s: %w", filepath.Dir(manifestPath), err)
	}
	return tree, b, nil
}

// readTreeOrEnvelope is ReadTree, except that a tree holding no items yet is
// its envelope — see readLocalTree for why an authored tree may be empty.
func readTreeOrEnvelope(ctx context.Context, tree content.Bundle) (*Bundle, error) {
	refs, err := tree.Refs(ctx)
	if err != nil {
		return nil, fmt.Errorf("enumerating: %w", err)
	}
	if len(refs) == 0 {
		return readEnvelope(ctx, tree)
	}
	return ReadTree(ctx, tree)
}

// treeSignatureFacts establishes a locally authored tree's signature axes from
// its ONE signature: the SHA256SUMS manifest and its .sigs/ entry, verified
// by attest.VerifyBundle — the same verifier the pull walk uses, so the two
// readers refuse the same things. A tree with no manifest is unsigned.
//
// A manifest that does not honestly cover the tree — a mutated or smuggled
// item file, a signature over other bytes — is INVALID, not absent, because a
// manifest that exists and does not describe the tree is a different fact
// from no manifest at all. Local content is trusted by LOCALITY, so an
// invalid signature never withholds a local tree (composite.Trust admits it
// as unsigned, with the stale-signature reason); it is the diagnostic that
// tells the author their bytes and their manifest have parted company.
func (r *localFSReader) treeSignatureFacts(ctx context.Context, tree content.Bundle) SignatureFacts {
	if _, err := tree.ReadFile(ctx, content.ManifestPath); err != nil {
		return SignatureFacts{Signature: SignatureNone, Signer: SignerNone}
	}
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
	case verdict.OK():
		return SignatureFacts{Signature: SignatureValid, Signer: SignerTrusted, Principal: verdict.Principal}
	case verdict.UntrustedSignerFingerprint != "":
		return SignatureFacts{Signature: SignatureValid, Signer: SignerUntrusted, Fingerprint: verdict.UntrustedSignerFingerprint}
	}
	return SignatureFacts{Signature: SignatureNone, Signer: SignerNone}
}

// invalidTreeFacts is the one shape a failed tree check reports: the signature
// axis is INVALID rather than absent, because a manifest that exists and does
// not describe the tree is a different fact from no manifest at all, and
// collapsing them loses which one happened.
func invalidTreeFacts(format string, args ...any) SignatureFacts {
	return SignatureFacts{
		Signature: SignatureInvalid,
		Signer:    SignerUntrusted,
		Detail:    fmt.Sprintf(format, args...),
	}
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
// publisher signature covers those exact bytes (spec §3.1), so a caller handed
// just one of the two goes back to the filesystem for the other — and then the file
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
	default:
		return content.Provenance{}, fmt.Errorf("bundles: no content provenance for reader class %v", r.provenance)
	}
}
