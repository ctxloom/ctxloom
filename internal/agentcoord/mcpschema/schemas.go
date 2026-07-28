package mcpschema

import (
	"embed"
	"encoding/json"
	"fmt"
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
func Tools() ([]ToolSpec, error) {
	loadOnce.Do(func() {
		entries, err := schemaFS.ReadDir("schemas")
		if err != nil {
			loadedErr = fmt.Errorf("mcpschema: embedded schemas: %w", err)
			return
		}
		for _, e := range entries {
			raw, err := schemaFS.ReadFile("schemas/" + e.Name())
			if err != nil {
				loadedErr = fmt.Errorf("mcpschema: read %s: %w", e.Name(), err)
				return
			}
			var spec ToolSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				loadedErr = fmt.Errorf("mcpschema: parse %s: %w", e.Name(), err)
				return
			}
			loaded = append(loaded, spec)
		}
		sort.Slice(loaded, func(i, j int) bool { return loaded[i].Name < loaded[j].Name })
	})
	return loaded, loadedErr
}

// ToolByName looks one generated tool up.
//
// test-only: no production caller (U026-F13) — kept rather than deleted
// because internal/cli/mcp_runner_test.go, a different package, calls it;
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
