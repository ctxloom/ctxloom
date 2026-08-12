package mcpschema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"sync"
)

// The checked-in generated schemas (the goldens). Regenerate with
// `just gen-mcp-schemas`; CI diffs a fresh generation against these
// (gen-mcp-schemas-check).
//
//go:embed schemas/*.json
var schemaFS embed.FS

var (
	loadOnce  sync.Once
	loaded    []ToolSpec
	loadedErr error
)

// Tools returns the generated coordination tool surface, sorted by name.
// The embedded schemas are trusted build products: a parse failure is a
// build corruption, surfaced as an error for the caller's fail-loud gate.
//
// The result is a fresh copy on every call. The load is memoised, so handing
// back the memoised slice would let one caller's edit rewrite the tool surface
// every later caller sees — and one of those callers is the runner's
// registration loop, which is what withholds coordinator-only tools from a
// leaf.
func Tools() ([]ToolSpec, error) {
	loadOnce.Do(func() { loaded, loadedErr = loadTools(schemaFS) })
	if loadedErr != nil {
		return nil, loadedErr
	}
	return cloneSpecs(loaded), nil
}

// loadTools reads and parses every schema in fsys, sorted by tool name. A
// failure returns NO tools: a partially populated surface handed back
// alongside an error is a silently incomplete tool set for any caller that
// treats the error as advisory.
func loadTools(fsys fs.FS) ([]ToolSpec, error) {
	entries, err := fs.ReadDir(fsys, "schemas")
	if err != nil {
		return nil, fmt.Errorf("mcpschema: embedded schemas: %w", err)
	}
	var specs []ToolSpec
	for _, e := range entries {
		raw, err := fs.ReadFile(fsys, "schemas/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("mcpschema: read %s: %w", e.Name(), err)
		}
		var spec ToolSpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			return nil, fmt.Errorf("mcpschema: parse %s: %w", e.Name(), err)
		}
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

// cloneSpecs deep-copies a tool surface, schema bytes included — a shallow
// slice copy would still alias the json.RawMessage backing arrays.
func cloneSpecs(specs []ToolSpec) []ToolSpec {
	out := make([]ToolSpec, len(specs))
	for i, s := range specs {
		s.InputSchema = bytes.Clone(s.InputSchema)
		s.OutputSchema = bytes.Clone(s.OutputSchema)
		out[i] = s
	}
	return out
}

// ToolByName looks one generated tool up.
//
// test-only: no production caller — kept rather than deleted
// because internal/mcp/mcp_runner_test.go, a different package, calls it;
// Go test files cannot be imported cross-package, so it cannot be moved into
// a _test.go file the way a same-package-only test helper could be. The
// goldens are a build product: a Tools() error here means the embedded
// schema set is corrupt, which is not a recoverable "no such tool" — panic
// rather than swallow the error into a false "not found" the caller cannot
// tell apart from a legitimate miss.
func ToolByName(name string) (ToolSpec, bool) {
	tools, err := Tools()
	if err != nil {
		panic(fmt.Errorf("mcpschema: ToolByName(%q): %w", name, err))
	}
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return ToolSpec{}, false
}
