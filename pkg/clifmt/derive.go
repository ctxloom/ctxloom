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

// root derives a whole value: a struct or map is its members, a list of
// structs or of uniform maps a table, a list of other maps a run of
// sections, a list of scalars a List, anything else one Para.
func (d *deriver) root(v any) (Doc, error) {
	rv := reflect.ValueOf(v)
	return d.node(nodeRef{v: rv, base: func() (Doc, error) { return d.deriveRoot(rv) }})
}

func (d *deriver) deriveRoot(rv reflect.Value) (Doc, error) {
	dv := derefValue(rv)
	kind := classifyField(dv)
	switch {
	case !dv.IsValid():
		return Doc{Para("(nil)")}, nil
	case isList(dv) && kind != fieldKindScalar:
		return d.listBlocks(dv, "", "")
	case isList(dv):
		items := make([]string, dv.Len())
		for i := range items {
			s, err := d.value(dv.Index(i), "[]", "", inlineString)
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
	case kind == fieldKindSection:
		return d.members(dv, "")
	default:
		return Doc{Para(scalarString(dv))}, nil
	}
}

func isList(dv reflect.Value) bool {
	return dv.IsValid() && (dv.Kind() == reflect.Slice || dv.Kind() == reflect.Array)
}

// isTableSlice reports whether dv is a list whose elements are structs with
// no canonical string form: the shape that renders as a table.
func isTableSlice(dv reflect.Value) bool {
	if !isList(dv) {
		return false
	}
	elem := derefType(dv.Type().Elem())
	return elem.Kind() == reflect.Struct && elem != mapType && !typeImplementsStringer(elem)
}

// members derives the members of a struct (its fields) or of a map (its
// entries): scalar lines first, then sections, then tables, each in member
// order. A member's custom view stays in its member's place in that order.
func (d *deriver) members(dv reflect.Value, path string) (Doc, error) {
	if !dv.IsValid() {
		return nil, nil
	}
	var buckets [fieldKindCount]Doc
	add := func(v reflect.Value, name, label string) error {
		doc, kind, err := d.member(v, joinPath(path, name), label)
		buckets[kind] = append(buckets[kind], doc...)
		return err
	}
	var err error
	if isMapLike(dv) {
		err = eachEntry(dv, add)
	} else {
		err = eachField(dv, add)
	}
	if err != nil {
		return nil, err
	}
	var out Doc
	for _, b := range buckets {
		out = append(out, b...)
	}
	return out, nil
}

// eachEntry calls fn for each entry of a map-like dv, labelled by its key
// verbatim: map keys are data, never humanized.
func eachEntry(dv reflect.Value, fn func(v reflect.Value, name, label string) error) error {
	for _, e := range mapEntries(dv) {
		if err := fn(e.val, e.key, e.key); err != nil {
			return err
		}
	}
	return nil
}

// eachField calls fn for each struct field the human views show.
func eachField(dv reflect.Value, fn func(v reflect.Value, name, label string) error) error {
	hints, err := hintsFor(dv.Type())
	if err != nil {
		return err
	}
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
		if err := fn(fv, hf.name, hf.label); err != nil {
			return err
		}
	}
	return nil
}

// member derives one struct field or map entry: a nested struct or map is a
// Section, a list of structs or maps a titled Table or Section, anything else
// a Field line.
func (d *deriver) member(fv reflect.Value, path, label string) (Doc, fieldKind, error) {
	dv := derefValue(fv)
	kind := classifyField(dv)
	base := func() (Doc, error) {
		switch {
		case kind == fieldKindScalar:
			s, err := d.scalarValue(fv, path)
			return Doc{Field{Label: label, Value: s}}, err
		case isList(dv):
			return d.listBlocks(dv, path, label)
		default:
			body, err := d.members(dv, path)
			return Doc{Section{Title: label, Body: body}}, err
		}
	}
	doc, err := d.node(nodeRef{v: fv, path: path, label: label, base: base})
	return labelLonePara(doc, label), kind, err
}

// labelLonePara reads a view's lone Para in a member's place as the member's
// value, so a view that only reformats a value (a duration, a time) keeps the
// label: "Elapsed: 1m30s", not a bare "1m30s".
func labelLonePara(doc Doc, label string) Doc {
	if len(doc) == 1 {
		if p, ok := doc[0].(Para); ok {
			return Doc{Field{Label: label, Value: string(p)}}
		}
	}
	return doc
}

// scalarValue is a scalar member's value text. A list of scalars joins its
// elements, each of which a view may restyle.
func (d *deriver) scalarValue(fv reflect.Value, path string) (string, error) {
	dv := derefValue(fv)
	if !isList(dv) || hasStringForm(dv) {
		return scalarString(fv), nil
	}
	parts := make([]string, dv.Len())
	for i := range parts {
		s, err := d.value(dv.Index(i), path+"[]", "", inlineString)
		if err != nil {
			return "", err
		}
		parts[i] = s
	}
	return strings.Join(parts, ", "), nil
}

