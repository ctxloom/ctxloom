package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// The remote bundle layout is now DERIVED from paths.RepoBundlesPrefixFor
// rather than hand-built at each site. Derivation removes the drift between
// sites but removes nothing about the VALUE, and the value is a wire contract:
// it is the directory every already-published repo committed its bundles into,
// and every consumer's lockfile pins paths under it.
//
// So these tests pin the LITERAL bytes. An edit that changes where publishing
// writes has to change these lines, in a commit that says so — rather than
// silently relocating a corpus that no longer resolves. The v2 segment below IS
// such an edit: RepoItemPrefix/ContentItemPrefix now resolve LayoutV2, the tree
// format, leaving v1 reachable only through the multi-root probes.

// TestRepoLayout_LiteralPaths pins each accessor's exact repo-relative value.
func TestRepoLayout_LiteralPaths(t *testing.T) {
	assert.Equal(t, ".ctxloom/content/bundles/v2", RepoItemPrefix(ItemTypeBundle),
		"the single-file publish/fetch prefix")
	assert.Equal(t, ".ctxloom/content/bundles", RepoItemRoot(ItemTypeBundle),
		"the listing walk root — the PARENT of every format root, which holds no bundles itself")
	assert.Equal(t, "bundles/v2", ContentItemPrefix(ItemTypeBundle),
		"the content-root-relative prefix a local ref resolves against")
	assert.NotContains(t, RepoItemRoots(ItemTypeBundle), RepoItemRoot(ItemTypeBundle),
		"no FORMAT root may be the listing root itself: a flat listing there finds directories, not bundles")
}

// TestPublishPath_LiteralPath pins the published file path itself, not just its
// prefix — a bundle lands at <prefix>/<name>.yaml and the suffix is as much a
// part of the contract as the directory.
func TestPublishPath_LiteralPath(t *testing.T) {
	assert.Equal(t, ".ctxloom/content/bundles/v2/go-tools.yaml",
		PublishPath(ItemTypeBundle, "go-tools", false))
	assert.Equal(t, ".ctxloom/content/bundles/v2/lang/go/testing.yaml",
		PublishPath(ItemTypeBundle, "lang/go/testing", false),
		"a path-addressed bundle keeps its subdirectories under the prefix")
}

// TestPublishPath_Tree_LiteralPath pins the DIRECTORY-form publish path: a
// tree's root has no ".yaml" suffix, because what lands there is a directory
// of files rather than one document.
func TestPublishPath_Tree_LiteralPath(t *testing.T) {
	assert.Equal(t, ".ctxloom/content/bundles/v2/atelier",
		PublishPath(ItemTypeBundle, "atelier", true))
}

// TestBuildFilePath_LiteralPath pins the fetch side's literal, both arms.
func TestBuildFilePath_LiteralPath(t *testing.T) {
	canonical, err := ParseReference("https://github.com/o/r@bundles/lang/go/testing")
	if err != nil {
		t.Fatalf("parse canonical ref: %v", err)
	}
	assert.Equal(t, ".ctxloom/content/bundles/v2/lang/go/testing.yaml",
		canonical.BuildFilePath(ItemTypeBundle))

	local := &Reference{IsLocal: true, ItemType: ItemTypeBundle, Path: "go-tools"}
	assert.Equal(t, "bundles/v2/go-tools.yaml", local.BuildFilePath(ItemTypeBundle),
		"a local ref resolves against an already-open content root, so it must NOT re-state it")
}

// TestRepoItemName_ReturnsBareNames is the listing half.
//
// Every listing site walks RepoItemRoot and reduces the walked path with
// RepoItemName. What that reduction must guarantee is that the name a listing
// yields is the name a consumer can ASK FOR — never a layout-qualified one. The
// guarantee is asserted against the live layout segments rather than against
// literal strings, so it keeps holding as layouts are added or renamed.
func TestRepoItemName_ReturnsBareNames(t *testing.T) {
	t.Run("an unsegmented name is returned unchanged", func(t *testing.T) {
		assert.Equal(t, "lang/go/testing", RepoItemName(ItemTypeBundle, "lang/go/testing"))
	})

	for _, l := range []paths.BundleLayout{paths.LayoutV1, paths.LayoutV2} {
		t.Run("a name under "+l.String()+" loses its segment", func(t *testing.T) {
			seg, err := l.Segment()
			if err != nil {
				t.Fatalf("segment for %s: %v", l, err)
			}
			// Build the listed name exactly as a walk of RepoItemRoot would:
			// the layout's own prefix, minus the root, plus the item name.
			listed := "lang/go/testing"
			if seg != "" {
				listed = seg + "/" + listed
			}
			assert.Equal(t, "lang/go/testing", RepoItemName(ItemTypeBundle, listed),
				"a listing must yield the name a consumer asks for, not one qualified by the layout it happened to be stored in")
		})
	}
}

// TestRepoItemName_KeepsANameThatMerelyLooksLikeASegment guards the trim's one
// dangerous edge: an item whose whole name equals a layout segment. Trimming it
// would reduce a real bundle to the empty name.
func TestRepoItemName_KeepsANameThatMerelyLooksLikeASegment(t *testing.T) {
	for _, l := range []paths.BundleLayout{paths.LayoutV1, paths.LayoutV2} {
		seg, err := l.Segment()
		if err != nil || seg == "" {
			continue
		}
		assert.Equal(t, seg, RepoItemName(ItemTypeBundle, seg),
			"a bundle NAMED %q is not a layout root and must survive the reduction", seg)
	}
}

// TestRepoLayout_PublishFetchAndListingAgree is the property the whole seam
// exists for, asserted against the accessors rather than against literals — so
// it still holds after the layout moves, which is exactly when it matters.
//
// Publish writes under RepoItemPrefix; a listing walks RepoItemRoot. If the
// prefix ever escapes the root, a published bundle is invisible to every
// listing, and `sign --all`, prune and browse all silently see nothing.
func TestRepoLayout_PublishFetchAndListingAgree(t *testing.T) {
	const name = "lang/go/testing"

	published := PublishPath(ItemTypeBundle, name, false)
	ref := &Reference{URL: "https://github.com/o/r", ItemType: ItemTypeBundle, Path: name}
	assert.Equal(t, published, ref.BuildFilePath(ItemTypeBundle),
		"a fetch must look exactly where the publish wrote")

	root := RepoItemRoot(ItemTypeBundle)
	assert.True(t, len(published) > len(root)+1 && published[:len(root)+1] == root+"/",
		"the publish path %q must sit under the listing root %q, or nothing will ever list it", published, root)

	// Now reduce the published path the way a listing does — strip the root and
	// the .yaml — and confirm the bare name comes back.
	rel := published[len(root)+1 : len(published)-len(".yaml")]
	assert.Equal(t, name, RepoItemName(ItemTypeBundle, rel),
		"a listing of the just-published bundle must yield the name it was published under")
}
