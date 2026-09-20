package bundles

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"gopkg.in/yaml.v3"
)

// EngineBlocks are an item's per-engine exports: one OPAQUE block per engine
// name. This package carries a block as bytes and never reads inside one —
// the engine the key names decodes its own block against its ExportSchema,
// so a new engine needs no change here and a block for an engine this binary
// does not know is carried, not dropped.
//
// A block is canonical JSON (keys sorted), whatever YAML spelled it, so two
// authorings of the same block are the same bytes.
type EngineBlocks map[string]json.RawMessage

// UnmarshalYAML reads `<engine>: <mapping>` pairs. A value that is not a
// mapping is refused naming the engine: a scalar is an authoring error, not
// a block an engine could decode.
func (e *EngineBlocks) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("exports: expected a mapping of engine name to block, got %s", yamlKindName(node.Kind))
	}
	out := make(EngineBlocks, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		engine := node.Content[i].Value
		value := node.Content[i+1]
		if value.Kind != yaml.MappingNode {
			return fmt.Errorf("exports: engine %q: expected a block (mapping), got %s", engine, yamlKindName(value.Kind))
		}
		var generic map[string]any
		if err := value.Decode(&generic); err != nil {
			return fmt.Errorf("exports: engine %q: %w", engine, err)
		}
		block, err := json.Marshal(generic)
		if err != nil {
			return fmt.Errorf("exports: engine %q: %w", engine, err)
		}
		out[engine] = block
	}
	*e = out
	return nil
}

// MarshalYAML writes the blocks back as YAML mappings, engines by name.
func (e EngineBlocks) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, engine := range slices.Sorted(maps.Keys(e)) {
		var generic map[string]any
		if err := json.Unmarshal(e[engine], &generic); err != nil {
			return nil, fmt.Errorf("exports: engine %q: %w", engine, err)
		}
		var value yaml.Node
		if err := value.Encode(generic); err != nil {
			return nil, fmt.Errorf("exports: engine %q: %w", engine, err)
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: engine}, &value)
	}
	return node, nil
}

// IsZero makes `omitempty` drop an item with no blocks.
func (e EngineBlocks) IsZero() bool { return len(e) == 0 }

// Clone returns an independent copy: the blocks are immutable bytes, so
// sharing them is fine; the map is not.
func (e EngineBlocks) Clone() EngineBlocks {
	if e == nil {
		return nil
	}
	return maps.Clone(e)
}

func yamlKindName(k yaml.Kind) string {
	switch k {
	case yaml.ScalarNode:
		return "a scalar"
	case yaml.SequenceNode:
		return "a sequence"
	case yaml.MappingNode:
		return "a mapping"
	case yaml.AliasNode:
		return "an alias"
	default:
		return "a document"
	}
}
