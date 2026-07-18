package clifmt

import (
	"fmt"
	"io"
	"reflect"
	"strings"
)

// renderMarkdown mirrors renderText's classification of the top-level value
// but emits GFM markdown: bold "**Label:** value" lines, "##"-and-deeper
// headings for sections/tables, and pipe tables for slices of struct.
func renderMarkdown(w io.Writer, v any) error {
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
		return renderNode(w, node, 2, markdownNodeFormat)
	case reflect.Slice, reflect.Array:
		elemType := derefType(rv.Type().Elem())
		if elemType.Kind() == reflect.Struct && !typeImplementsStringer(elemType) {
			tbl, err := buildTable(rv)
			if err != nil {
				return err
			}
			return writeMarkdownTable(w, tbl)
		}
		for i := 0; i < rv.Len(); i++ {
			if _, err := fmt.Fprintf(w, "- %s\n", scalarString(rv.Index(i))); err != nil {
				return err
			}
		}
		return nil
	default:
		_, err := fmt.Fprintln(w, scalarString(rv))
		return err
	}
}

// markdownNodeFormat is the markdown instantiation of renderNode's shared
// traversal (see noderender.go): depth is a heading level (capped at 6,
// markdown's max), scalars render as bold "**Label:** value" lines, and
// both sections and tables get a "#"-heading at the current level.
var markdownNodeFormat = nodeFormat[int]{
	writeScalar: func(w io.Writer, label, value string, _ int) error {
		_, err := fmt.Fprintf(w, "**%s:** %s\n", label, value)
		return err
	},
	writeSectionHeading: writeHeading,
	writeTableHeading:   writeHeading,
	writeTable:          writeMarkdownTable,
	childDepth:          nextLevel,
}

func writeHeading(w io.Writer, label string, level int) error {
	_, err := fmt.Fprintf(w, "%s %s\n\n", strings.Repeat("#", level), label)
	return err
}

func nextLevel(level int) int {
	if level >= 6 {
		return 6
	}
	return level + 1
}

// writeMarkdownTable renders a Table as a GFM pipe table. Cell values are
// escaped so an embedded "|" can't corrupt the table structure.
func writeMarkdownTable(w io.Writer, tbl *Table) error {
	if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(tbl.Columns, " | ")); err != nil {
		return err
	}
	seps := make([]string, len(tbl.Columns))
	for i := range seps {
		seps[i] = "---"
	}
	if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(seps, " | ")); err != nil {
		return err
	}
	for _, row := range tbl.Rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = mdEscapeCell(c)
		}
		if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(cells, " | ")); err != nil {
			return err
		}
	}
	return nil
}

func mdEscapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", "<br>")
	return s
}
