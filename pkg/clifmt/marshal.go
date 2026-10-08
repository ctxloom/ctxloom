package clifmt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	toml "github.com/pelletier/go-toml/v2"
	yaml "gopkg.in/yaml.v3"
)

// renderJSON marshals v directly via encoding/json, which is the canonical
// implementation of the json: tag convention (naming, "-", omitempty,
// embedding) — no need to reinvent it. Nil slices are emptied first (see
// emptyNilSlices) so a list is always `[]`, never `null`.
func renderJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(emptyNilSlices(v))
}

// toGeneric round-trips v through encoding/json into a generic tree of
// *ordered objects, []any and scalars. This is how yaml/toml rendering gets
// the same field identity as JSON (names, omission via json:"-", omitempty,
// json.Marshaler) without depending on gopkg.in/yaml.v3 or go-toml/v2's own
// struct tags, which are "yaml:"/"toml:" and know nothing about json:"-".
// Objects keep the key order json wrote, so yaml follows the json contract's
// order. Numbers are normalized back to int64/uint64/float64 so downstream
// encoders emit bare numbers instead of quoted strings. Nil slices are
// emptied first, exactly as renderJSON does, so yaml/toml agree with json.
func toGeneric(v any) (any, error) {
	b, err := json.Marshal(emptyNilSlices(v))
	if err != nil {
		return nil, fmt.Errorf("clifmt: marshaling %T to derive field identity: %w", v, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	generic, err := decodeOrdered(dec)
	if err != nil {
		return nil, fmt.Errorf("clifmt: decoding generic form of %T: %w", v, err)
	}
	return generic, nil
}

// ordered is a JSON object with its key order kept.
type ordered struct {
	keys []string
	vals []any
}

// decodeOrdered reads one JSON value from dec.
func decodeOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			return decodeObject(dec)
		}
		return decodeArray(dec)
	case json.Number:
		return normalizeNumber(t), nil
	default:
		return t, nil
	}
}

func decodeObject(dec *json.Decoder) (*ordered, error) {
	obj := &ordered{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		val, err := decodeOrdered(dec)
		if err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, kt.(string))
		obj.vals = append(obj.vals, val)
	}
	_, err := dec.Token() // '}'
	return obj, err
}

func decodeArray(dec *json.Decoder) ([]any, error) {
	arr := []any{}
	for dec.More() {
		val, err := decodeOrdered(dec)
		if err != nil {
			return nil, err
		}
		arr = append(arr, val)
	}
	_, err := dec.Token() // ']'
	return arr, err
}

func normalizeNumber(t json.Number) any {
	if i, err := t.Int64(); err == nil {
		return i
	}
	// An unsigned integer above math.MaxInt64 fails Int64 but SUCCEEDS
	// Float64, so without this arm it would reach the yaml/toml encoders
	// as a float64 and be written in exponent form with its low bits
	// gone — while json, which never round-trips through toGeneric,
	// writes the exact digits. Try the exact integer form before ever
	// accepting the lossy one.
	if u, err := strconv.ParseUint(t.String(), 10, 64); err == nil {
		return u
	}
	if f, err := t.Float64(); err == nil {
		return f
	}
	return t.String()
}

// yamlNode builds the yaml.v3 node for a generic value, mappings in key
// order.
func yamlNode(v any) (*yaml.Node, error) {
	switch t := v.(type) {
	case *ordered:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for i, k := range t.keys {
			kn, err := yamlNode(k)
			if err != nil {
				return nil, err
			}
			vn, err := yamlNode(t.vals[i])
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, kn, vn)
		}
		return n, nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, e := range t {
			en, err := yamlNode(e)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, en)
		}
		return n, nil
	default:
		n := &yaml.Node{}
		if err := n.Encode(t); err != nil {
			return nil, err
		}
		return n, nil
	}
}

// plainTree turns ordered objects into map[string]any, for encoders with no
// ordered form.
func plainTree(v any) any {
	switch t := v.(type) {
	case *ordered:
		m := make(map[string]any, len(t.keys))
		for i, k := range t.keys {
			m[k] = plainTree(t.vals[i])
		}
		return m
	case []any:
		for i, e := range t {
			t[i] = plainTree(e)
		}
		return t
	default:
		return v
	}
}

// renderYAML marshals v generically via yaml.v3, using toGeneric so its
// keys, their order and omissions match the json contract. It indents two spaces,
// the common convention for hand-edited YAML; yaml.v3's own default is four,
// so the indent is set on the encoder rather than inherited.
// reprise:accept-drift
func renderYAML(w io.Writer, v any) error {
	generic, err := toGeneric(v)
	if err != nil {
		return err
	}
	node, err := yamlNode(generic)
	if err != nil {
		return fmt.Errorf("clifmt: yaml marshal: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		_ = enc.Close()
		return fmt.Errorf("clifmt: yaml marshal: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("clifmt: yaml marshal: %w", err)
	}
	_, err = w.Write(buf.Bytes())
	return err
}

// renderTOML marshals v generically via go-toml/v2. Its keys are sorted:
// go-toml/v2 sorts map keys and has no ordered generic form, so unlike yaml
// it cannot follow the json contract's order. TOML documents must be
// a table at the root, so a top-level slice or scalar Result (e.g. a bare
// []T from a list command) is wrapped under an "items" key; a top-level
// struct is already a table and passes through unwrapped.
func renderTOML(w io.Writer, v any) error {
	generic, err := toGeneric(v)
	if err != nil {
		return err
	}
	plain := plainTree(generic)
	root, ok := plain.(map[string]any)
	if !ok {
		root = map[string]any{"items": plain}
	}
	if tomlRootIsEmpty(root) {
		// TOML has no way to represent a bare null/empty value at
		// the document root (unlike JSON's `null`/`{}` or YAML's `null`) —
		// go-toml/v2 silently DROPS a nil map value (confirmed: Marshal(map[
		// string]any{"items": nil}) and Marshal(map[string]any{}) both
		// return zero bytes, nil error), so a nil v (or an all-omitempty
		// struct, which round-trips through toGeneric to the same empty
		// map) rendered nothing here — indistinguishable from a write that
		// never happened, while json/yaml render the identical value as
		// `null`/`{}`. A leading comment is valid TOML (parses back to an
		// empty table) and makes "there was nothing to render" visible.
		_, err := io.WriteString(w, "# (none)\n")
		return err
	}
	b, err := toml.Marshal(root)
	if err != nil {
		return fmt.Errorf("clifmt: toml marshal: %w", err)
	}
	_, err = w.Write(b)
	return err
}

// tomlRootIsEmpty reports whether root would marshal to zero bytes: either
// genuinely empty, or its only key is the non-map "items" wrapper holding a
// nil value (go-toml/v2 drops a nil-valued map entry outright rather than
// erroring or emitting anything for it).
func tomlRootIsEmpty(root map[string]any) bool {
	if len(root) == 0 {
		return true
	}
	if len(root) == 1 {
		if v, ok := root["items"]; ok && v == nil {
			return true
		}
	}
	return false
}
