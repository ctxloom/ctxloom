package docsgen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GenConfig renders the tracked JSON Schema at schemaPath into a Markdown
// reference page at <dir>/config.md. It walks the schema rather than reflecting
// p's own Go config struct directly: a hand-annotated schema's tags are
// yaml/mapstructure (not the json the reflectors want) and it carries untagged
// runtime fields, whereas the hand-annotated schema is the curated, user-facing
// surface. Output is deterministic (object keys sorted; anyOf/oneOf array order
// preserved).
//
// p supplies the product-specific conventions the intro prose names: its
// config-dir name (p.Bin-derived, e.g. ~/.ctxloom/config.yaml vs.
// ~/.taskloom/config.yaml) and its confload EnvPrefix (CTXLOOM_CONFIG_ vs.
// TASKLOOM_CONFIG_) — see overrideChainSection. Without this, a second
// product's generated page would describe ctxloom's own conventions verbatim,
// which is wrong for every product but ctxloom.
func GenConfig(p *Product, schemaPath, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read config schema: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse config schema: %w", err)
	}

	// json.Unmarshal accepts a bare `null` document into a map with
	// no error, leaving root == nil; every subsequent map read on a nil map
	// is legal Go, so nothing panics and nothing errors either. Combined with
	// a schema that parses but carries no "properties" and no "$defs" (an
	// empty object, or any other valid-but-content-free JSON), this used to
	// still write a config.md with a heading and zero fields, exit 0, and
	// print success -- indistinguishable from --config-schema pointing at
	// the wrong file entirely. Refuse before writing anything.
	props := objectMap(root["properties"])
	defs := objectMap(root["$defs"])
	if len(props) == 0 && len(defs) == 0 {
		return fmt.Errorf("docsgen: config schema %s has no properties or $defs -- nothing to document", schemaPath)
	}

	c := &configDoc{defs: defs}

	c.b.WriteString(configFrontmatter(schemaPath))
	if title, _ := root["title"].(string); title != "" {
		fmt.Fprintf(&c.b, "%s.\n\n", strings.TrimRight(title, "."))
	}
	dotDir := "." + p.Bin
	fmt.Fprintf(&c.b, "These fields map to `~/%s/config.yaml` (and the per-project `%s/config.yaml`). ",
		dotDir, dotDir)
	fmt.Fprintf(&c.b, "The canonical machine-readable form is the JSON Schema at `%s`.\n\n", schemaPath)

	c.b.WriteString(overrideChainSection(props, p.envPrefix()))

	// Top-level fields. Note the root schema's own "description" (if any) is
	// NOT suppressed here: renderNode renders whatever describe(root) returns
	// under this "Top-Level Fields" heading same as any other node, so it can
	// duplicate the intro prose above.
	c.renderNode(2, "Top-Level Fields", "", root)

	// Shared definitions ($defs), referenced by $ref from the fields above.
	if len(c.defs) > 0 {
		c.b.WriteString("## Definitions\n\n")
		c.b.WriteString("Reusable schema types referenced by the fields above.\n\n")
		names := make([]string, 0, len(c.defs))
		for name := range c.defs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			c.renderNode(3, name, name, objectMap(c.defs[name]))
		}
	}

	out := filepath.Join(dir, "config.md")
	return os.WriteFile(out, []byte(c.b.String()), 0o644)
}

type configDoc struct {
	b    strings.Builder
	defs map[string]any
}

// renderNode renders one object schema node: an optional description, then
// either its anyOf/oneOf branches (each a sub-node) or a Field|Type|Description
// table of its properties, recursing into every property that expands into a
// nested object of its own. path builds the dotted breadcrumb used to title
// child sections; heading is this node's own heading text.
func (c *configDoc) renderNode(level int, heading, path string, node map[string]any) {
	fmt.Fprintf(&c.b, "%s %s\n\n", strings.Repeat("#", clampLevel(level)), heading)

	if desc := describe(node); desc != "" {
		fmt.Fprintf(&c.b, "%s\n\n", desc)
	}

	// anyOf/oneOf: render each object branch (e.g. the per-backend shapes of
	// llmConfig) as its own sub-node. Scalar branches carry no fields to table.
	if branches := objectBranches(node); len(branches) > 0 {
		for i, br := range branches {
			bt := branchTitle(br, i)
			c.renderNode(level+1, bt, joinPath(path, bt), br)
		}
		return
	}

	props := objectMap(node["properties"])
	if len(props) == 0 {
		return
	}
	required := stringSet(node["required"])

	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	c.b.WriteString("| Field | Type | Description |\n")
	c.b.WriteString("|-------|------|-------------|\n")
	for _, name := range names {
		field := objectMap(props[name])
		desc := describe(field)
		if required[name] {
			desc = strings.TrimSpace("**Required.** " + desc)
		}
		fmt.Fprintf(&c.b, "| `%s` | %s | %s |\n", name, typeOf(field), mdCell(desc))
	}
	c.b.WriteString("\n")

	// Recurse into each property whose shape is an inline object/array-of-object
	// worth its own section. $ref'd fields are documented under Definitions.
	for _, name := range names {
		if child, suffix := expandableChild(objectMap(props[name])); child != nil {
			childPath := joinPath(path, name) + suffix
			c.renderNode(level+1, childPath, childPath, child)
		}
	}
}

