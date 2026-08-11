//go:build arch

package wire

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This package is the cross-repo serialized vocabulary: the same value is read
// out of config.yaml (yaml tags) and written into backend settings files (json
// tags). A field that carries one tag and not the other, or carries both under
// different names, is one spelling in the file the user edits and a different
// spelling in the file the engine reads — and the drift is silent, because
// encoding/json falls back to the Go field name rather than failing. That is
// exactly how the container types (UnifiedHooks, HooksConfig, MCPConfig) drifted
// from the leaf types (Hook, MCPServer): the leaves picked up json tags when
// they needed backend-settings marshalling and the containers never did.
//
// BUILD-TAGGED `arch`, following this repo's discrete architectural-invariant
// gate idiom (build/gates.justfile's `test-arch`, ~155): named
// `TestArch_<Subject>_<Property>` so that recipe's `-run 'TestArch_'` selects
// it, and tagged `//go:build arch` so `test-default` never runs it — a red
// result here is unambiguously this class of defect and nothing else.
//
// The two tests below are halves of one gate. taggedTypes is the reflection
// registry the parity assertion runs over; TestArch_WireTagParity_AllTypesRegistered
// walks the package source so a type added to the package but not to the
// registry fails instead of going unchecked.

// taggedTypes lists every struct type declared in this package. Keep it in sync
// with the package — TestArch_WireTagParity_AllTypesRegistered enforces that.
var taggedTypes = []any{
	Hook{},
	UnifiedHooks{},
	HooksConfig{},
	MCPServer{},
	MCPConfig{},
}

// tagName returns the name a struct tag assigns to a field, and whether the tag
// is present at all. The name is the tag's first comma-separated element, so
// `json:"pre_tool,omitempty"` reports "pre_tool": options like omitempty are
// per-encoder emission policy, not identity, and parity is about identity.
func tagName(f reflect.StructField, key string) (string, bool) {
	raw, ok := f.Tag.Lookup(key)
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(raw, ",")
	return name, true
}

// TestArch_WireTagParity asserts every exported field of every struct type in
// this package carries BOTH a yaml tag and a json tag, under the SAME name.
// There is no exemption list: a field that seems to need one is a signal this
// package grew a field that is not, in fact, part of the shared wire
// vocabulary — that is an escalation, not a line to add here.
func TestArch_WireTagParity(t *testing.T) {
	for _, v := range taggedTypes {
		typ := reflect.TypeOf(v)
		t.Run(typ.Name(), func(t *testing.T) {
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				if !f.IsExported() {
					continue
				}
				jsonName, hasJSON := tagName(f, "json")
				yamlName, hasYAML := tagName(f, "yaml")
				switch {
				case !hasJSON && !hasYAML:
					t.Errorf("%s.%s: no json tag and no yaml tag; both are required so the field has one name on the wire", typ.Name(), f.Name)
				case !hasJSON:
					t.Errorf("%s.%s: yaml tag %q but no json tag; json.Marshal would emit the Go field name %q instead", typ.Name(), f.Name, yamlName, f.Name)
				case !hasYAML:
					t.Errorf("%s.%s: json tag %q but no yaml tag; yaml.Marshal would emit the lowercased Go field name instead", typ.Name(), f.Name, jsonName)
				case jsonName != yamlName:
					t.Errorf("%s.%s: json name %q != yaml name %q; the field must have ONE name across both encodings", typ.Name(), f.Name, jsonName, yamlName)
				}
			}
		})
	}
}

// TestArch_WireTagParity_AllTypesRegistered parses this package's own source and
// fails if it declares a struct type that taggedTypes does not cover. Without it
// the parity check above is only as complete as a hand-maintained list, and the
// next type added to the package would be born untested — which is the exact
// way the container types drifted from the leaf types in the first place.
func TestArch_WireTagParity_AllTypesRegistered(t *testing.T) {
	registered := make(map[string]bool, len(taggedTypes))
	for _, v := range taggedTypes {
		registered[reflect.TypeOf(v).Name()] = true
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	fset := token.NewFileSet()
	var declared []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if _, ok := ts.Type.(*ast.StructType); !ok {
				return true
			}
			declared = append(declared, ts.Name.Name)
			return true
		})
	}

	if len(declared) == 0 {
		t.Fatal("parsed no struct declarations from this package: the scan is vacuous and would pass no matter what")
	}
	sort.Strings(declared)

	for _, name := range declared {
		if !registered[name] {
			t.Errorf("struct type %s is declared in this package but missing from taggedTypes, so its tags are unchecked; add %s{} to taggedTypes", name, name)
		}
	}
	t.Logf("checked %d struct types: %s", len(declared), strings.Join(declared, ", "))
}
