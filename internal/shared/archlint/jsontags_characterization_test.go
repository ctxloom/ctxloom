package archlint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectUntaggedFields_WalksEveryReachableStruct pins the walk the
// structured-output gate reports from: an exported field with no json tag
// is missing whether reached directly, through a pointer, slice, array or
// map, or through an embedded struct; a "-" tag, an unexported field and an
// embedded field's own name are not; a type that owns its encoding is not
// followed; and a cycle terminates.
func TestCollectUntaggedFields_WalksEveryReachableStruct(t *testing.T) {
	const src = `package p

type Outer struct {
	Tagged   int ` + "`json:\"tagged\"`" + `
	Untagged string
	skip     int
	Dash     int ` + "`json:\"-\"`" + `
	Ptr      *ViaPtr
	Slice    []ViaSlice ` + "`json:\"slice\"`" + `
	Map      map[string]ViaMap ` + "`json:\"map\"`" + `
	Arr      [2]ViaArr ` + "`json:\"arr\"`" + `
	Embedded
	Owner    Custom ` + "`json:\"owner\"`" + `
	Loop     *Rec ` + "`json:\"loop\"`" + `
}

type ViaPtr struct{ A int }
type ViaSlice struct{ B int }
type ViaMap struct{ C int }
type ViaArr struct{ D int }
type Embedded struct{ E int }

type Custom struct{ X int }

func (Custom) MarshalJSON() ([]byte, error) { return nil, nil }

type Rec struct {
	Next *Rec ` + "`json:\"next\"`" + `
	Q    int
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	require.NoError(t, err)
	pkg, err := (&types.Config{}).Check("p", fset, []*ast.File{f}, nil)
	require.NoError(t, err)

	missing := map[string]bool{}
	collectUntaggedFields(pkg.Scope().Lookup("Outer").Type(), map[types.Type]bool{}, missing)
	got := make([]string, 0, len(missing))
	for k := range missing {
		got = append(got, k)
	}
	sort.Strings(got)
	assert.Equal(t, []string{
		"p.Embedded.E",
		"p.Outer.Ptr",
		"p.Outer.Untagged",
		"p.Rec.Q",
		"p.ViaArr.D",
		"p.ViaMap.C",
		"p.ViaPtr.A",
		"p.ViaSlice.B",
	}, got)
}
