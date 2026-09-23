package layerscope

import (
	"encoding/json"
	"strings"
)

// decodeJSONObject parses raw JSON schema bytes into a generic tree —
// deliberately encoding/json, not the compiled jsonschema.Schema this
// package's production code never touches: this is a TEST-ONLY walk of the
// schema DOCUMENT itself (see walkSchemaLeaves' doc for why the compiled
// form can't answer "is this a dynamic map" from the outside).
func decodeJSONObject(data []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// maxSchemaWalkDepth bounds the recursion defensively — there is no
// self-referential $ref in this schema today, but a test walker should fail
// loud on a runaway recursion rather than hang, matching
// schema.collectKnownKeys' own depth guard.
const maxSchemaWalkDepth = 64

// resolveSchemaRef follows a "#/$defs/X" pointer against root's own $defs,
// returning node unchanged if it carries no $ref or the pointer doesn't
// resolve.
func resolveSchemaRef(root, node map[string]any) map[string]any {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref, prefix) {
		return node
	}
	defs, _ := root["$defs"].(map[string]any)
	if defs == nil {
		return node
	}
	target, _ := defs[strings.TrimPrefix(ref, prefix)].(map[string]any)
	if target == nil {
		return node
	}
	return target
}

// walkSchemaLeaves walks node (ref-resolved against root at every step) and
// calls visit once for every LEAF path reachable from it — matching
// koanf/maps.Flatten's own granularity exactly, since that is what
// Policy.Check actually walks in production: a declared `properties` object
// is recursed into per named key; a dynamic map (`additionalProperties` as a
// schema, not a bool) is recursed into using ONE synthetic "*" segment,
// exactly like Rule.Path's own wildcard convention; `anyOf`/`oneOf` branches
// (e.g. one LLM backend's shape among several) are unioned at the SAME path,
// so every branch's fields are visited without adding a spurious segment.
// Anything else — an array, a scalar, or an object with none of the above —
// is a leaf and stops the walk.
func walkSchemaLeaves(root, node map[string]any, path []string, visit func(path []string)) {
	walkSchemaLeavesDepth(root, node, path, visit, 0)
}

func walkSchemaLeavesDepth(root, node map[string]any, path []string, visit func(path []string), depth int) {
	if depth > maxSchemaWalkDepth {
		return
	}
	node = resolveSchemaRef(root, node)

	props, hasProps := node["properties"].(map[string]any)
	additional, hasAdditional := node["additionalProperties"].(map[string]any)
	anyOf, _ := node["anyOf"].([]any)
	oneOf, _ := node["oneOf"].([]any)

	if !hasProps && !hasAdditional && len(anyOf) == 0 && len(oneOf) == 0 {
		if len(path) > 0 {
			visit(path)
		}
		return
	}

	for key, childRaw := range props {
		child, _ := childRaw.(map[string]any)
		if child == nil {
			continue
		}
		walkSchemaLeavesDepth(root, child, append(append([]string{}, path...), key), visit, depth+1)
	}
	if hasAdditional {
		walkSchemaLeavesDepth(root, additional, append(append([]string{}, path...), wildcard), visit, depth+1)
	}
	for _, branchRaw := range anyOf {
		branch, _ := branchRaw.(map[string]any)
		if branch == nil {
			continue
		}
		walkSchemaLeavesDepth(root, branch, path, visit, depth+1)
	}
	for _, branchRaw := range oneOf {
		branch, _ := branchRaw.(map[string]any)
		if branch == nil {
			continue
		}
		walkSchemaLeavesDepth(root, branch, path, visit, depth+1)
	}
}
