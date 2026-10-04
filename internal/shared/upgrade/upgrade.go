// Package upgrade is ctxloom's on-disk schema upgrade engine: an older
// on-disk representation is upgraded to the current one *in memory* on load,
// and persisting the result is a separate, explicit act of the caller.
//
// An Upgrader is one schema step; a Pipeline is an ordered chain of them run
// over raw file bytes. The layer is YAML-document oriented — parse once,
// re-encode once — and version-aware via the Version/SetVersion helpers.
// Versioned file kinds do not drive a Pipeline themselves: they declare their
// steps on a schemaver.Kind, which owns the version gate and calls into this
// package for the parse, the steps and the encode.
package upgrade

import (
	"bytes"
	"errors"
	"io"
	"strconv"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
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
type Pipeline []Upgrader

// Run is the byte driver: it parses data into a YAML document, applies the
// pipeline to the root mapping node, and re-encodes only if some stage changed
// it. When nothing changes — or the input is malformed, not a mapping, or
// carries more than one document — the original bytes are returned verbatim (no
// reserialization), leaving the normal parse path to surface any real error.
// applied lists the names of the stages that fired, in order, for the caller's
// rewrite prompt.
func (p Pipeline) Run(data []byte) (out []byte, applied []string) {
	doc, err := DecodeSingle(data)
	if err != nil {
		return data, nil
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data, nil
	}
	root := doc.Content[0]
	if HasDuplicateKey(root) {
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
	encoded, err := Encode(&doc)
	if err != nil {
		return data, nil
	}
	return encoded, applied
}

// ErrMultiDocument reports a YAML stream carrying more than one document.
var ErrMultiDocument = errors.New("more than one YAML document")

// DecodeSingle parses data as a YAML stream carrying EXACTLY ONE document and
// returns that document's node. An empty or comment-only stream returns
// io.EOF (yaml.v3 keeps no node for it); a stream carrying a second document
// returns ErrMultiDocument, because a caller that re-encodes the node it
// parsed would otherwise emit a single-document file, silently deleting every
// later one. An upgrade rewrites a document in place or not at all — it never
// narrows a stream.
func DecodeSingle(data []byte) (doc yaml.Node, err error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		return doc, err
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		if err == nil {
			err = ErrMultiDocument
		}
		return doc, err
	}
	return doc, nil
}

// Encode serializes a document node the way every upgrade writes one back:
// two-space indentation.
func Encode(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// HasDuplicateKey reports whether any mapping in the subtree rooted at n names
// the same key twice. Such a document is malformed — every struct/map decode in
// the codebase refuses it — but a yaml.Node decode accepts it, and yamlx's
// MapValue, MapSet and MapDelete all act on the FIRST match. An upgrade run over it
// therefore rewrites one of the two entries and leaves the other under the
// legacy key, producing a document that no longer has a duplicate and so parses
// cleanly, carrying whichever value the helpers happened to reach. Refusing to
// upgrade it keeps the loud parse error the caller would otherwise have got.
func HasDuplicateKey(n *yaml.Node) bool {
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
		if HasDuplicateKey(child) {
			return true
		}
	}
	return false
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
	v := yamlx.MapValue(root, key)
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
	node := yamlx.ScalarNode(strconv.Itoa(v))
	node.Tag = "!!int"
	yamlx.MapSet(root, key, node)
}
