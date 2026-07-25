//go:build schemagen

package schemagen

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"
)

// idBase is the published $id prefix; matches the hand-maintained input schemas
// (schema/input/{config,fragment}-schema.json) under the same host.
const idBase = "https://ctxloom.dev/schemas/"

// draft is the JSON Schema dialect every generated schema declares.
const draft = "https://json-schema.org/draft/2020-12/schema"

// Target names one JSON output struct to publish a schema for. Name is the file
// base (without the "-schema.json" suffix) and the $id stem; if empty it is
// derived from the Go type name. Nested types reused inside a Target are inlined
// by the reflector — only top-level output shapes need their own Target.
type Target struct {
	Type reflect.Type
	Name string
}

// Generate writes one <name>-schema.json per target into dir, reflecting each
// struct via google/jsonschema-go and stamping the $schema, $id, and title so
// the files are stable, self-describing contracts. Output is deterministic
// (sorted targets, indented JSON, map-keyed properties) so the regenerate-then-
// git-diff CI check is meaningful. An unrepresentable type is a hard error — a
// silently dropped field would be a lying contract.
func Generate(dir string, targets []Target) error {
	// U097-F02: zero targets used to succeed silently — MkdirAll, a no-op sort,
	// a loop over nothing, return nil — and the caller printed "wrote 0
	// schemas" and exited 0. Both target providers sit behind `//go:build
	// schemagen`, so a tag typo or a file move empties the list rather than
	// breaking the build, and nothing downstream notices: the output directory
	// is gitignored and `//go:embed all:schema` still matches on the input
	// schemas. A generator with nothing to generate is a broken build, not a
	// finished one.
	if len(targets) == 0 {
		return errors.New("schemagen: no targets — refusing to report success having generated nothing")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	sort.Slice(targets, func(i, j int) bool { return name(targets[i]) < name(targets[j]) })

	for _, t := range targets {
		n := name(t)
		schema, err := jsonschema.ForType(t.Type, nil)
		if err != nil {
			return fmt.Errorf("reflect %s: %w", t.Type, err)
		}
		schema.Schema = draft
		schema.ID = idBase + n + ".json"
		if schema.Title == "" {
			schema.Title = t.Type.Name()
		}

		data, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", n, err)
		}
		data = append(data, '\n')
		path := filepath.Join(dir, n+"-schema.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// name resolves a target's file/$id base: its explicit Name, else the kebab-case
// of its Go type name (RunOneshotResult -> run-oneshot-result).
func name(t Target) string {
	if t.Name != "" {
		return t.Name
	}
	return kebab(t.Type.Name())
}

// kebab lower-cases a CamelCase identifier with hyphens at case boundaries,
// keeping runs of capitals together (MCPServer -> mcp-server).
func kebab(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && unicode.IsLower(runes[i-1])
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if i > 0 && (prevLower || nextLower) {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
