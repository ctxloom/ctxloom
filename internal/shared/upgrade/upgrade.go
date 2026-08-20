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
	"errors"
	"io"
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

// Pipeline applies an ordered sequence of Upgraders front-to-back.
//
// Pipeline used to also implement Upgrader itself (Name/Apply) so
// pipelines could compose and nest. No production Pipeline literal ever
// nested another Pipeline (config, sessions, bundles, and profiles each build
// one flat list of concrete Upgraders — config.go even explicitly flattens
// rather than nesting), and the capability was reachable only via a test that
// existed solely to exercise it. Deleted both methods; if a real nesting need
// shows up later, reintroducing them is one small commit.
type Pipeline []Upgrader

// Run is the byte driver: it parses data into a YAML document, applies the
// pipeline to the root mapping node, and re-encodes only if some stage changed
// it. When nothing changes — or the input is malformed, not a mapping, or
// carries more than one document — the original bytes are returned verbatim (no
// reserialization), leaving the normal parse path to surface any real error.
// applied lists the names of the stages that fired, in order, for the caller's
// rewrite prompt.
func (p Pipeline) Run(data []byte) (out []byte, applied []string) {
	doc, ok := singleDocument(data)
	if !ok {
		return data, nil
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data, nil
	}
	root := doc.Content[0]
	if hasDuplicateKey(root) {
		return data, nil
	}

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
	if err := enc.Close(); err != nil {
		return data, nil
	}
	return buf.Bytes(), applied
}

// singleDocument parses data as a YAML stream carrying EXACTLY ONE document and
// returns that document's node. A stream carrying a second document is refused
// (ok == false), because Run re-encodes the node it parsed: a decode that keeps
// only the first document would emit a single-document file, silently deleting
// every later one from bytes the caller then persists verbatim. The pipeline
// upgrades a document in place or does nothing at all — it never narrows a
// stream.
func singleDocument(data []byte) (doc yaml.Node, ok bool) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		return doc, false
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return doc, false
	}
	return doc, true
}

// hasDuplicateKey reports whether any mapping in the subtree rooted at n names
// the same key twice. Such a document is malformed — every struct/map decode in
// the codebase refuses it — but a yaml.Node decode accepts it, and MapValue,
// MapSet and MapDelete all act on the FIRST match. An upgrade run over it
// therefore rewrites one of the two entries and leaves the other under the
// legacy key, producing a document that no longer has a duplicate and so parses
// cleanly, carrying whichever value the helpers happened to reach. Refusing to
// upgrade it keeps the loud parse error the caller would otherwise have got.
func hasDuplicateKey(n *yaml.Node) bool {
	if n.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			if _, dup := seen[n.Content[i].Value]; dup {
				return true
			}
			seen[n.Content[i].Value] = struct{}{}
		}
	}
	for _, child := range n.Content {
		if hasDuplicateKey(child) {
			return true
		}
	}
	return false
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
// node.
//
// A MISSING key is the implicit "pre-versioning" generation, reported as
// (0, true): such a document is genuinely at generation 0 and every migration
// should run over it.
//
// A key that is PRESENT but not an integer — `version: banana`, `version: 6.5`,
// a nested mapping — is reported as (0, false). It is not a pre-versioning
// document; it is a document whose version cannot be read, and the two are
// different facts. Collapsing them re-runs every migration from generation 0
// over a file that is probably corrupt and stamps the current version on the
// way out, which replaces the parse error the caller would have surfaced with a
// clean load of rewritten bytes. Callers gate on ok and decline.
func Version(root *yaml.Node, key string) (version int, ok bool) {
	v := MapValue(root, key)
	if v == nil {
		return 0, true
	}
	if v.Kind != yaml.ScalarNode {
		return 0, false
	}
	n, err := strconv.Atoi(v.Value)
	if err != nil {
		return 0, false
	}
	return n, true
}

// SetVersion stamps a top-level integer schema version under key on the root
// mapping node, replacing any existing value.
func SetVersion(root *yaml.Node, key string, v int) {
	node := ScalarNode(strconv.Itoa(v))
	node.Tag = "!!int"
	MapSet(root, key, node)
}

// reprise:ignore — shares the walk-the-Content-pairs idiom with
// agentcoord/spool's mappingGet/mappingSet/mappingDelete. The duplication is
// real and was weighed: neither package may import the other (spool is agent
// coordination, upgrade is config-schema migration), so collapsing them needs a
// third package for four small functions. Ruled 2026-08-20 not worth the
// boundary. The forcing function is gone too — MapEntry, the dead member whose
// deletion this group used to block, is deleted.
//
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

// Reporter is how an upgrade step reports a LOSSY change — a user-set value it
// had to drop — to whoever is driving the pipeline. The Upgrader interface has
// no return channel for this and deliberately keeps none: a step that silently
// discards a setting is the failure this exists to prevent, and a caller that
// wants the report must pass somewhere to put it.
//
// It is a plain callback rather than a shared sink type so a step can live in
// its own package (see internal/config/migrate) without that package and its
// driver having to agree on a concrete buffer. A nil Reporter is legal and
// means the caller is not collecting; call it through a step's own helper that
// nil-checks, never directly.
type Reporter func(format string, args ...any)
