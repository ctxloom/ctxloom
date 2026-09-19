//go:build arch

// A BUNDLE ITEM'S PATH COMPOSES ITS LEAF FROM THE LAYOUT, IN ONE PLACE.
//
// A bundles prefix is layout-qualified (".ctxloom/content/bundles/v1"), and the
// LEAF beneath it is decided by that same layout: v1 is the single-file
// document form and its leaf carries ".yaml"; v2 holds true trees and its leaf
// is the directory name. paths.BundleLayout.ItemFileName is the only place that
// says so, and remote.RepoItemPath / remote.ContentItemPath are the only things
// that compose a prefix with it.
//
// WHY THIS IS A GATE AND NOT A CONVENTION. Taking the bare prefix and appending
// a leaf by hand is a SECOND decision about a fact the layout already owns, and
// two sources of one fact can disagree. They did, and it shipped: the prefix was
// pointed at v2 while four call sites still appended ".yaml", so the resolver
// asked for bundles/v2/<name>.yaml — a file that cannot exist under a layout
// holding only directories. Every remote bundle failed to load. Sessions were
// assembled from 6 fragments instead of 83, silently, on stderr nobody reads.
//
// NOTHING CAUGHT IT. `just build`, `just lint`, `just test-arch` and the full
// acceptance suite were all green, because the corpus seeds its fixtures through
// the SAME accessor the fetch uses: publish to v1 and pull from v1 round-trips
// exactly as well as v2, so a round-trip test is structurally blind to the
// code's layout disagreeing with the layout real content is stored at. Only the
// literal-path unit pins reddened, and only for the prefix — never the leaf.
//
// So the invariant is enforced here instead: outside the file that builds the
// composed accessors, production code may not reach for the bare prefix at all.
// If you cannot get the prefix, you cannot hand-build a leaf.
//
// THE ALLOWLIST IS EMPTY ON PURPOSE. This gate was written after the last
// hand-built leaf was removed, so it grandfathers no debt — unlike the ratchets
// beside it, a violation here is always new. Adding an entry means writing down
// why a caller needs a prefix it cannot compose a path from.
//
// Detection is purely syntactic (go/ast, no go/types): any reference to the
// prefix accessors by NAME, whichever package qualifies them, so a caller that
// aliases the import is still caught.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bundlePrefixAccessors are the layout-qualified prefixes that must not be
// composed with a hand-built leaf.
var bundlePrefixAccessors = map[string]bool{
	"RepoItemPrefix":    true,
	"ContentItemPrefix": true,
}

// bundlePrefixAllowed are the module-relative files permitted to name them.
// Empty of exemptions by design — see the package doc.
var bundlePrefixAllowed = map[string]string{
	"internal/adapters/remote/repo_layout.go": "declares the accessors AND the composed " +
		"RepoItemPath/ContentItemPath that are the only sanctioned way to use them",
}

func TestArch_BundlePath_LeafIsNeverHandBuilt(t *testing.T) {
	for file, refs := range findBundlePrefixRefs(t) {
		if _, ok := bundlePrefixAllowed[file]; ok {
			continue
		}
		assert.Failf(t, "bundle prefix used outside its composing file",
			"%s references %s. A layout-qualified PREFIX cannot be joined to a "+
				"leaf by hand: the layout owns the leaf too (paths.BundleLayout.ItemFileName), "+
				"and deciding it separately is how publish and fetch came to disagree. "+
				"Use remote.RepoItemPath / remote.ContentItemPath, which compose both.",
			file, strings.Join(refs, ", "))
	}
}

// TestArch_BundlePath_AllowlistIsLive fails when an allowlisted file stops
// referencing the accessors, so the exemption cannot outlive its reason.
func TestArch_BundlePath_AllowlistIsLive(t *testing.T) {
	found := findBundlePrefixRefs(t)
	for file, why := range bundlePrefixAllowed {
		assert.Containsf(t, found, file,
			"%s is allowlisted (%q) but no longer references the prefix accessors — drop the entry",
			file, why)
	}
}

// findBundlePrefixRefs walks every non-test .go file under the module root and
// returns, per module-relative file, the prefix accessor names it mentions.
func findBundlePrefixRefs(t *testing.T) map[string][]string {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	hits := map[string][]string{}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", p, perr)
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)

		seen := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || !bundlePrefixAccessors[id.Name] || seen[id.Name] {
				return true
			}
			seen[id.Name] = true
			hits[rel] = append(hits[rel], id.Name)
			return true
		})
		return nil
	})
	require.NoError(t, err)
	return hits
}