// typeOf renders a schema node's type as a compact human string.
func typeOf(n map[string]any) string {
	if ref, ok := n["$ref"].(string); ok {
		return refName(ref)
	}
	if _, ok := n["const"]; ok {
		// A discriminator const (e.g. `type: {const: "claude-code"}`); the
		// exact value is surfaced in the description via describe.
		if t, ok := n["type"].(string); ok {
			return t
		}
		return "string"
	}
	if _, ok := n["anyOf"]; ok {
		return "one of several shapes"
	}
	if _, ok := n["oneOf"]; ok {
		return "one of several shapes"
	}
	switch t, _ := n["type"].(string); t {
	case "array":
		if items := objectMap(n["items"]); items != nil {
			if ref, ok := items["$ref"].(string); ok {
				return refName(ref) + "[]"
			}
			if _, ok := items["oneOf"]; ok {
				return "(mixed)[]"
			}
			if it, ok := items["type"].(string); ok {
				return it + "[]"
			}
		}
		return "array"
	case "object":
		if ap := objectMap(n["additionalProperties"]); ap != nil {
			if ref, ok := ap["$ref"].(string); ok {
				return "map → " + refName(ref)
			}
			if at, ok := ap["type"].(string); ok {
				if at == "object" {
					if ap2 := objectMap(ap["additionalProperties"]); ap2 != nil {
						if ref, ok := ap2["$ref"].(string); ok {
							return "map → map → " + refName(ref)
						}
					}
					return "map → object"
				}
				return "map → " + at
			}
			return "map"
		}
		return "object"
	case "":
		return "any"
	default:
		return t
	}
}

// describe assembles a table-cell description: the node's description plus any
// enum, examples, and default annotations.
func describe(n map[string]any) string {
	var parts []string
	if d, ok := n["description"].(string); ok && d != "" {
		parts = append(parts, d)
	}
	if cv, ok := n["const"]; ok {
		parts = append(parts, fmt.Sprintf("Must be `%s`.", scalarOrJSON(cv)))
	}
	if enum := toSlice(n["enum"]); len(enum) > 0 {
		parts = append(parts, "Allowed values: "+backticked(enum)+".")
	}
	if ex := toSlice(n["examples"]); len(ex) > 0 {
		parts = append(parts, "Examples: "+backticked(ex)+".")
	}
	if def, ok := n["default"]; ok {
		parts = append(parts, fmt.Sprintf("Default: `%s`.", scalarOrJSON(def)))
	}
	return strings.Join(parts, " ")
}

