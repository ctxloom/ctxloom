package yamlx

import "gopkg.in/yaml.v3"

// MapValue returns the value node for key in a mapping node, or nil when the
// key is absent or m is nil.
func MapValue(m *yaml.Node, key string) *yaml.Node {
	panic("yamlx.MapValue: not implemented")
}

// MapSet replaces key's value IN PLACE when it exists — the key node, its
// position and its comments survive — and appends a string-keyed pair
// otherwise.
func MapSet(m *yaml.Node, key string, value *yaml.Node) {
	panic("yamlx.MapSet: not implemented")
}

// MapDelete removes key and its value from a mapping node; absent is a no-op.
func MapDelete(m *yaml.Node, key string) {
	panic("yamlx.MapDelete: not implemented")
}

// EnsureMap returns parent[key] as a mapping node, creating one (or replacing
// a value of another kind) when needed.
func EnsureMap(parent *yaml.Node, key string) *yaml.Node {
	panic("yamlx.EnsureMap: not implemented")
}

// ScalarNode builds a plain string scalar node.
func ScalarNode(val string) *yaml.Node {
	panic("yamlx.ScalarNode: not implemented")
}
