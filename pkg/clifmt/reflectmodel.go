package clifmt

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

// Node is the reflective model of a struct value: its fields bucketed by how
// the text and markdown renderers present them. Scalars become "Label:
// value" lines, Sections become nested headings, Tables become slices of
// struct rendered as tables. Order within each slice follows the struct's
// declared field order.
type Node struct {
	Scalars  []ScalarField
	Sections []SectionField
	Tables   []TableField
}

// Empty reports whether node carries no scalars, sections, or tables — the
// case renderNode (noderender.go) writes zero bytes for and returns nil
// (U154-F02: an all-omitempty struct silently rendered as nothing, in both
// text and markdown, indistinguishable from a write that never happened).
// Callers use this to render an explicit "(none)" marker instead.
func (n *Node) Empty() bool {
	return len(n.Scalars) == 0 && len(n.Sections) == 0 && len(n.Tables) == 0
}

// ScalarField is one "Label: value" line.
type ScalarField struct {
	Label string
	Value string
}

// SectionField is a nested struct field, rendered as its own heading.
type SectionField struct {
	Label string
	Node  *Node
}

// TableField is a slice-of-struct field, rendered as a table.
type TableField struct {
	Label string
	Table *Table
}

// Table is an aligned column model: Columns is the header row, Rows is the
// stringified body, one slice per row with the same length as Columns.
type Table struct {
	Columns []string
	Rows    [][]string
}

// buildNode reflects over a struct value (or pointer to one) and produces
// its Node model. It is the shared entry point the text and markdown
// renderers use for any struct-shaped Result.
func buildNode(v reflect.Value) (*Node, error) {
	v = derefValue(v)
	if !v.IsValid() {
		return &Node{}, nil
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("clifmt: buildNode requires a struct, got %s", v.Kind())
	}

	node := &Node{}
	for _, sf := range reflect.VisibleFields(v.Type()) {
		if !sf.IsExported() {
			continue
		}
		fv, err := v.FieldByIndexErr(sf.Index)
		if err != nil {
			// A nil embedded pointer along the path: nothing to show.
			continue
		}

		jsonName, skip, omitempty := parseJSONTag(sf.Tag)
		if skip {
			continue
		}
		label := resolveLabel(sf, jsonName)

		if omitempty && isEmptyValue(fv) {
			continue
		}

		deref := derefValue(fv)
		switch classifyField(deref) {
		case fieldKindSection:
			sub, err := buildNode(deref)
			if err != nil {
				return nil, err
			}
			node.Sections = append(node.Sections, SectionField{Label: label, Node: sub})
		case fieldKindTable:
			tbl, err := buildTable(deref)
			if err != nil {
				return nil, err
			}
			node.Tables = append(node.Tables, TableField{Label: label, Table: tbl})
		default:
			node.Scalars = append(node.Scalars, ScalarField{Label: label, Value: scalarString(fv)})
		}
	}
	return node, nil
}

type fieldKind int

const (
	fieldKindScalar fieldKind = iota
	fieldKindSection
	fieldKindTable
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

// buildTable reflects a slice (or array) of struct into a Table. Columns are
// derived from the element type so an empty slice still reports its headers.
func buildTable(v reflect.Value) (*Table, error) {
	v = derefValue(v)
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
		return nil, fmt.Errorf("clifmt: buildTable requires a slice or array, got %s", v.Kind())
	}
	elemType := derefType(v.Type().Elem())
	if elemType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("clifmt: buildTable requires a slice of struct, got slice of %s", elemType.Kind())
	}

	cols := reflect.VisibleFields(elemType)
	var columns []string
	var indices [][]int
	for _, sf := range cols {
		if !sf.IsExported() {
			continue
		}
		jsonName, skip, _ := parseJSONTag(sf.Tag)
		if skip {
			continue
		}
		label := resolveLabel(sf, jsonName)
		columns = append(columns, resolveCol(sf, label))
		indices = append(indices, sf.Index)
	}

	tbl := &Table{Columns: columns, Rows: [][]string{}}
	for i := 0; i < v.Len(); i++ {
		elem := derefValue(v.Index(i))
		row := make([]string, len(indices))
		for c, index := range indices {
			if !elem.IsValid() {
				continue
			}
			fv, err := elem.FieldByIndexErr(index)
			if err != nil {
				continue
			}
			row[c] = tableCellString(fv)
		}
		tbl.Rows = append(tbl.Rows, row)
	}
	return tbl, nil
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
