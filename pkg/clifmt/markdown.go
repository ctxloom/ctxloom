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
		if node.Empty() {
			// U154-F02: an all-omitempty struct used to render zero bytes
			// here — indistinguishable from "the command produced no output
			// at all" (json/yaml render the same value as `{}`/`null`).
			_, err := fmt.Fprintln(w, "(none)")
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
		if rv.Len() == 0 {
			// U154-F02: an empty SCALAR slice has no columns to derive a
			// header row from (unlike the struct-slice branch above, which
			// stays self-evidencing via buildTable's header even with zero
			// rows) — it used to render zero bytes here.
			_, err := fmt.Fprintln(w, "(none)")
			return err
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

// writeMarkdownTable renders a Table as a GFM pipe table. Header and cell
// values alike are escaped so an embedded "|" can't corrupt the table
// structure: a header carrying an unescaped pipe declares more columns than
// the separator row below it, and GFM then stops treating the block as a
// table at all.
func writeMarkdownTable(w io.Writer, tbl *Table) error {
	headers := make([]string, len(tbl.Columns))
	for i, c := range tbl.Columns {
		headers[i] = mdEscapeCell(c)
	}
	if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(headers, " | ")); err != nil {
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
