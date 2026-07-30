//go:build treesitter

package compression

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractJava handles Java AST extraction.
func (c *CodeCompressor) extractJava(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "package_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")

		case "import_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")

		case "class_declaration":
			c.extractJavaClass(child, source, out)

		case "interface_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")

		case "enum_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")

		case "program":
			// Handle nested program node
			c.extractJava(child, source, out)
		}
	}
}

func (c *CodeCompressor) extractJavaClass(node *sitter.Node, source []byte, out *strings.Builder) {
	var sig strings.Builder

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "modifiers":
			sig.WriteString(c.nodeText(child, source))
			sig.WriteString(" ")
		case "class":
			sig.WriteString("class ")
		case "identifier":
			sig.WriteString(c.nodeText(child, source))
		case "type_parameters":
			sig.WriteString(c.nodeText(child, source))
		case "superclass":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "super_interfaces":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "class_body":
			sig.WriteString(" {\n")
			c.extractJavaClassBody(child, source, &sig)
			sig.WriteString("}")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
}

func (c *CodeCompressor) extractJavaClassBody(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "field_declaration":
			out.WriteString("    ")
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
		case "method_declaration":
			out.WriteString("    ")
			c.writeJavaMemberSig(child, source, out, "block")
			out.WriteString("\n")
		case "constructor_declaration":
			out.WriteString("    ")
			c.writeJavaMemberSig(child, source, out, "constructor_body")
			out.WriteString("\n")
		}
	}
}

// writeJavaMemberSig writes one class member's declaration verbatim up to the
// point its body opens, then elides the body. bodyKind names the node kind that
// opens it — `block` for a method, `constructor_body` for a constructor — which
// is the ONLY thing that differs between the two; a member with no body at all
// (abstract, or an interface method) is emitted whole.
//
// Taking the signature as a source SLICE rather than re-assembling it from child
// nodes is deliberate: it keeps annotations, modifiers, generics, and a `throws`
// clause exactly as written, with no node kind left to forget.
func (c *CodeCompressor) writeJavaMemberSig(node *sitter.Node, source []byte, out *strings.Builder, bodyKind string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && child.Type() == bodyKind {
			out.WriteString(strings.TrimSpace(string(source[node.StartByte():child.StartByte()])))
			out.WriteString(" { ... }")
			return
		}
	}
	out.WriteString(strings.TrimSpace(c.nodeText(node, source)))
}
