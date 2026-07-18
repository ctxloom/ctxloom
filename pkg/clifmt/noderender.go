package clifmt

import "io"

// nodeFormat supplies the per-format pieces of rendering a Node: how a
// scalar line, a section heading, and a table heading look, plus how the
// depth marker (a heading level for markdown, an indent string for text)
// advances into a child section. renderNode drives the traversal — walk
// order, the blank-line-between-blocks rule, and recursion — once, generic
// over the depth type D so markdown's int level and text's string indent
// can share it without either format bending to the other's shape.
type nodeFormat[D any] struct {
	writeScalar         func(w io.Writer, label, value string, depth D) error
	writeSectionHeading func(w io.Writer, label string, depth D) error
	writeTableHeading   func(w io.Writer, label string, depth D) error
	writeTable          func(w io.Writer, tbl *Table) error
	childDepth          func(depth D) D
}

// renderNode writes node's scalar lines, then its sections and tables under
// headings, recursing into sections at the next depth. A blank line
// separates each block once something has already been written for this
// node — the shared rule both renderNodeMarkdown and renderNodeText relied
// on before they were unified here.
func renderNode[D any](w io.Writer, node *Node, depth D, f nodeFormat[D]) error {
	wrote := false
	for _, s := range node.Scalars {
		if err := f.writeScalar(w, s.Label, s.Value, depth); err != nil {
			return err
		}
		wrote = true
	}
	for _, sec := range node.Sections {
		if wrote {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		if err := f.writeSectionHeading(w, sec.Label, depth); err != nil {
			return err
		}
		if err := renderNode(w, sec.Node, f.childDepth(depth), f); err != nil {
			return err
		}
		wrote = true
	}
	for _, tf := range node.Tables {
		if wrote {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		if err := f.writeTableHeading(w, tf.Label, depth); err != nil {
			return err
		}
		if err := f.writeTable(w, tf.Table); err != nil {
			return err
		}
		wrote = true
	}
	return nil
}
