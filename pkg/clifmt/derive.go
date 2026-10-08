package clifmt

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// deriver walks a value into a Doc for one call, consulting the custom views
// at every node in precedence order: the call's path views, the call's type
// views, the Printer's type views, then derivation.
type deriver struct {
	format       Format
	paths        map[string]viewFn
	callTypes    map[reflect.Type]viewFn
	printerTypes map[reflect.Type]viewFn
}

// nodeRef is one node of the walk: its value, its json path, the label it
// derives with, and its own derivation (the lowest layer).
type nodeRef struct {
	v     reflect.Value
	path  string
	label string
	base  func() (Doc, error)
}

// layers returns the views that apply to the node at path holding v, most
// specific first.
func (d *deriver) layers(path string, v reflect.Value) []viewFn {
	var ls []viewFn
	if fn, ok := d.paths[path]; ok {
		ls = append(ls, fn)
	}
	if dv := derefValue(v); dv.IsValid() && dv.CanInterface() {
		if fn, ok := d.callTypes[dv.Type()]; ok {
			ls = append(ls, fn)
		}
		if fn, ok := d.printerTypes[dv.Type()]; ok {
			ls = append(ls, fn)
		}
	}
	return ls
}

// node derives n through every layer that applies to it.
func (d *deriver) node(n nodeRef) (Doc, error) {
	return d.eval(n, d.layers(n.path, n.v))
}

// eval runs the top layer of layers over n, handing it the rest as
// ViewCtx.Derived; with no layer left it derives n.
func (d *deriver) eval(n nodeRef, layers []viewFn) (Doc, error) {
	if len(layers) == 0 {
		return n.base()
	}
	doc, err := layers[0](&ViewCtx{d: d, n: n, next: layers[1:]}, derefValue(n.v))
	if err != nil {
		var ve *viewError
		if errors.As(err, &ve) {
			return nil, err
		}
		return nil, &viewError{path: n.path, err: err}
	}
	return doc, nil
}

// viewError names the node whose custom view failed.
type viewError struct {
	path string
	err  error
}

func (e *viewError) Error() string {
	return fmt.Sprintf("clifmt: view for %s: %v", pathName(e.path), e.err)
}

func (e *viewError) Unwrap() error { return e.err }

