//go:build arch

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestArch_DeclSites_FindsEveryDeclarationShape pins declSites against a
// planted file, because the retired-symbol test only ever sees it answer
// "nowhere": every shape a retired entry can name — a function, a method
// on a value or pointer receiver, a type, a struct field, a var or const —
// is found where declared (once per declaration), and a lookalike is not.
func TestArch_DeclSites_FindsEveryDeclarationShape(t *testing.T) {
	const src = `package plant

type Holder struct {
	Kept    int
	Retired string
	A, B    bool
}

type Retired struct{}

func Retired2() {}

func (Holder) Method()   {}
func (*Holder) PtrMethod() {}
func (Other) Method()    {}

var RetiredVar, Kept = 1, 2

const RetiredConst = 3
`
	f, err := parser.ParseFile(token.NewFileSet(), "plant.go", src, 0)
	require.NoError(t, err)
	files := map[string]*ast.File{"plant.go": f}

	cases := []struct {
		d    retiredDecl
		want int
	}{
		{retiredDecl{name: "Retired2"}, 1},
		{retiredDecl{name: "Retired"}, 1},
		{retiredDecl{recv: "Holder", name: "Method"}, 1},
		{retiredDecl{recv: "Holder", name: "PtrMethod"}, 1},
		{retiredDecl{recv: "Other", name: "Method"}, 1},
		{retiredDecl{recv: "Holder", name: "Retired", field: true}, 1},
		{retiredDecl{recv: "Holder", name: "B", field: true}, 1},
		{retiredDecl{name: "RetiredVar"}, 1},
		{retiredDecl{name: "RetiredConst"}, 1},
		{retiredDecl{name: "Method"}, 0},
		{retiredDecl{recv: "Holder", name: "Missing", field: true}, 0},
		{retiredDecl{recv: "Retired", name: "Kept", field: true}, 0},
		{retiredDecl{recv: "Holder", name: "RetiredVar"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.d.recv+"."+tc.d.name, func(t *testing.T) {
			assert.Len(t, declSites(files, tc.d), tc.want)
		})
	}
}
