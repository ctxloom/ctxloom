package clifmt

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

// humanFieldInfo is what the human views need of one struct field.
type humanFieldInfo struct {
	name      string // the json name, or the Go name when untagged: the path segment
	label     string
	col       string
	omitempty bool
}

// humanField resolves a visible struct field for the human views: its label,
// its column header and its omitempty flag, or ok=false when the field is
// unexported, json:"-", or hidden by clifmt:"-".
func humanField(hints *typeHints, sf reflect.StructField) (humanFieldInfo, bool) {
	if !sf.IsExported() {
		return humanFieldInfo{}, false
	}
	jsonName, skip, omitempty := parseJSONTag(sf.Tag)
	h := hints.of(sf)
	if skip || h.hide {
		return humanFieldInfo{}, false
	}
	label := resolveLabel(sf, jsonName, h)
	name := jsonName
	if name == "" {
		name = sf.Name
	}
	return humanFieldInfo{name: name, label: label, col: resolveCol(h, label), omitempty: omitempty}, true
}

type fieldKind int

// The field kinds, in the order their blocks appear within a struct.
const (
	fieldKindScalar fieldKind = iota
	fieldKindSection
	fieldKindTable
	fieldKindCount
)

// classifyField decides how an already-dereferenced field value should be
// modeled. A struct that implements fmt.Stringer (e.g. time.Time) is treated
// as a scalar rather than a section, since it has a canonical human string
// form. A slice/array of struct becomes a table; a slice of scalars is
// stringified as a comma-joined scalar line.
func classifyField(v reflect.Value) fieldKind {
	if !v.IsValid() {
		return fieldKindScalar
	}
	if implementsStringer(v) {
		return fieldKindScalar
	}
	switch v.Kind() {
	case reflect.Struct:
		return fieldKindSection
	case reflect.Slice, reflect.Array:
		elem := derefType(v.Type().Elem())
		if elem.Kind() == reflect.Struct && !typeImplementsStringer(elem) {
			return fieldKindTable
		}
		return fieldKindScalar
	default:
		return fieldKindScalar
	}
}

// tableCellString stringifies a row field for a table cell. Nested
// struct/slice fields (rare inside a table row) fall back to a compact
// fmt.Sprintf rather than recursing into another table, since a table cell
// has no room for a nested table.
func tableCellString(v reflect.Value) string {
	deref := derefValue(v)
	if !deref.IsValid() {
		return ""
	}
	switch classifyField(deref) {
	case fieldKindScalar:
		return scalarString(v)
	default:
		return fmt.Sprintf("%v", deref.Interface())
	}
}

// scalarString renders a leaf field value as a human string. Pointers
// dereference (nil -> ""); types implementing fmt.Stringer or error use that
// form; everything else uses a kind-appropriate strconv call to avoid
// Go's default float/quote noise from fmt's %v on strings.
func scalarString(v reflect.Value) string {
	v = derefValue(v)
	if !v.IsValid() {
		return ""
	}
	if s, ok := stringerString(v); ok {
		return s
	}
	if err, ok := v.Interface().(error); ok {
		return err.Error()
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case reflect.Slice, reflect.Array:
		return joinSlice(v)
	case reflect.Map:
		return joinMap(v)
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}

// hasStringForm reports whether scalarString renders v whole (through
// fmt.Stringer or error) rather than by its kind.
func hasStringForm(v reflect.Value) bool {
	if implementsStringer(v) {
		return true
	}
	return v.CanInterface() && v.Type().Implements(errorType)
}

var errorType = reflect.TypeFor[error]()

func joinSlice(v reflect.Value) string {
	out := ""
	for i := 0; i < v.Len(); i++ {
		if i > 0 {
			out += ", "
		}
		out += scalarString(v.Index(i))
	}
	return out
}

func joinMap(v reflect.Value) string {
	keys := v.MapKeys()
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%v=%s", k.Interface(), scalarString(v.MapIndex(k))))
	}
	sort.Strings(pairs)
	out := ""
	for i, p := range pairs {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func derefValue(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

var stringerType = reflect.TypeOf((*fmt.Stringer)(nil)).Elem()

// implementsStringer reports whether v has a canonical human string form. It
// asks the same question of a VALUE that typeImplementsStringer asks of a
// TYPE, and must keep giving the same answer: a String() declared on the
// pointer receiver still gives the type a canonical form, and addressability
// is a property of the call site rather than of the type. When the two
// disagreed, a slice of such a struct was ruled "stringable" (so not rendered
// as a table) by the type-level check and then failed the value-level one,
// falling through to Go's default struct dump — neither a table nor a String().
func implementsStringer(v reflect.Value) bool {
	return v.IsValid() && typeImplementsStringer(v.Type())
}

func typeImplementsStringer(t reflect.Type) bool {
	return t.Implements(stringerType) || reflect.PointerTo(t).Implements(stringerType)
}

// stringerString returns v's fmt.Stringer form, honoring a String() declared
// on the POINTER receiver by addressing the value — or an addressable copy of
// it, since a reflect.Value reached through an interface is never addressable.
// Without the copy, every pointer-receiver Stringer would be classified as
// stringable and then fail to produce its string.
func stringerString(v reflect.Value) (string, bool) {
	if !v.IsValid() || !v.CanInterface() {
		return "", false
	}
	if v.Type().Implements(stringerType) {
		if v.Kind() == reflect.Pointer && v.IsNil() {
			return "", true
		}
		return v.Interface().(fmt.Stringer).String(), true
	}
	if !reflect.PointerTo(v.Type()).Implements(stringerType) {
		return "", false
	}
	if v.CanAddr() {
		return v.Addr().Interface().(fmt.Stringer).String(), true
	}
	addressable := reflect.New(v.Type())
	addressable.Elem().Set(v)
	return addressable.Interface().(fmt.Stringer).String(), true
}
