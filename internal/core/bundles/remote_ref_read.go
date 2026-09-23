package bundles

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// ReadRemoteRef reads the WHOLE bundle a canonical remote ref names at a pinned
// commit: every item file, not just the manifest.
//
// It is the ONE remote read path, and it is always verified: a bundle is a
// tree, so nothing reaches a session without attest.VerifyBundle having
// covered its bytes.
//
// # The loss it exists to remove
//
// A tree bundle's items live in files BESIDE its bundle.yaml, and readEnvelope
// refuses a tree manifest that declares any item inline. So the manifest alone
// is structurally incapable of carrying a single fragment, command, skill, mcp
// entry, hook or profile: parsing it yields an envelope with every item map
// empty. A caller that fetched a whole tree and then parsed only its manifest
// got a bundle that loaded, assembled and delivered nothing, with no error
// anywhere. Reading a tree's manifest alone is what this function exists to
// stop being possible.
//
// # Why it lives in this package and not beside the fetch
//
// The composition needs the content layer, which sits ABOVE internal/adapters/remote and
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
//
// It also returns what verification established — the release the publisher
// signed and who they are — so a caller about to move a pin has the version its
// floor is measured in, and the publisher it records, without a second read.
func ReadRemoteRef(ctx context.Context, factory remote.FetcherFactory, auth remote.AuthConfig, ref *remote.Reference, sha string, treeFetch remote.TreeFetchFunc, root trust.TrustRoot) (*Bundle, remote.Verified, error) {
	c, err := remote.FetchRef(ctx, factory, auth, ref, sha, treeFetch)
	if err != nil {
		return nil, remote.Verified{}, err
	}
	// The bundle id is the tree root's last segment, the same rule openTreeAt
	// applies locally — a bundle id is one path segment, so a nested name is
	// absorbed by the root rather than smuggled into the id. The two must agree
	// or a bundle would read differently depending on how it arrived. It is
	// the SERVED id: attest.VerifyBundle refuses a tree whose signed release
	// names another bundle, so a trusted tree cannot be re-homed under it.
	id := path.Base(c.Root)
	tree, err := remotetree.OpenFetchedBundle(ctx, id, c.Tree, ref.URL)
	if err != nil {
		return nil, remote.Verified{}, fmt.Errorf("bundles: opening remote tree bundle %s at %s: %w", c.Root, sha, err)
	}
	v, err := verifyRemoteTree(ctx, tree, root, c.Root, sha)
	if err != nil {
		return nil, remote.Verified{}, err
	}
	b, err := ReadTree(ctx, tree)
	if err != nil {
		return nil, remote.Verified{}, fmt.Errorf("bundles: reading remote tree bundle %s at %s: %w", c.Root, sha, err)
	}
	return b, v, nil
}