// listBlocks derives a list of structs or maps titled title: a Table when
// every element fits one row shape, else one Section "[n]" per element. When
// a view applies to the elements they cannot be rows (a view returns blocks,
// a row is cells), so each element renders through its view instead.
func (d *deriver) listBlocks(dv reflect.Value, path, title string) (Doc, error) {
	elemPath := path + "[]"
	if d.elementViews(dv, elemPath) {
		return d.elementBlocks(dv, elemPath, title)
	}
	if isTableSlice(dv) {
		return d.structTable(dv, elemPath, title)
	}
	if ml, _ := mapListShape(dv); ml.uniform {
		return d.mapTable(dv, elemPath, title, ml.columns)
	}
	return d.elementBlocks(dv, elemPath, title)
}

// elementViews reports whether any view addresses the elements of dv.
func (d *deriver) elementViews(dv reflect.Value, elemPath string) bool {
	if _, ok := d.paths[elemPath]; ok {
		return true
	}
	for i := 0; i < dv.Len(); i++ {
		if ev := derefValue(dv.Index(i)); ev.IsValid() {
			_, c := d.callTypes[ev.Type()]
			_, p := d.printerTypes[ev.Type()]
			if c || p {
				return true
			}
		}
	}
	return false
}

// structTable derives a list of structs as a Table; the columns come from the
// element type, so an empty list still shows its header.
func (d *deriver) structTable(dv reflect.Value, elemPath, title string) (Doc, error) {
	elemType := derefType(dv.Type().Elem())
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
			s, err := d.value(fv, joinPath(elemPath, col.name), col.label, inlineString)
			if err != nil {
				return nil, err
			}
			row[c] = s
		}
		tbl.Rows = append(tbl.Rows, row)
	}
	return Doc{tbl}, nil
}

// mapTable derives a uniform list of maps as a Table with the given columns.
func (d *deriver) mapTable(dv reflect.Value, elemPath, title string, cols []string) (Doc, error) {
	tbl := Table{Title: title, Columns: cols, Rows: [][]string{}}
	for i := 0; i < dv.Len(); i++ {
		vals := map[string]reflect.Value{}
		for _, e := range mapEntries(derefValue(dv.Index(i))) {
			vals[e.key] = e.val
		}
		row := make([]string, len(cols))
		for c, col := range cols {
			s, err := d.value(vals[col], joinPath(elemPath, col), col, inlineString)
			if err != nil {
				return nil, err
			}
			row[c] = s
		}
		tbl.Rows = append(tbl.Rows, row)
	}
	return Doc{tbl}, nil
}

// elementBlocks derives each element of a list as its own node, derived as a
// Section titled "[n]" (1-based).
func (d *deriver) elementBlocks(dv reflect.Value, elemPath, title string) (Doc, error) {
	var seq Doc
	for i := 0; i < dv.Len(); i++ {
		ev := dv.Index(i)
		label := fmt.Sprintf("[%d]", i+1)
		doc, err := d.node(nodeRef{v: ev, path: elemPath, label: label, base: func() (Doc, error) {
			body, err := d.members(derefValue(ev), elemPath)
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
func (d *deriver) value(v reflect.Value, path, label string, base func(reflect.Value) (string, error)) (string, error) {
	ls := d.layers(path, v)
	if len(ls) == 0 {
		return base(v)
	}
	doc, err := d.eval(nodeRef{v: v, path: path, label: label, base: func() (Doc, error) {
		s, err := base(v)
		return Doc{Field{Label: label, Value: s}}, err
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

// inlineString renders v on one line: a scalar as itself, a struct or map as
// "k=v, k=v" in json names and map keys, a list as "a, b"; a composite nested
// inside another is braced ({…} or […]). It never falls back to Go syntax.
func inlineString(v reflect.Value) (string, error) {
	return inlineValue(v, false)
}

func inlineValue(v reflect.Value, nested bool) (string, error) {
	dv := derefValue(v)
	if isScalarValue(dv) {
		return scalarString(dv), nil
	}
	var parts []string
	var err error
	open, closing := "{", "}"
	switch {
	case isMapLike(dv):
		for _, e := range mapEntries(dv) {
			var s string
			if s, err = inlineValue(e.val, true); err != nil {
				return "", err
			}
			parts = append(parts, e.key+"="+s)
		}
	case dv.Kind() == reflect.Struct:
		parts, err = inlineFields(dv)
	default:
		open, closing = "[", "]"
		for i := 0; i < dv.Len(); i++ {
			var s string
			if s, err = inlineValue(dv.Index(i), true); err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
	}
	if err != nil {
		return "", err
	}
	s := strings.Join(parts, ", ")
	if nested {
		s = open + s + closing
	}
	return s, nil
}

func inlineFields(dv reflect.Value) ([]string, error) {
	var parts []string
	err := eachField(dv, func(fv reflect.Value, name, _ string) error {
		s, err := inlineValue(fv, true)
		parts = append(parts, name+"="+s)
		return err
	})
	return parts, err
}
