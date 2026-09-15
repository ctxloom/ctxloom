package bundles

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
	"github.com/ctxloom/ctxloom/internal/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// ReadRemoteRef reads the WHOLE bundle a canonical remote ref names at a pinned
// commit — both published shapes, and for a tree every item file, not just the
// manifest.
//
// # The loss it exists to remove
//
// A tree bundle's items live in files BESIDE its bundle.yaml, and readEnvelope
// refuses a tree manifest that declares any item inline. So the manifest alone
// is structurally incapable of carrying a single fragment, command, skill, mcp
// entry, hook or profile: parsing it yields an envelope with every item map
// empty. A caller that fetched a whole tree and then parsed only its manifest
// got a bundle that loaded, assembled and delivered nothing, with no error
// anywhere — which is what remote.FetchRefBytes hands back and why anything
// wanting the bundle must come here instead.
//
// # Why it lives in this package and not beside the fetch
//
// The composition needs the content layer, which sits ABOVE internal/remote and
// imports it (see remote.TreeFetchFunc). A tree-to-bundle sibling inside remote
// would be an import cycle. This package already imports remote, content,
// content/attest and signing, so the seam costs no new edge in either
// direction: remote returns remote types, and the layer that owns bundles turns
// them into one.
//
// # Verify before interpret
//
// The tree is verified against its manifest and its publisher BEFORE ReadTree
// sees it, mirroring what the local tree path already does in
// localFSReader.treeIntegrityFacts. One rule for both paths; nothing is
// interpreted before it is verified.
//
// Unlike the local path this FAILS rather than downgrading a fact, and the
// difference is not an inconsistency. Local content is trusted by LOCALITY — the
// author is the operator, so the check there is a diagnostic telling them their
// bytes and their manifest have parted company. Remote bytes have no such
// standing: they are a publisher's claim, and interpreting a tree's item files
// before establishing that the publisher signed them would newly expose
// unverified remote content to assembly.
func ReadRemoteRef(ctx context.Context, factory remote.FetcherFactory, auth remote.AuthConfig, ref *remote.Reference, sha string, treeFetch remote.TreeFetchFunc, root signing.TrustRoot) (*Bundle, error) {
	c, err := remote.FetchRef(ctx, factory, auth, ref, sha, treeFetch)
	if err != nil {
		return nil, err
	}
	if !c.IsTree() {
		// A single-file bundle IS its document: the bytes are the whole bundle,
		// and its publisher signature is the detached sibling the fetch path
		// already accounts for.
		return ParseBundle(c.Data)
	}

	// The bundle id is the tree root's last segment, the same rule openTreeAt
	// applies locally — a bundle id is one path segment, so a nested name is
	// absorbed by the root rather than smuggled into the id. The two must agree
	// or a bundle would read differently depending on how it arrived.
	id := path.Base(c.Root)
	tree, err := remotetree.OpenFetchedBundle(ctx, id, c.Tree, ref.URL)
	if err != nil {
		return nil, fmt.Errorf("bundles: opening remote tree bundle %s at %s: %w", c.Root, sha, err)
	}
	if err := verifyRemoteTree(ctx, tree, root, c.Root, sha); err != nil {
		return nil, err
	}
	b, err := ReadTree(ctx, tree)
	if err != nil {
		return nil, fmt.Errorf("bundles: reading remote tree bundle %s at %s: %w", c.Root, sha, err)
	}
	return b, nil
}

// verifyRemoteTree refuses a remote tree that its publisher did not attest, or
// whose files no longer match the manifest that attestation covers.
//
// The three outcomes stay APART rather than collapsing into BundleVerdict.OK().
// "Could not be read", "the manifest does not describe these files" and "nobody
// this machine trusts signed this" are three different things for an operator to
// do something about, and a single "untrusted" sentence sends all three to the
// same wrong remedy.
//
// Whole-bundle attestation is the only check worth making here. A bundle.yaml
// content_hash is an INDEX and never an authority, so verifying the manifest
// alone would assert nothing about the item bytes this read newly exposes.
func verifyRemoteTree(ctx context.Context, tree content.Bundle, root signing.TrustRoot, treeRoot, sha string) error {
	verdict, err := attest.VerifyBundle(ctx, tree, root, time.Now())
	if err != nil {
		return fmt.Errorf("bundles: refusing to read remote tree bundle %s at %s: it could not be checked against its manifest: %w", treeRoot, sha, err)
	}
	if verdict.Contents != nil {
		return fmt.Errorf("bundles: refusing to read remote tree bundle %s at %s: its files no longer match %s: %w",
			treeRoot, sha, content.ManifestPath, verdict.Contents)
	}
	// verdict.Verdict.OK(), not verdict.OK(): BundleVerdict.OK() folds in the
	// Contents check already reported above, and collapsing the two would send
	// a tree/manifest disagreement to the "nobody trusts this key" sentence.
	if !verdict.Verdict.OK() {
		return fmt.Errorf("bundles: refusing to read remote tree bundle %s at %s: %s — %s "+
			"(its item files would otherwise reach a session unverified; trust the publisher's key, or pin a commit they signed)",
			treeRoot, sha, verdict.Status, verdict.Detail)
	}
	return nil
}
