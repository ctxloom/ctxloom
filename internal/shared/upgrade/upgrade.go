// Package upgrade is ctxloom's on-disk schema upgrade layer. ctxloom never
// silently rewrites a user/state file: instead it upgrades an older on-disk
// representation to the current one *in memory* on load, and an interactive
// caller may then prompt the user before persisting (see Pending).
//
// An Upgrader is one schema step; a Pipeline is an ordered, composable chain of
// them. Both config (internal/config) and the session index (internal/sessions)
// build a Pipeline from their own Upgraders and run it over the raw file bytes.
// The layer is YAML-document oriented — Pipeline.Run parses once and re-encodes
// once — and version-aware via the Version/SetVersion helpers, so an Upgrader
// can gate on (and bump) a top-level integer schema version.
package upgrade

import (
	"bytes"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Upgrader is one schema upgrade. Apply mutates the root mapping node of a
// parsed YAML document in place and reports whether it changed anything. An
// Upgrader MUST be idempotent: given a document already at (or past) its target
// form it must leave the node untouched and return false. That idempotence is
// what makes Upgraders composable — a Pipeline can run the whole chain over any
// document, new or legacy, and trust already-current docs to pass through.
type Upgrader interface {
	// Name identifies the upgrade in logs and the rewrite prompt.
	Name() string
	// Apply mutates root (a !!map node) toward the current schema, returning
	// whether it made any change.
	Apply(root *yaml.Node) (changed bool)
}

// Pipeline applies an ordered sequence of Upgraders front-to-back. It is itself
// an Upgrader, so pipelines compose and nest: a layer of upgrades is just
// another upgrade.
type Pipeline []Upgrader

// Name identifies the pipeline in logs.
func (p Pipeline) Name() string { return "upgrade pipeline" }

// Apply runs every stage in order against the same root node, so a stage that
// unlocks a later one composes naturally. Returns whether any stage changed the
// node.
func (p Pipeline) Apply(root *yaml.Node) (changed bool) {
	for _, u := range p {
		if u.Apply(root) {
			changed = true
		}
	}
	return changed
}

// Run is the byte driver: it parses data into a YAML document, applies the
// pipeline to the root mapping node, and re-encodes only if some stage changed
// it. When nothing changes — or the input is malformed or not a mapping — the
// original bytes are returned verbatim (no reserialization), leaving the normal
// parse path to surface any real error. applied lists the names of the stages
// that fired, in order, for the caller's rewrite prompt.
func (p Pipeline) Run(data []byte) (out []byte, applied []string) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return data, nil
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data, nil
	}
	root := doc.Content[0]

	for _, u := range p {
		if u.Apply(root) {
			applied = append(applied, u.Name())
		}
	}
	if len(applied) == 0 {
		return data, nil
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return data, nil
	}
	_ = enc.Close()
	return buf.Bytes(), applied
}

// Pending records that loading upgraded an older on-disk document to the
// current schema in memory. The upgraded bytes are NOT persisted automatically;
// an interactive caller may prompt the user and then write Data to Path. Nil
// means the file was already current.
type Pending struct {
	Path    string   // file the document came from
	Data    []byte   // upgraded bytes, ready to persist verbatim
	Applied []string // names of the upgrades that fired, for the prompt
}

// Version reads a top-level integer schema version from key on the root mapping
// node. A missing or non-integer value yields 0 — the implicit "pre-versioning"
// generation.
func Version(root *yaml.Node, key string) int {
	v := MapValue(root, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return 0
	}
	n, err := strconv.Atoi(v.Value)
	if err != nil {
		return 0
	}
	return n
}

// SetVersion stamps a top-level integer schema version under key on the root
// mapping node, replacing any existing value.
func SetVersion(root *yaml.Node, key string, v int) {
	node := ScalarNode(strconv.Itoa(v))
	node.Tag = "!!int"
	MapSet(root, key, node)
}

// MapValue returns the value node for key in a mapping node, or nil if absent.
func MapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// MapSet replaces key's value, or appends the key/value pair if absent.
func MapSet(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, ScalarNode(key), value)
}

// MapDelete removes key (and its value) from a mapping node.
func MapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// EnsureMap returns parent[key] as a mapping node, creating one if absent or of
// the wrong kind.
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
