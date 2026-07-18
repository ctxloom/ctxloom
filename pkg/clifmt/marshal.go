package clifmt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	toml "github.com/pelletier/go-toml/v2"
	yaml "gopkg.in/yaml.v3"
)

// renderJSON marshals v directly via encoding/json, which is the canonical
// implementation of the json: tag convention (naming, "-", omitempty,
// embedding) — no need to reinvent it.
func renderJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// toGeneric round-trips v through encoding/json into a generic
// map[string]any / []any / scalar tree. This is how yaml/toml rendering
// gets the same field identity as JSON (names, omission via json:"-",
// omitempty) without depending on gopkg.in/yaml.v3 or go-toml/v2's own
// struct tags, which are "yaml:"/"toml:" and know nothing about json:"-".
// json.Number results are normalized back to int64/float64 so downstream
// encoders emit bare numbers instead of quoted strings.
func toGeneric(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("clifmt: marshaling %T to derive field identity: %w", v, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("clifmt: decoding generic form of %T: %w", v, err)
	}
	return normalizeNumbers(generic), nil
}

func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case map[string]any:
		for k, vv := range t {
			t[k] = normalizeNumbers(vv)
		}
		return t
	case []any:
		for i, vv := range t {
			t[i] = normalizeNumbers(vv)
		}
		return t
	default:
		return v
	}
}

// renderYAML marshals v generically via yaml.v3, using toGeneric so its
// keys and omissions match the json: tag convention.
func renderYAML(w io.Writer, v any) error {
	generic, err := toGeneric(v)
	if err != nil {
		return err
	}
	b, err := yaml.Marshal(generic)
	if err != nil {
		return fmt.Errorf("clifmt: yaml marshal: %w", err)
	}
	_, err = w.Write(b)
	return err
}

// renderTOML marshals v generically via go-toml/v2. TOML documents must be
// a table at the root, so a top-level slice or scalar Result (e.g. a bare
// []T from a list command) is wrapped under an "items" key; a top-level
// struct is already a table and passes through unwrapped.
func renderTOML(w io.Writer, v any) error {
	generic, err := toGeneric(v)
	if err != nil {
		return err
	}
	root, ok := generic.(map[string]any)
	if !ok {
		root = map[string]any{"items": generic}
	}
	b, err := toml.Marshal(root)
	if err != nil {
		return fmt.Errorf("clifmt: toml marshal: %w", err)
	}
	_, err = w.Write(b)
	return err
}