func pathName(path string) string {
	if path == "" {
		return "the root"
	}
	return fmt.Sprintf("%q", path)
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// root derives a whole value: a struct is its fields, a list of structs a
// table, a list of scalars a List, anything else one Para.
func (d *deriver) root(v any) (Doc, error) {
	rv := reflect.ValueOf(v)
	return d.node(nodeRef{v: rv, base: func() (Doc, error) { return d.deriveRoot(rv) }})
}

func (d *deriver) deriveRoot(rv reflect.Value) (Doc, error) {
	dv := derefValue(rv)
	switch {
	case !dv.IsValid():
		return Doc{Para("(nil)")}, nil
	case dv.Kind() == reflect.Struct && !implementsStringer(dv):
		return d.structBlocks(dv, "")
	case isTableSlice(dv):
		return d.tableBlocks(dv, "", "")
	case dv.Kind() == reflect.Slice || dv.Kind() == reflect.Array:
		items := make([]string, dv.Len())
		for i := range items {
			s, err := d.value(dv.Index(i), "[]", "", scalarString)
			if err != nil {
				return nil, err
			}
			items[i] = s
		}
		if len(items) == 0 {
			// An empty scalar list has no header row to show; Render marks
			// the empty Doc "(none)".
			return nil, nil
		}
		return Doc{List{Items: items}}, nil
	default:
		return Doc{Para(scalarString(dv))}, nil
	}
}

// isTableSlice reports whether dv is a list whose elements are structs with
// no canonical string form: the shape that renders as a table.
func isTableSlice(dv reflect.Value) bool {
	if dv.Kind() != reflect.Slice && dv.Kind() != reflect.Array {
		return false
	}
	elem := derefType(dv.Type().Elem())
	return elem.Kind() == reflect.Struct && !typeImplementsStringer(elem)
}

// structBlocks derives a struct's fields: scalar lines first, then sections,
// then tables, each in declaration order. A field's custom view stays in its
// field's place in that order.
func (d *deriver) structBlocks(dv reflect.Value, path string) (Doc, error) {
	if !dv.IsValid() {
		return nil, nil
	}
	hints, err := hintsFor(dv.Type())
	if err != nil {
		return nil, err
	}
	var buckets [fieldKindCount]Doc
	for _, sf := range reflect.VisibleFields(dv.Type()) {
		hf, ok := humanField(hints, sf)
		if !ok {
			continue
		}
		fv, err := dv.FieldByIndexErr(sf.Index)
		if err != nil {
			// A nil embedded pointer along the path: nothing to show.
			continue
		}
		if hf.omitempty && isEmptyValue(fv) {
			continue
		}
		doc, kind, err := d.field(fv, joinPath(path, hf.name), hf.label)
		if err != nil {
			return nil, err
		}
		buckets[kind] = append(buckets[kind], doc...)
	}
	var out Doc
	for _, b := range buckets {
		out = append(out, b...)
	}
	return out, nil
}

// field derives one struct field: a nested struct is a Section, a list of
// structs a titled Table, anything else a Field line.
func (d *deriver) field(fv reflect.Value, path, label string) (Doc, fieldKind, error) {
	dv := derefValue(fv)
	kind := classifyField(dv)
	base := func() (Doc, error) {
		switch kind {
		case fieldKindSection:
			body, err := d.structBlocks(dv, path)
			return Doc{Section{Title: label, Body: body}}, err
		case fieldKindTable:
			return d.tableBlocks(dv, path, label)
		default:
			s, err := d.scalarValue(fv, path)
			return Doc{Field{Label: label, Value: s}}, err
		}
	}
	doc, err := d.node(nodeRef{v: fv, path: path, label: label, base: base})
	return labelLonePara(doc, label), kind, err
}

// labelLonePara reads a view's lone Para in a field's place as the field's
// value, so a view that only reformats a value (a duration, a time) keeps the
// field's label: "Elapsed: 1m30s", not a bare "1m30s".
func labelLonePara(doc Doc, label string) Doc {
	if len(doc) == 1 {
		if p, ok := doc[0].(Para); ok {
			return Doc{Field{Label: label, Value: string(p)}}
		}
	}
	return doc
}

// scalarValue is a scalar field's value text. A list of scalars joins its
// elements, each of which a view may restyle.
func (d *deriver) scalarValue(fv reflect.Value, path string) (string, error) {
	dv := derefValue(fv)
	if !dv.IsValid() || (dv.Kind() != reflect.Slice && dv.Kind() != reflect.Array) || hasStringForm(dv) {
		return scalarString(fv), nil
	}
	parts := make([]string, dv.Len())
	for i := range parts {
		s, err := d.value(dv.Index(i), path+"[]", "", scalarString)
		if err != nil {
			return "", err
		}
		parts[i] = s
	}
	return strings.Join(parts, ", "), nil
}

// tableBlocks derives a list of structs as a Table titled title. When a view
// applies to its elements, the elements cannot be rows (a view returns
// blocks, a row is cells), so the list renders as one block run per element
// instead, each derived as a Section titled "[n]".
func (d *deriver) tableBlocks(dv reflect.Value, path, title string) (Doc, error) {
	elemPath := path + "[]"
	elemType := derefType(dv.Type().Elem())
	if d.hasViews(elemPath, elemType) {
		return d.elementBlocks(dv, elemPath, title)
	}
	hints, err := hintsFor(elemType)
	if err != nil {
		return nil, err
	}
	type column struct {
		index []int
		name  string
		label string
	}
	var cols []column
	tbl := Table{Title: title, Rows: [][]string{}}
	for _, sf := range reflect.VisibleFields(elemType) {
		hf, ok := humanField(hints, sf)
		if !ok {
			continue
		}
		cols = append(cols, column{index: sf.Index, name: hf.name, label: hf.label})
		tbl.Columns = append(tbl.Columns, hf.col)
	}
	for i := 0; i < dv.Len(); i++ {
		elem := derefValue(dv.Index(i))
		row := make([]string, len(cols))
		if !elem.IsValid() {
			// A nil element of a slice-of-pointer: no fields to read, but the
			// row still occupies its place so row indexes keep matching the
			// caller's slice indexes.
			tbl.Rows = append(tbl.Rows, row)
			continue
		}
		for c, col := range cols {
			fv, err := elem.FieldByIndexErr(col.index)
			if err != nil {
				// A nil embedded pointer along this promoted field's index
				// path: nothing to show for this ONE cell. The column stays
				// in the header and the cell stays empty, so the row still
				// lines up with its siblings.
				continue
			}
			s, err := d.value(fv, joinPath(elemPath, col.name), col.label, tableCellString)
			if err != nil {
				return nil, err
			}
			row[c] = s
		}
		tbl.Rows = append(tbl.Rows, row)
	}
	return Doc{tbl}, nil
}

// hasViews reports whether any view addresses the elements at elemPath.
func (d *deriver) hasViews(elemPath string, elemType reflect.Type) bool {
	_, p := d.paths[elemPath]
	_, c := d.callTypes[elemType]
	_, pr := d.printerTypes[elemType]
	return p || c || pr
}

// elementBlocks derives each element of a list of structs as its own node.
func (d *deriver) elementBlocks(dv reflect.Value, elemPath, title string) (Doc, error) {
	var seq Doc
	for i := 0; i < dv.Len(); i++ {
		ev := dv.Index(i)
		label := fmt.Sprintf("[%d]", i+1)
		doc, err := d.node(nodeRef{v: ev, path: elemPath, label: label, base: func() (Doc, error) {
			body, err := d.structBlocks(derefValue(ev), elemPath)
			return Doc{Section{Title: label, Body: body}}, err
		}})
		if err != nil {
			return nil, err
		}
		seq = append(seq, doc...)
	}
	if title == "" {
		return seq, nil
	}
	return Doc{Section{Title: title, Body: seq}}, nil
}

// value derives a node that renders as one string: a table cell, a list
// item, an element of a joined list. base is its derived text. A view's
// Doc is read inline: a Para is its text, a Field its value, a List its
// items joined; several blocks join with ", ".
func (d *deriver) value(v reflect.Value, path, label string, base func(reflect.Value) string) (string, error) {
	ls := d.layers(path, v)
	if len(ls) == 0 {
		return base(v), nil
	}
	doc, err := d.eval(nodeRef{v: v, path: path, label: label, base: func() (Doc, error) {
		return Doc{Field{Label: label, Value: base(v)}}, nil
	}}, ls)
	if err != nil {
		return "", err
	}
	return inlineDoc(doc, path)
}

func inlineDoc(doc Doc, path string) (string, error) {
	parts := make([]string, 0, len(doc))
	for _, b := range doc {
		switch b := b.(type) {
		case Para:
			parts = append(parts, string(b))
		case Field:
			parts = append(parts, b.Value)
		case List:
			parts = append(parts, strings.Join(b.Items, ", "))
		default:
			return "", fmt.Errorf("clifmt: view for %s returned a %T where one value is rendered (a table cell or list item)", pathName(path), b)
		}
	}
	return strings.Join(parts, ", "), nil
}
