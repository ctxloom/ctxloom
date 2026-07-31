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
// (resources/schema/input/{config,fragment,taskloom-config}-schema.json) under
// the same host.
const idBase = "https://ctxloom.dev/schemas/"

// draft is the JSON Schema dialect every generated schema declares.
const draft = "https://json-schema.org/draft/2020-12/schema"

// Target names one JSON output struct to publish a schema for. Name is the file
// base (without the "-schema.json" suffix) and the $id stem; if empty it is
// derived from the Go type name, which must therefore be non-empty — Generate
// refuses a target whose name cannot be derived. Nested types reused inside a
// Target are inlined by the reflector — only top-level output shapes need their
// own Target.
//
// An omitted Name binds the published $id to the Go identifier: renaming the
// type renames the schema's URL. Set Name explicitly on any target whose $id is
// meant to outlive Go-side refactoring.
type Target struct {
	Type reflect.Type
	Name string
}

// Generate writes one <name>-schema.json per target into dir, reflecting each
// struct via google/jsonschema-go and stamping the $schema, $id, and title so
// the files are stable, self-describing contracts. Output is deterministic
// (indented JSON, map-keyed properties) so re-running Generate over the same
// targets is a byte-identical no-op diff — useful for reproducibility, but
// NOT checked by CI: schemaDir is gitignored (see cmd/gen-schemas/main.go's
// doc), so there is nothing checked-in to regenerate-then-git-diff against.
// U097-F01: this comment used to claim a "regenerate-then-git-diff CI check"
// that has never existed (`rg -n gen-schemas-check .` finds no recipe
// anywhere) — determinism here is worth having for its own sake, not because
// of a gate that isn't real. An unrepresentable type is a hard error — a
// silently dropped field would be a lying contract.
//
// It returns the number of files it wrote, so a caller reports what was
// generated rather than what it asked for; the two can only differ when
// something went wrong, and the caller's own count could never disclose that.
func Generate(dir string, targets []Target) (int, error) {
	// U097-F02: zero targets used to succeed silently — MkdirAll, a no-op sort,
	// a loop over nothing, return nil — and the caller printed "wrote 0
	// schemas" and exited 0. Both target providers sit behind `//go:build
	// schemagen`, so a tag typo or a file move empties the list rather than
	// breaking the build, and nothing downstream notices: the output directory
	// is gitignored and `//go:embed all:schema` still matches on the input
	// schemas. A generator with nothing to generate is a broken build, not a
	// finished one.
	if len(targets) == 0 {
		return 0, errors.New("schemagen: no targets — refusing to report success having generated nothing")
	}
	if err := rejectUnderivableNames(targets); err != nil {
		return 0, err
	}
	// Two targets resolving to the same file base would write one $id with two
	// different shapes, last writer wins — a lying contract, and one no count of
	// TARGETS can ever disclose. Refuse before anything reaches disk.
	if err := rejectNameCollisions(targets); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// Ordered on a COPY. The order does not affect any file's bytes — each
	// target writes its own independent file — but it does fix which files
	// exist after a mid-run failure, so a failing run leaves the same partial
	// state every time. Sorting the argument itself would export that ordering
	// as a silent side effect on a slice the caller still owns.
	ordered := make([]Target, len(targets))
	copy(ordered, targets)
	sort.Slice(ordered, func(i, j int) bool { return name(ordered[i]) < name(ordered[j]) })

	written := 0
	for _, t := range ordered {
		n := name(t)
		schema, err := jsonschema.ForType(t.Type, nil)
		if err != nil {
			return written, fmt.Errorf("reflect %s: %w", t.Type, err)
		}
		schema.Schema = draft
		schema.ID = idBase + n + ".json"
		if schema.Title == "" {
			schema.Title = t.Type.Name()
		}

		data, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			return written, fmt.Errorf("marshal %s: %w", n, err)
		}
		data = append(data, '\n')
		path := filepath.Join(dir, n+"-schema.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", path, err)
		}
		written++
	}
	return written, nil
}

// rejectUnderivableNames reports an error for any target whose schema name
// resolves to the empty string. reflect.Type.Name() is "" for every unnamed
// type — an anonymous struct, a pointer, a slice, a map — so a target that
// relies on the derived name for one of those would otherwise be published as
// the file "-schema.json" under the $id "<idBase>.json": a contract whose URL
// identifies nothing. The name is the schema's identity; it cannot be blank.
func rejectUnderivableNames(targets []Target) error {
	for _, t := range targets {
		if name(t) != "" {
			continue
		}
		return fmt.Errorf(
			"schemagen: cannot derive a schema name for %s (unnamed types have no reflect name) — give the target an explicit Name", t.Type)
	}
	return nil
}

// rejectNameCollisions reports an error when two targets resolve to the same
// schema name, naming both Go types so the clash is fixable without a bisect.
func rejectNameCollisions(targets []Target) error {
	seen := make(map[string]reflect.Type, len(targets))
	for _, t := range targets {
		n := name(t)
		if prev, dup := seen[n]; dup {
			return fmt.Errorf("schemagen: %s and %s both resolve to schema %q — one would silently overwrite the other on disk", prev, t.Type, n)
		}
		seen[n] = t.Type
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
