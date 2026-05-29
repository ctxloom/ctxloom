//go:build treesitter

package compression

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractRust handles Rust AST extraction.
func (c *CodeCompressor) extractRust(node *sitter.Node, source []byte, out *strings.Builder, preserved, compressed *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "use_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "use")

		case "mod_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "mod")

		case "struct_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "struct")

		case "enum_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "enum")

		case "trait_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "trait")

		case "impl_item":
			c.extractRustImpl(child, source, out, preserved)

		case "function_item":
			c.extractRustFunc(child, source, out, preserved)

		case "type_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "type alias")

		case "const_item", "static_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "const/static")
		}
	}
}

func (c *CodeCompressor) extractRustFunc(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	var funcName string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "visibility_modifier", "function_modifiers":
			sig.WriteString(c.nodeText(child, source))
			sig.WriteString(" ")
		case "fn":
			sig.WriteString("fn ")
		case "identifier":
			funcName = c.nodeText(child, source)
			sig.WriteString(funcName)
		case "type_parameters":
			sig.WriteString(c.nodeText(child, source))
		case "parameters":
			sig.WriteString(c.nodeText(child, source))
		case "return_type":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "where_clause":
			sig.WriteString("\n    ")
			sig.WriteString(c.nodeText(child, source))
		case "block":
			sig.WriteString(" { ... }")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, fmt.Sprintf("fn %s", funcName))
}

func (c *CodeCompressor) extractRustImpl(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	sig.WriteString("impl")

	// Extract impl header
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "type_parameters":
			sig.WriteString(c.nodeText(child, source))
		case "type_identifier", "generic_type":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "for":
			sig.WriteString(" for")
		case "declaration_list":
			sig.WriteString(" {\n")
			c.extractRustImplBody(child, source, &sig, preserved)
			sig.WriteString("}")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, "impl block")
}

func (c *CodeCompressor) extractRustImplBody(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if child.Type() == "function_item" {
			out.WriteString("    ")
			c.extractRustFuncSigOnly(child, source, out)
			out.WriteString("\n")
		}
	}
}

func (c *CodeCompressor) extractRustFuncSigOnly(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "visibility_modifier":
			out.WriteString(c.nodeText(child, source))
			out.WriteString(" ")
		case "fn":
			out.WriteString("fn ")
		case "identifier":
			out.WriteString(c.nodeText(child, source))
		case "type_parameters":
			out.WriteString(c.nodeText(child, source))
		case "parameters":
			out.WriteString(c.nodeText(child, source))
		case "return_type":
			out.WriteString(" ")
			out.WriteString(c.nodeText(child, source))
		case "block":
			out.WriteString(" { ... }")
			return
		}
	}
}