// TreeVerifier is the verifier a Puller runs over a fetched tree before it pins
// it (remote.WithTreeVerifier): the same check ReadRemoteRef makes, so a pull
// and a read refuse the same trees.
//
// The one difference is unattested content, which a read refuses and a pull
// admits: a pin of content nobody this machine trusts is still a pin, and the
// exposure gate withholds it for review. It is admitted with no floor.
func TreeVerifier(root trust.TrustRoot) remote.TreeVerifyFunc {
	return func(ctx context.Context, files map[string]remote.TreeFile, treeRoot, sha, repoURL string) (remote.Verified, error) {
		tree, err := remotetree.OpenFetchedBundle(ctx, path.Base(treeRoot), files, repoURL)
		if err != nil {
			return remote.Verified{}, fmt.Errorf("bundles: opening remote tree bundle %s at %s: %w", treeRoot, sha, err)
		}
		v, err := verifyRemoteTree(ctx, tree, root, treeRoot, sha)
		if errors.Is(err, ErrTreeUnattested) {
			return remote.Verified{}, nil
		}
		return v, err
	}
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
//
// A verified tree's bundle.yaml version must EQUAL the signed release version.
// bundle.yaml is what a human reads and the signed header is what the version
// floor enforces; a tree in which they differ would be displayed as one
// release and enforced as another.
func verifyRemoteTree(ctx context.Context, tree content.Bundle, root trust.TrustRoot, treeRoot, sha string) (remote.Verified, error) {
	// A nil trust root can decide nothing, so it must refuse BY NAME rather
	// than be handed to attest. Today it would survive by accident — an
	// unsigned tree is refused before resolvePublisher is ever reached — which
	// means the first SIGNED tree read through a nil root would be the thing
	// that discovered the gap, as a nil-interface panic rather than a verdict.
	if root == nil {
		return remote.Verified{}, fmt.Errorf("bundles: refusing to read remote tree bundle %s at %s: no trust root was supplied, "+
			"so nothing can say whether its publisher is trusted", treeRoot, sha)
	}
	verdict, err := attest.VerifyBundle(ctx, tree, root, time.Now())
	if err != nil {
		return remote.Verified{}, fmt.Errorf("bundles: refusing to read remote tree bundle %s at %s: it could not be checked against its manifest: %w", treeRoot, sha, err)
	}
	if verdict.Contents != nil {
		return remote.Verified{}, fmt.Errorf("%w: remote tree bundle %s at %s: its files no longer match %s: %v",
			ErrTreeBundleWithheld, treeRoot, sha, content.ManifestPath, verdict.Contents)
	}
	if verdict.Status == attest.StatusTampered {
		return remote.Verified{}, fmt.Errorf("%w: remote tree bundle %s at %s: %s", ErrTreeBundleWithheld, treeRoot, sha, verdict.Detail)
	}
	// verdict.Verdict.OK(), not verdict.OK(): BundleVerdict.OK() folds in the
	// Contents check already reported above, and collapsing the two would send
	// a tree/manifest disagreement to the "nobody trusts this key" sentence.
	if !verdict.Verdict.OK() {
		return remote.Verified{}, fmt.Errorf("%w: remote tree bundle %s at %s: %s — %s "+
			"(its item files would otherwise reach a session unverified; trust the publisher's key, or pin a commit they signed)",
			ErrTreeUnattested, treeRoot, sha, verdict.Status, verdict.Detail)
	}
	rel := verdict.Manifest.Release()
	env, err := readEnvelope(ctx, tree)
	if err != nil {
		return remote.Verified{}, fmt.Errorf("bundles: refusing to read remote tree bundle %s at %s: %w", treeRoot, sha, err)
	}
	if env.Version != rel.Version.String() {
		return remote.Verified{}, fmt.Errorf("%w: remote tree bundle %s at %s: its %s declares version %q but its publisher signed it as %s",
			ErrTreeBundleWithheld, treeRoot, sha, DirectoryFormManifest, env.Version, rel.Version)
	}
	return remote.Verified{Release: rel, Publisher: verdict.Principal}, nil
}

// ErrTreeUnattested is the pull walk's refusal of a remote tree nobody this
// machine trusts has signed: unsigned, or signed by a key the trust root does
// not know. It is a different fact from ErrTreeBundleWithheld — nothing was
// tampered with; nothing vouched for it either.
var ErrTreeUnattested = errors.New("bundles: refusing to read remote tree bundle: unattested")

// ManifestVerifier is the verifier the retraction check runs over the default
// branch's tip manifest (remote.WithManifestVerifier), through
// attest.VerifyManifest: a release retracts only when a key this trust root
// authorizes to publish signed it.
//
// A tampered manifest is an error; an unsigned or untrusted one is
// remote.Verified{} — both of which the retraction check treats as saying
// nothing, never as "clean".
func ManifestVerifier(root trust.TrustRoot) remote.ManifestVerifyFunc {
	return func(raw []byte, sigFiles map[string][]byte) (remote.Verified, error) {
		if root == nil {
			return remote.Verified{}, errors.New("bundles: no trust root was supplied, so no manifest signature can be judged")
		}
		sigs, err := manifestSigSet(sigFiles)
		if err != nil {
			return remote.Verified{}, err
		}
		m, v := attest.VerifyManifest(raw, sigs, root, time.Now())
		switch v.Status {
		case attest.StatusManifestSigned:
			return remote.Verified{Release: m.Release(), Publisher: v.Principal}, nil
		case attest.StatusTampered:
			return remote.Verified{}, fmt.Errorf("%w: tip manifest: %s", ErrTreeBundleWithheld, v.Detail)
		default:
			return remote.Verified{}, nil
		}
	}
}

// manifestSigSet files the fetched signature entries into an in-memory tree so
// the content layer — the one owner of the .sigs/ naming — decides which of
// them are signatures over the manifest.
func manifestSigSet(sigFiles map[string][]byte) (content.SigSet, error) {
	const id = "manifest"
	files := make(map[string][]byte, len(sigFiles))
	for name, data := range sigFiles {
		if name != path.Base(name) {
			continue
		}
		files[path.Join(id, content.SigDirName, name)] = data
	}
	if len(files) == 0 {
		return nil, nil
	}
	tfs, err := content.NewMapTreeFS(files)
	if err != nil {
		return nil, err
	}
	st, err := content.NewFSStore(tfs, content.Provenance{IsLocal: true})
	if err != nil {
		return nil, err
	}
	b, err := st.Open(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return b.BundleSignatures(context.Background())
}
