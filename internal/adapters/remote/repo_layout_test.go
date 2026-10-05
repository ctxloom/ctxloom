package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// The remote bundle layout is now DERIVED from paths.RepoBundlesPrefixFor
// rather than hand-built at each site. Derivation removes the drift between
// sites but removes nothing about the VALUE, and the value is a wire contract:
// it is the directory every already-published repo committed its bundles into,
// and every consumer's lockfile pins paths under it.
//
// So these tests pin the LITERAL bytes. An edit that changes where publishing
// writes has to change these lines, in a commit that says so — rather than
// silently relocating a corpus that no longer resolves. Format v2 holds only
// TREES, so a published item's leaf is the bundle's own directory name — no
// ".yaml" suffix — which is why these literals carry none.

// TestRepoLayout_LiteralPaths pins each accessor's exact repo-relative value.
func TestRepoLayout_LiteralPaths(t *testing.T) {
	assert.Equal(t, ".ctxloom/content/bundles/v2", RepoItemPrefix(ItemTypeBundle),
		"the tree publish/fetch prefix")
	assert.Equal(t, ".ctxloom/content/bundles", RepoItemRoot(ItemTypeBundle),
		"the listing walk root — the PARENT of every format root, which holds no bundles itself")
	assert.Equal(t, "bundles/v2", ContentItemPrefix(ItemTypeBundle),
		"the content-root-relative prefix a local ref resolves against")
	assert.NotContains(t, RepoItemRoots(ItemTypeBundle), RepoItemRoot(ItemTypeBundle),
		"no FORMAT root may be the listing root itself: a flat listing there finds directories, not bundles")
}

// TestPublishPath_LiteralPath pins the published path itself, not just its
// prefix — a bundle lands at <prefix>/<name>, its OWN tree root, with no
// extension: what travels there is a manifest plus item files, not one
// document.
func TestPublishPath_LiteralPath(t *testing.T) {
	assert.Equal(t, ".ctxloom/content/bundles/v2/go-tools",
		PublishPath(ItemTypeBundle, "go-tools"))
	assert.Equal(t, ".ctxloom/content/bundles/v2/lang/go/testing",
		PublishPath(ItemTypeBundle, "lang/go/testing"),
		"a path-addressed bundle keeps its subdirectories under the prefix")
}

// TestBuildFilePath_LiteralPath pins the fetch side's literal, both arms.
func TestBuildFilePath_LiteralPath(t *testing.T) {
	canonical, err := ParseReference("https://github.com/o/r@bundles/lang/go/testing")
	if err != nil {
		t.Fatalf("parse canonical ref: %v", err)
	}
	assert.Equal(t, ".ctxloom/content/bundles/v2/lang/go/testing",
		canonical.BuildFilePath(ItemTypeBundle))

	local := &Reference{IsLocal: true, ItemType: ItemTypeBundle, Path: "go-tools"}
	assert.Equal(t, "bundles/v2/go-tools", local.BuildFilePath(ItemTypeBundle),
		"a local ref resolves against an already-open content root, so it must NOT re-state it")
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

	published := PublishPath(ItemTypeBundle, name)
	ref := &Reference{URL: "https://github.com/o/r", ItemType: ItemTypeBundle, Path: name}
	assert.Equal(t, published, ref.BuildFilePath(ItemTypeBundle),
		"a fetch must look exactly where the publish wrote")

	root := RepoItemRoot(ItemTypeBundle)
	assert.True(t, len(published) > len(root)+1 && published[:len(root)+1] == root+"/",
		"the publish path %q must sit under the listing root %q, or nothing will ever list it", published, root)

	// Now reduce the published path the way a listing does — strip the root
	// (format v2 carries no extension, since the leaf is the tree's own
	// directory) — and confirm the bare name comes back.
	rel := published[len(root)+1:]
	assert.Equal(t, name, paths.TrimBundlesLayoutSegment(rel),
		"a listing of the just-published bundle must yield the name it was published under")
}
