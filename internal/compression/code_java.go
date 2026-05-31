//go:build treesitter

package compression

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractJava handles Java AST extraction.
func (c *CodeCompressor) extractJava(node *sitter.Node, source []byte, out *strings.Builder, preserved, compressed *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "package_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "package")

		case "import_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "import")

		case "class_declaration":
			c.extractJavaClass(child, source, out, preserved)

		case "interface_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "interface")

		case "enum_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "enum")

		case "program":
			// Handle nested program node
			c.extractJava(child, source, out, preserved, compressed)
		}
	}
}

func (c *CodeCompressor) extractJavaClass(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	var className string

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
			className = c.nodeText(child, source)
			sig.WriteString(className)
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
			c.extractJavaClassBody(child, source, &sig, preserved)
			sig.WriteString("}")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, fmt.Sprintf("class %s", className))
}

func (c *CodeCompressor) extractJavaClassBody(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
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
			c.extractJavaMethodSig(child, source, out)
			out.WriteString("\n")
		case "constructor_declaration":
			out.WriteString("    ")
			c.extractJavaConstructorSig(child, source, out)
			out.WriteString("\n")
		}
	}
}

func (c *CodeCompressor) extractJavaMethodSig(node *sitter.Node, source []byte, out *strings.Builder) {
	// Find the block and extract everything before it from source
	var blockStart uint32
	hasBlock := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && child.Type() == "block" {
			blockStart = child.StartByte()
			hasBlock = true
			break
		}
	}

	if hasBlock {
		sig := strings.TrimSpace(string(source[node.StartByte():blockStart]))
		out.WriteString(sig)
		out.WriteString(" { ... }")
	} else {
		// Abstract method or interface method - no body
		out.WriteString(strings.TrimSpace(c.nodeText(node, source)))
	}
}

func (c *CodeCompressor) extractJavaConstructorSig(node *sitter.Node, source []byte, out *strings.Builder) {
	// Find the constructor_body and extract everything before it from source
	var bodyStart uint32
	hasBody := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && child.Type() == "constructor_body" {
			bodyStart = child.StartByte()
			hasBody = true
			break
		}
	}

	if hasBody {
		sig := strings.TrimSpace(string(source[node.StartByte():bodyStart]))
		out.WriteString(sig)
		out.WriteString(" { ... }")
	} else {
		out.WriteString(strings.TrimSpace(c.nodeText(node, source)))
	}
}
