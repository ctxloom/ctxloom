package clifmt

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"text/tabwriter"
)

// renderText is the human plain-text renderer. It classifies the top-level
// value the same way buildNode/buildTable do for struct fields: a struct
// becomes "Label: value" lines with nested sections/tables, a slice of
// struct becomes a bare aligned table, a slice of scalars becomes one value
// per line, and anything else prints its scalar string.
func renderText(w io.Writer, v any) error {
	rv := derefValue(reflect.ValueOf(v))
	if !rv.IsValid() {
		_, err := fmt.Fprintln(w, "(nil)")
		return err
	}

	switch rv.Kind() {
	case reflect.Struct:
		if implementsStringer(rv) {
			_, err := fmt.Fprintln(w, scalarString(rv))
			return err
		}
		node, err := buildNode(rv)
		if err != nil {
			return err
		}
		if node.Empty() {
			// U154-F02: an all-omitempty struct used to render zero bytes
			// here — indistinguishable from "the command produced no output
			// at all" (json/yaml render the same value as `{}`/`null`).
			_, err := fmt.Fprintln(w, "(none)")
			return err
		}
		return renderNode(w, node, "", textNodeFormat)
	case reflect.Slice, reflect.Array:
		elemType := derefType(rv.Type().Elem())
		if elemType.Kind() == reflect.Struct && !typeImplementsStringer(elemType) {
			tbl, err := buildTable(rv)
			if err != nil {
				return err
			}
			return writeTextTable(w, tbl)
		}
		if rv.Len() == 0 {
			// U154-F02: an empty SCALAR slice has no columns to derive a
			// header row from (unlike the struct-slice branch above, which
			// stays self-evidencing via buildTable's header even with zero
			// rows) — it used to render zero bytes here.
			_, err := fmt.Fprintln(w, "(none)")
			return err
		}
		for i := 0; i < rv.Len(); i++ {
			if _, err := fmt.Fprintln(w, scalarString(rv.Index(i))); err != nil {
				return err
			}
		}
		return nil
	default:
		_, err := fmt.Fprintln(w, scalarString(rv))
		return err
	}
}

// textNodeFormat is the plain-text instantiation of renderNode's shared
// traversal (see noderender.go): depth is an indent string that grows by
// two spaces per nesting level, scalars render as "Label: value" lines, and
// both sections and tables get an unindented-relative-to-body "Label:"
// heading line at the current indent.
var textNodeFormat = nodeFormat[string]{
	writeScalar: func(w io.Writer, label, value string, indent string) error {
		_, err := fmt.Fprintf(w, "%s%s: %s\n", indent, label, value)
		return err
	},
	writeSectionHeading: writeTextHeading,
	writeTableHeading:   writeTextHeading,
	writeTable:          writeTextTable,
	childDepth:          func(indent string) string { return indent + "  " },
}

func writeTextHeading(w io.Writer, label string, indent string) error {
	_, err := fmt.Fprintf(w, "%s%s:\n", indent, label)
	return err
}

// writeTextTable renders a Table as shell-style aligned columns (uppercase
// headers, two-space minimum gutter), via text/tabwriter so column widths
// never need manual computation.
func writeTextTable(w io.Writer, tbl *Table) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	headers := make([]string, len(tbl.Columns))
	for i, c := range tbl.Columns {
		headers[i] = strings.ToUpper(c)
	}
	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range tbl.Rows {
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}
