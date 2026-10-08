package clifmt

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// writeTextDoc writes doc as plain text.
func writeTextDoc(w io.Writer, doc Doc) error {
	return writeDoc(w, doc, "", textDocFormat)
}

// textDocFormat is the plain-text instantiation of writeDoc's traversal (see
// doc.go): depth is an indent string that grows by two spaces per nesting
// level, fields render as "Label: value" lines, headings as "Title:" lines at
// the current indent, and tables are aligned columns.
var textDocFormat = docFormat[string]{
	field: func(w io.Writer, label, value string, indent string) error {
		_, err := fmt.Fprintf(w, "%s%s: %s\n", indent, label, value)
		return err
	},
	heading: func(w io.Writer, title string, indent string) error {
		_, err := fmt.Fprintf(w, "%s%s:\n", indent, title)
		return err
	},
	table:    writeTextTable,
	listItem: writeIndentedLine,
	para: func(w io.Writer, text string, indent string) error {
		return eachLine(text, func(line string) error { return writeIndentedLine(w, line, indent) })
	},
	child: func(indent string) string { return indent + "  " },
}

func writeIndentedLine(w io.Writer, line, indent string) error {
	_, err := fmt.Fprintf(w, "%s%s\n", indent, line)
	return err
}

// writeTextTable renders a Table as shell-style aligned columns (uppercase
// headers, two-space minimum gutter), via text/tabwriter so column widths
// never need manual computation.
func writeTextTable(w io.Writer, tbl Table) error {
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
