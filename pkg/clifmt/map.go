package clifmt

import (
	"bytes"
	"encoding"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strconv"
)

// Map is a string-keyed map that keeps insertion order. Use it for dynamic
// data whose order carries meaning (the columns of a dynamic table, a config
// section as written). A plain Go map renders with its keys sorted, the way
// encoding/json writes it. The zero Map is empty and ready to use.
type Map struct {
	keys []string
	vals map[string]any
}

// NewMap returns an empty Map.
func NewMap() *Map { return &Map{} }

// Set sets key to v and returns m, so calls chain. Re-setting a key keeps
// its position.
func (m *Map) Set(key string, v any) *Map {
	if m.vals == nil {
		m.vals = map[string]any{}
	}
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = v
	return m
}

// Get returns the value at key and whether key is set.
func (m *Map) Get(key string) (any, bool) {
	v, ok := m.vals[key]
	return v, ok
}

// Keys returns the keys in insertion order (a copy).
func (m *Map) Keys() []string { return slices.Clone(m.keys) }

// Len returns the number of keys.
func (m *Map) Len() int { return len(m.keys) }

// MarshalJSON writes m as a JSON object in insertion order. A nil slice in a
// value is written `[]`, as everywhere else clifmt renders. It has a value
// receiver so a Map held by value encodes the same as a *Map.
func (m Map) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(emptyNilSlices(m.vals[k]))
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

var mapType = reflect.TypeFor[Map]()

// isMapLike reports whether the dereferenced v is a Go map or a Map.
func isMapLike(v reflect.Value) bool {
	return v.IsValid() && (v.Kind() == reflect.Map || v.Type() == mapType)
}

// entry is one key/value of a map-like value.
type entry struct {
	key string
	val reflect.Value
}

// mapEntries returns the entries of a map-like dv in render order: a Map's
// insertion order, or a Go map's keys stringified and sorted as
// encoding/json does.
func mapEntries(dv reflect.Value) []entry {
	if dv.Type() == mapType {
		m := dv.Interface().(Map)
		out := make([]entry, len(m.keys))
		for i, k := range m.keys {
			out[i] = entry{key: k, val: reflect.ValueOf(m.vals[k])}
		}
		return out
	}
	out := make([]entry, 0, dv.Len())
	iter := dv.MapRange()
	for iter.Next() {
		out = append(out, entry{key: mapKeyString(iter.Key()), val: iter.Value()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// mapKeyString stringifies a map key the way encoding/json does: a string
// kind as itself, an encoding.TextMarshaler through MarshalText, an integer
// in decimal.
func mapKeyString(k reflect.Value) string {
	if k.Kind() == reflect.String {
		return k.String()
	}
	if tm, ok := k.Interface().(encoding.TextMarshaler); ok {
		if k.Kind() == reflect.Pointer && k.IsNil() {
			return ""
		}
		if b, err := tm.MarshalText(); err == nil {
			return string(b)
		}
	}
	switch k.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(k.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(k.Uint(), 10)
	}
	return scalarString(k)
}

// mapList describes a list whose every element is map-like: whether it can be
// a table (every element has the same key set and only scalar values) and,
// if so, its columns in the first element's order.
type mapList struct {
	uniform bool
	columns []string
}

// mapListShape reports whether dv (a slice or array) is a non-empty list of
// map-like elements, and its shape.
func mapListShape(dv reflect.Value) (mapList, bool) {
	if dv.Len() == 0 {
		return mapList{}, false
	}
	var first []entry
	uniform := true
	for i := 0; i < dv.Len(); i++ {
		ev := derefValue(dv.Index(i))
		if !isMapLike(ev) {
			return mapList{}, false
		}
		es := mapEntries(ev)
		if i == 0 {
			first = es
		}
		uniform = uniform && sameKeys(first, es) && allScalar(es)
	}
	if !uniform {
		return mapList{}, true
	}
	cols := make([]string, len(first))
	for i, e := range first {
		cols[i] = e.key
	}
	return mapList{uniform: true, columns: cols}, true
}

func sameKeys(a, b []entry) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make(map[string]bool, len(a))
	for _, e := range a {
		keys[e.key] = true
	}
	for _, e := range b {
		if !keys[e.key] {
			return false
		}
	}
	return true
}

func allScalar(es []entry) bool {
	for _, e := range es {
		if !isScalarValue(derefValue(e.val)) {
			return false
		}
	}
	return true
}

// isScalarValue reports whether a dereferenced value renders as one plain
// value: nil, a value with a string form, or a non-composite kind.
func isScalarValue(dv reflect.Value) bool {
	if !dv.IsValid() || hasStringForm(dv) {
		return true
	}
	switch dv.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
		return false
	}
	return true
}