// scalarOrJSON renders a JSON-decoded value the way a reader would actually
// type it into config.yaml: a scalar via %v (string/bool/number, unquoted),
// but a non-scalar (a map or array default/const) as compact JSON rather
// than Go's %v syntax (map[a:1], [x y]), which nobody writes in a config
// file.
func scalarOrJSON(v any) string {
	switch v.(type) {
	case map[string]any, []any:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return fmt.Sprintf("%v", v)
}

// expandableChild decides whether a property warrants its own recursive section
// and returns the schema to render plus a heading suffix. $ref'd properties
// return nil (their type is documented once under Definitions).
func expandableChild(field map[string]any) (map[string]any, string) {
	if _, ok := field["$ref"]; ok {
		return nil, ""
	}
	switch t, _ := field["type"].(string); t {
	case "object":
		if objectMap(field["properties"]) != nil {
			return field, ""
		}
		if ap := objectMap(field["additionalProperties"]); ap != nil {
			if _, isRef := ap["$ref"]; isRef {
				return nil, ""
			}
			if objectMap(ap["properties"]) != nil {
				return ap, " (map values)"
			}
		}
	case "array":
		if items := objectMap(field["items"]); items != nil {
			if _, isRef := items["$ref"]; isRef {
				return nil, ""
			}
			if objectMap(items["properties"]) != nil {
				return items, " (items)"
			}
			if len(objectBranches(items)) > 0 {
				return items, " (items)"
			}
		}
	}
	return nil, ""
}

// objectBranches returns the anyOf/oneOf branches that carry a properties block
// (scalar branches are skipped — they have nothing to table).
func objectBranches(n map[string]any) []map[string]any {
	raw, ok := n["anyOf"]
	if !ok {
		raw, ok = n["oneOf"]
	}
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, item := range list {
		br := objectMap(item)
		if br != nil && objectMap(br["properties"]) != nil {
			out = append(out, br)
		}
	}
	return out
}

// branchTitle names an anyOf/oneOf branch: its `title`, else its `type`, else a
// positional fallback that keeps headings unique and deterministic.
func branchTitle(br map[string]any, i int) string {
	if t, ok := br["title"].(string); ok && t != "" {
		return t
	}
	if t, ok := br["type"].(string); ok && t != "" {
		return t
	}
	return fmt.Sprintf("option %d", i+1)
}

// overrideChainSection renders the cross-cutting "how to override any of
// this" explanation: the full precedence chain
// (internal/shared/confload implements it) and
// the envPrefix env-var / CLI-flag naming convention. This can't be
// attached to any single schema property — it applies to EVERY field alike —
// so it lives here as fixed prose rather than per-field description text,
// with one concrete example derived from the schema itself (the first
// scalar top-level property, alphabetically) so the convention reads as
// real, not hypothetical. envPrefix is the product's own confload EnvPrefix
// (e.g. "CTXLOOM_CONFIG_", "TASKLOOM_CONFIG_" — see Product.envPrefix), so
// this reads correctly for every product sharing this generator, not just
// ctxloom.
func overrideChainSection(topProps map[string]any, envPrefix string) string {
	var b strings.Builder
	b.WriteString("## Overriding From The Environment Or A CLI Flag\n\n")
	b.WriteString("Every field on this page can also be set without editing a config file, in ")
	b.WriteString("ascending precedence: the home config file, then the project config file, ")
	b.WriteString("then an environment variable, then a `--config-set` flag — the last one that sets a ")
	b.WriteString("given key wins.\n\n")
	fmt.Fprintf(&b, "An environment variable override starts with `%s`, followed by ", envPrefix)
	b.WriteString("the field's dotted path with each segment upper-cased and joined by `_` ")
	fmt.Fprintf(&b, "(e.g. `agents.mycoder.runtime` becomes `%sAGENTS_MYCODER_RUNTIME`). ", envPrefix)
	b.WriteString("A CLI override uses the repeatable `--config-set <dotted.path>=<value>` flag ")
	b.WriteString("(`--config-set agents.mycoder.runtime=container-rootless`) — never a per-field flag of its own, ")
	b.WriteString("since a command's OWN flags (`--format`, `--bundle`, ...) are not config overrides. ")
	b.WriteString("Both forms are matched case-insensitively against whatever your config file ")
	b.WriteString("already has, adopting its casing; unlike an environment variable's name, `--config-set`'s ")
	b.WriteString("path preserves whatever case you type, so it can also CREATE a new case-sensitive ")
	b.WriteString("key (e.g. `--config-set agents.MyCoder.runtime=container-rootless`), which an environment variable ")
	b.WriteString("cannot do.\n\n")

	if name, ok := pickScalarExampleField(topProps); ok {
		fmt.Fprintf(&b, "For example, `%s` can be set via `%s%s=<value>` or `--config-set %s=<value>`.\n\n",
			name, envPrefix, strings.ToUpper(name), name)
	}
	return b.String()
}

// pickScalarExampleField returns the alphabetically-first top-level property
// whose type is a plain scalar (string/boolean/integer/number) — an object
// or array-typed field makes a confusing example (its OWN nested fields are
// what actually gets overridden, not the field itself).
func pickScalarExampleField(props map[string]any) (string, bool) {
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch t, _ := objectMap(props[name])["type"].(string); t {
		case "string", "boolean", "integer", "number":
			return name, true
		}
	}
	return "", false
}

// --- small schema helpers ---

// configFrontmatter renders the page's Starlight frontmatter + generated-file
// banner. schemaPath is the exact path passed to GenConfig (and thus to `just
// gen-docs`'s --config-schema), quoted verbatim so a reader always sees the
// schema THIS page was actually generated from — ctxloom's own
// resources/schema/input/config-schema.json, or a second product's own path
// (e.g. taskloom's resources/schema/input/taskloom-config-schema.json).
func configFrontmatter(schemaPath string) string {
	return fmt.Sprintf(`---
title: "Configuration"
---
<!-- GENERATED by %[1]sjust gen-docs%[1]s from %[2]s.
     Do not edit; edit the JSON Schema instead. -->
:::note
This page is generated from the config JSON Schema (%[1]s%[2]s%[1]s).
:::

`, "`", schemaPath)
}

func refName(ref string) string {
	return ref[strings.LastIndex(ref, "/")+1:]
}

func clampLevel(level int) int {
	if level > 6 {
		return 6
	}
	return level
}

func joinPath(path, seg string) string {
	if path == "" {
		return seg
	}
	return path + "." + seg
}

// objectMap coerces an `any` into a JSON object map, or nil if it isn't one.
func objectMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func toSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// stringSet builds a lookup set from a JSON string array.
func stringSet(v any) map[string]bool {
	out := map[string]bool{}
	for _, item := range toSlice(v) {
		if s, ok := item.(string); ok {
			out[s] = true
		}
	}
	return out
}

// backticked renders a list of scalars as comma-separated inline-code spans.
func backticked(items []any) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = fmt.Sprintf("`%v`", it)
	}
	return strings.Join(parts, ", ")
}
