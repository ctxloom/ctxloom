package clifmt

import (
	"fmt"
	"io"
	"strings"
)

// Doc is the human view of a value: an ordered list of blocks that the text
// and markdown writers lay out. Derivation produces one, and so does every
// custom view (see At and ViewFor), so a view can return a fresh Doc,
// decorate the derived one, or nest inside a derived parent.
type Doc []Block

// Block is one element of a Doc. The set is sealed: Field, Section, Table,
// List and Para are the only blocks.
type Block interface{ block() }

// Field is one "Label: value" line ("**Label:** value" in markdown).
type Field struct{ Label, Value string }

// Section is a titled, nested Doc: an indented "Title:" heading in text, a
// "#" heading one level deeper in markdown.
type Section struct {
	Title string
	Body  Doc
}

// Table is an aligned table: Columns is the header row, each row of Rows has
// one cell per column. A Title, when set, heads it like a Section's.
type Table struct {
	Title   string
	Columns []string
	Rows    [][]string
}

// List is a run of items: one per line in text, "- item" in markdown. A
// Title, when set, heads it like a Section's.
type List struct {
	Title string
	Items []string
}

// Para is verbatim text, written as-is (one or more lines), e.g. "(none)".
type Para string

func (Field) block()   {}
func (Section) block() {}
func (Table) block()   {}
func (List) block()    {}
func (Para) block()    {}

// docFormat supplies the per-format pieces of writing a Doc, and how the
// depth marker (a heading level for markdown, an indent string for text)
// advances into a child. writeDoc drives the traversal once, generic over the
// depth type D, so neither format bends to the other's shape.
type docFormat[D any] struct {
	field    func(w io.Writer, label, value string, depth D) error
	heading  func(w io.Writer, title string, depth D) error
	table    func(w io.Writer, tbl Table) error
	listItem func(w io.Writer, item string, depth D) error
	para     func(w io.Writer, text string, depth D) error
	child    func(depth D) D
}

// isLine reports whether b is a one-line block (Field, Para). Consecutive
// line blocks sit together; any other neighbour is separated by a blank
// line, so a heading, table or list never runs into its siblings (in GFM a
// line right after a table would even be read as another row).
func isLine(b Block) bool {
	switch b.(type) {
	case Field, Para:
		return true
	}
	return false
}

// writeDoc writes doc at depth.
func writeDoc[D any](w io.Writer, doc Doc, depth D, f docFormat[D]) error {
	for i, b := range doc {
		if i > 0 && (!isLine(doc[i-1]) || !isLine(b)) {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		if err := writeBlock(w, b, depth, f); err != nil {
			return err
		}
	}
	return nil
}

func writeBlock[D any](w io.Writer, b Block, depth D, f docFormat[D]) error {
	switch b := b.(type) {
	case Field:
		return f.field(w, b.Label, b.Value, depth)
	case Para:
		return f.para(w, string(b), depth)
	case Section:
		if err := f.heading(w, b.Title, depth); err != nil {
			return err
		}
		return writeDoc(w, b.Body, f.child(depth), f)
	case Table:
		if b.Title != "" {
			if err := f.heading(w, b.Title, depth); err != nil {
				return err
			}
		}
		return f.table(w, b)
	case List:
		return writeList(w, b, depth, f)
	default:
		return fmt.Errorf("clifmt: unknown block %T", b)
	}
}

func writeList[D any](w io.Writer, l List, depth D, f docFormat[D]) error {
	if l.Title != "" {
		if err := f.heading(w, l.Title, depth); err != nil {
			return err
		}
		depth = f.child(depth)
	}
	for _, item := range l.Items {
		if err := f.listItem(w, item, depth); err != nil {
			return err
		}
	}
	return nil
}

// eachLine calls fn for each line of s ("" counts as one empty line).
func eachLine(s string, fn func(line string) error) error {
	for _, line := range strings.Split(s, "\n") {
		if err := fn(line); err != nil {
			return err
		}
	}
	return nil
}
