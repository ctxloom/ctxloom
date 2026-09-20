package yamlx

import "gopkg.in/yaml.v3"

// MapValue returns the value node for key in a mapping node, or nil when the
// key is absent or m is nil.
func MapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// MapSet replaces key's value IN PLACE when it exists — the key node, its
// position and its comments survive — and appends a string-keyed pair
// otherwise.
func MapSet(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, ScalarNode(key), value)
}

// MapDelete removes key and its value from a mapping node; absent is a no-op.
func MapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// EnsureMap returns parent[key] as a mapping node, creating one (or replacing
// a value of another kind) when needed.
func EnsureMap(parent *yaml.Node, key string) *yaml.Node {
	if v := MapValue(parent, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	MapSet(parent, key, m)
	return m
}

// ScalarNode builds a plain string scalar node.
func ScalarNode(val string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val}
}
