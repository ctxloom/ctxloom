package wire

import (
	"encoding/json"
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
// encoding/json falls back to the Go field name rather than failing.
//
// The two tests below are halves of one gate. taggedTypes is the reflection
// registry the parity assertions run over; TestEveryStructTypeIsRegistered
// walks the package source so a type added to the package but not to the
// registry fails instead of going unchecked.

// taggedTypes lists every struct type declared in this package. Keep it in sync
// with the package — TestEveryStructTypeIsRegistered enforces that.
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

func TestJSONYAMLTagParity(t *testing.T) {
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

// TestEveryStructTypeIsRegistered parses this package's own source and fails if
// it declares a struct type that taggedTypes does not cover. Without it the
// parity check above is only as complete as a hand-maintained list, and the
// next type added to the package would be born untested — which is the exact
// way the container types drifted from the leaf types in the first place.
func TestEveryStructTypeIsRegistered(t *testing.T) {
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

// TestLeafMarshalBytesArePinned pins the exact JSON a fully-populated leaf
// value produces. The leaf tags are load-bearing — internal/shared/agent's
// mcpfile writer and internal/claude's settings writer marshal MCPServer and
// Hook straight into backend settings files, so a changed leaf name silently
// changes a file some engine parses. Adding tags to the CONTAINER types must
// not move these bytes; this test is what says so.
func TestLeafMarshalBytesArePinned(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want string
	}{
		{
			name: "MCPServer",
			val: MCPServer{
				Command:      "npx",
				Args:         []string{"-y", "srv"},
				Env:          map[string]string{"K": "V"},
				Notes:        "n",
				Installation: "i",
				SCM:          "hash",
			},
			want: `{"command":"npx","args":["-y","srv"],"env":{"K":"V"},"notes":"n","installation":"i","_ctxloom":"hash"}`,
		},
		{
			name: "MCPServer_minimal",
			val:  MCPServer{Command: "cmd"},
			want: `{"command":"cmd"}`,
		},
		{
			name: "Hook",
			val: Hook{
				Matcher:         "Bash",
				Command:         "echo hi",
				Type:            "command",
				Prompt:          "p",
				Timeout:         30,
				Async:           true,
				SCM:             "hash",
				ContextHash:     "never-serialized",
				PreToolFallback: true,
			},
			want: `{"matcher":"Bash","command":"echo hi","type":"command","prompt":"p","timeout":30,"async":true,"_ctxloom":"hash","pre_tool_fallback":true}`,
		},
		{
			name: "Hook_minimal",
			val:  Hook{Command: "echo hi"},
			want: `{"command":"echo hi"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.val)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("leaf JSON changed\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// TestContainerMarshalUsesSnakeCase is the other side of the same coin: it
// pins what the containers now emit. Before the json tags were added these
// keys were the Go field names (Unified, PreTool, Servers,
// AutoRegisterCtxloom) sitting beside snake_case leaves in the same document.
func TestContainerMarshalUsesSnakeCase(t *testing.T) {
	hooks := HooksConfig{
		Unified: UnifiedHooks{
			PreTool:      []Hook{{Command: "a"}},
			PostTool:     []Hook{{Command: "b"}},
			SessionStart: []Hook{{Command: "c"}},
			SessionEnd:   []Hook{{Command: "d"}},
			PreShell:     []Hook{{Command: "e"}},
			PostFileEdit: []Hook{{Command: "f"}},
		},
		Plugins: map[string]BackendHooks{
			"claude-code": {"PreToolUse": []Hook{{Command: "g"}}},
		},
	}
	gotHooks, err := json.Marshal(hooks)
	if err != nil {
		t.Fatalf("json.Marshal(HooksConfig): %v", err)
	}
	wantHooks := `{"unified":{"pre_tool":[{"command":"a"}],"post_tool":[{"command":"b"}],"session_start":[{"command":"c"}],"session_end":[{"command":"d"}],"pre_shell":[{"command":"e"}],"post_file_edit":[{"command":"f"}]},"plugins":{"claude-code":{"PreToolUse":[{"command":"g"}]}}}`
	if string(gotHooks) != wantHooks {
		t.Errorf("HooksConfig JSON\n got: %s\nwant: %s", gotHooks, wantHooks)
	}

	auto := true
	mcp := MCPConfig{
		AutoRegisterCtxloom: &auto,
		Servers:             map[string]MCPServer{"s": {Command: "cmd"}},
		Plugins:             map[string]map[string]MCPServer{"claude-code": {"p": {Command: "pc"}}},
	}
	gotMCP, err := json.Marshal(mcp)
	if err != nil {
		t.Fatalf("json.Marshal(MCPConfig): %v", err)
	}
	wantMCP := `{"auto_register_ctxloom":true,"servers":{"s":{"command":"cmd"}},"plugins":{"claude-code":{"p":{"command":"pc"}}}}`
	if string(gotMCP) != wantMCP {
		t.Errorf("MCPConfig JSON\n got: %s\nwant: %s", gotMCP, wantMCP)
	}
}
