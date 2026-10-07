package bundles

import (
	"context"
	"fmt"
	"path"

	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
)

// ReadRemoteRef reads the WHOLE bundle a canonical remote ref names at a pinned
// commit: every item file, not just the manifest.
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
// would be an import cycle. This package already imports remote and content,
// so the seam costs no new edge in either direction: remote returns remote
// types, and the layer that owns bundles turns them into one.
func ReadRemoteRef(ctx context.Context, factory remote.FetcherFactory, auth remote.AuthConfig, ref *remote.Reference, sha string, treeFetch remote.TreeFetchFunc) (*Bundle, error) {
	c, err := remote.FetchRef(ctx, factory, auth, ref, sha, treeFetch)
	if err != nil {
		return nil, err
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
	b, err := ReadTree(ctx, tree)
	if err != nil {
		return nil, fmt.Errorf("bundles: reading remote tree bundle %s at %s: %w", c.Root, sha, err)
	}
	return b, nil
}
