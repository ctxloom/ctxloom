//go:build arch

package coord

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// referencingFiles returns the files in dir that MENTION sym somewhere other
// than in sym's own declaration. Comments are invisible to it: the files are
// parsed without them, so prose naming a symbol never counts as a use.
func referencingFiles(t *testing.T, dir, sym string, includeTests bool) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err, "parsing %s", name)

		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			if found {
				return false
			}
			switch v := n.(type) {
			case *ast.FuncDecl:
				// The declaration of sym is not a reference to it. Its BODY
				// still is — a method that calls itself is a real call site.
				if v.Name != nil && v.Name.Name == sym {
					if v.Body != nil {
						ast.Inspect(v.Body, func(b ast.Node) bool {
							if id, ok := b.(*ast.Ident); ok && id.Name == sym {
								found = true
								return false
							}
							return true
						})
					}
					return false
				}
			case *ast.SelectorExpr:
				if v.Sel != nil && v.Sel.Name == sym {
					found = true
					return false
				}
			case *ast.Ident:
				if v.Name == sym {
					found = true
					return false
				}
			}
			return true
		})

		if found {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TestArch_SpoolWrite_HappensOnlyInTheCourier pins the invariant that gives the
// spool its one-write-one-ring guarantee: a message file appears on disk in
// exactly one place, and that place also rings the doorbell. If a second caller
// could reach the writer cache, it could create a spool file that nobody is
// ever told about — the file would sit there until the next 30s sweep, or
// forever if the recipient never sweeps.
func TestArch_SpoolWrite_HappensOnlyInTheCourier(t *testing.T) {
	got := referencingFiles(t, ".", "writerFor", false)
	assert.Equal(t, []string{"spoolcourier.go"}, got,
		"only the courier may reach a spool writer: it is what pairs the write with the ring. "+
			"If you added a caller, route it through spoolCourier.SendProjected instead; "+
			"if you MOVED the courier, update this test to name its new file.")
}

// TestArch_RingSpool_ReachedOnlyThroughTheCourier is the other half. ringSpool
// is fire-and-forget on both peers, so a ring raised beside a write (rather
// than by the courier that just wrote) is indistinguishable from a correct one
// until a message goes missing. The two files below do not CALL it: each
// installs it as a courier's ring field, which is the whole point.
func TestArch_RingSpool_ReachedOnlyThroughTheCourier(t *testing.T) {
	got := referencingFiles(t, ".", "ringSpool", false)
	assert.Equal(t, []string{"spooldelivery.go", "spoolturnresult.go"}, got,
		"ringSpool belongs to the courier: these two files may only hand it to one as its ring. "+
			"A new file here means someone rings without writing through the courier — "+
			"make it go through spoolCourier.SendProjected (or Announce for a ring with no write).")
}
