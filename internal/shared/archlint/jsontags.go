package archlint

import (
	"go/ast"
	"go/types"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// jsonOutputSinks are the functions whose second argument is rendered as the
// CLI's structured (--format json/yaml/toml) output. Keyed by
// "<import path>.<func name>".
var jsonOutputSinks = map[string]bool{
	"github.com/ctxloom/ctxloom/internal/adapters/cli.emit":   true,
	"github.com/ctxloom/ctxloom/internal/shared/cliemit.Emit": true,
	"github.com/ctxloom/ctxloom/pkg/clifmt.Render":            true,
}

// JSONTagsAnalyzer enforces that every struct reachable from a value handed to
// a structured-output sink names each exported field with a json tag.
//
// An untagged field renders under its Go name ("HarpID", "Kinds") beside
// snake_case siblings, and `jq '.[].harp_id'` then returns null per item: a
// wrong answer rather than an error, so nothing downstream notices. The key is
// a public contract, so it has to be chosen, not defaulted.
//
// Blind spots: a payload whose STATIC type at the sink is an interface (any)
// is not followed, and neither is anything below a type that implements
// json.Marshaler or encoding.TextMarshaler, since that type owns its encoding.
var JSONTagsAnalyzer = &analysis.Analyzer{
	Name: "archjsontags",
	Doc:  "structs rendered as CLI structured output must json-tag every exported field",
	Run:  runJSONTags,
}

func runJSONTags(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	for _, f := range ProdFiles(pass) {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 || !isJSONOutputSink(pass, call) {
				return true
			}
			payload := pass.TypesInfo.TypeOf(call.Args[1])
			if payload == nil {
				return true
			}
			missing := map[string]bool{}
			collectUntaggedFields(payload, map[types.Type]bool{}, missing)
			if len(missing) == 0 {
				return true
			}
			fields := make([]string, 0, len(missing))
			for k := range missing {
				fields = append(fields, k)
			}
			sort.Strings(fields)
			pass.Reportf(call.Args[1].Pos(),
				"%s is rendered as structured CLI output but these exported fields have no json tag: %s — "+
					"they would render under their Go names. Add a snake_case json tag to each (or json:\"-\" "+
					"to keep one out of the output).",
				types.TypeString(payload, nil), strings.Join(fields, ", "))
			return true
		})
	}
	return nil, nil
}

func isJSONOutputSink(pass *analysis.Pass, call *ast.CallExpr) bool {
	var id *ast.Ident
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		id = fn
	case *ast.SelectorExpr:
		id = fn.Sel
	default:
		return false
	}
	obj, ok := pass.TypesInfo.Uses[id].(*types.Func)
	return ok && obj.Pkg() != nil && jsonOutputSinks[obj.Pkg().Path()+"."+obj.Name()]
}

func ownsJSONEncoding(t types.Type) bool {
	for _, tt := range []types.Type{t, types.NewPointer(t)} {
		ms := types.NewMethodSet(tt)
		if ms.Lookup(nil, "MarshalJSON") != nil || ms.Lookup(nil, "MarshalText") != nil {
			return true
		}
	}
	return false
}

func collectUntaggedFields(t types.Type, seen map[types.Type]bool, missing map[string]bool) {
	if seen[t] {
		return
	}
	seen[t] = true
	if _, named := t.(*types.Named); named && ownsJSONEncoding(t) {
		return
	}
	if elem, ok := containerElem(t); ok {
		collectUntaggedFields(elem, seen, missing)
		return
	}
	if st, ok := t.Underlying().(*types.Struct); ok {
		collectStructFields(t, st, seen, missing)
	}
}

// containerElem is the element type of a pointer, slice, array or map.
func containerElem(t types.Type) (types.Type, bool) {
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return u.Elem(), true
	case *types.Slice:
		return u.Elem(), true
	case *types.Array:
		return u.Elem(), true
	case *types.Map:
		return u.Elem(), true
	}
	return nil, false
}

// collectStructFields records st's exported, untagged, non-embedded fields
// as missing (named by t) and walks every field it does not skip: a "-" tag,
// or an unexported non-embedded field.
func collectStructFields(t types.Type, st *types.Struct, seen map[types.Type]bool, missing map[string]bool) {
	for i := range st.NumFields() {
		f := st.Field(i)
		tag := reflect.StructTag(st.Tag(i)).Get("json")
		if tag == "-" || (!f.Exported() && !f.Embedded()) {
			continue
		}
		if tag == "" && !f.Embedded() {
			missing[types.TypeString(t, nil)+"."+f.Name()] = true
		}
		collectUntaggedFields(f.Type(), seen, missing)
	}
}
