//go:build arch

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// clifmt reads its display hints from ONE namespaced struct tag,
// `clifmt:"label=…,col=…,role=…"`. The bare `label:"…"` / `col:"…"` keys it
// once read are retired, and a leftover one is SILENTLY IGNORED: the field
// renders under its humanized json name and nothing fails. This gate turns
// that silence into a red build.
func TestArch_NoBareClifmtHintTags(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	scanned := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isNonTestGoFile(d.Name()) {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned++
		rel, _ := filepath.Rel(root, p)
		for _, hit := range bareHintTags(fset, f) {
			t.Errorf("%s:%s carries a bare struct tag the clifmt renderer no longer reads; write it as clifmt:\"label=…,col=…\"", rel, hit)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("scanned no Go files — the gate checked nothing")
	}
}

// The gate's detector, fed a planted source: it must flag each bare key and
// leave the namespaced form, and unrelated keys, alone.
func TestArch_NoBareClifmtHintTags_CatchesAPlantedTag(t *testing.T) {
	const src = `package p
type T struct {
	A string ` + "`json:\"a\" label:\"Alpha\"`" + `
	B string ` + "`json:\"b\" col:\"BETA\"`" + `
	C string ` + "`json:\"c\" clifmt:\"label=Gamma,col=GAMMA\"`" + `
	D string ` + "`json:\"d\" yaml:\"label\"`" + `
}
func f() { _ = struct{ E int ` + "`label:\"e\"`" + ` }{} }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "planted.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := bareHintTags(fset, f)
	want := []string{"3: A (label)", "4: B (col)", "8: E (label)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bareHintTags = %q, want %q", got, want)
	}
}

// bareHintTags returns "line: Field (key)" for every struct field in f whose
// tag carries a bare label or col key.
func bareHintTags(fset *token.FileSet, f *ast.File) []string {
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, field := range st.Fields.List {
			if field.Tag == nil {
				continue
			}
			raw, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				continue
			}
			tag := reflect.StructTag(raw)
			for _, key := range []string{"label", "col"} {
				if _, ok := tag.Lookup(key); !ok {
					continue
				}
				name := "_"
				if len(field.Names) > 0 {
					name = field.Names[0].Name
				}
				hits = append(hits, strconv.Itoa(fset.Position(field.Pos()).Line)+": "+name+" ("+key+")")
			}
		}
		return true
	})
	return hits
}
