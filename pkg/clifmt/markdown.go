package clifmt

import (
	"fmt"
	"io"
	"strings"
)

// writeMarkdownDoc writes doc as GFM markdown. Top-level headings are "##".
func writeMarkdownDoc(w io.Writer, doc Doc) error {
	return writeDoc(w, doc, 2, markdownDocFormat)
}

// markdownDocFormat is the markdown instantiation of writeDoc's traversal
// (see doc.go): depth is a heading level (capped at 6, markdown's max),
// fields render as bold "**Label:** value" lines, list items as "- item",
// and tables as GFM pipe tables.
var markdownDocFormat = docFormat[int]{
	field: func(w io.Writer, label, value string, _ int) error {
		_, err := fmt.Fprintf(w, "**%s:** %s\n", label, value)
		return err
	},
	heading: writeHeading,
	table:   writeMarkdownTable,
	listItem: func(w io.Writer, item string, _ int) error {
		_, err := fmt.Fprintf(w, "- %s\n", item)
		return err
	},
	para: func(w io.Writer, text string, _ int) error {
		_, err := fmt.Fprintln(w, text)
		return err
	},
	child: nextLevel,
}

// writeMarkdownTable renders a Table as a GFM pipe table. Header and cell
// values alike are escaped so an embedded "|" can't corrupt the table
// structure: a header carrying an unescaped pipe declares more columns than
// the separator row below it, and GFM then stops treating the block as a
// table at all.
func writeMarkdownTable(w io.Writer, tbl Table) error {
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
