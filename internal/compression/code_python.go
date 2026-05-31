//go:build treesitter

package compression

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractPython handles Python-specific AST extraction: one handler branch per
// top-level node kind.
func (c *CodeCompressor) extractPython(node *sitter.Node, source []byte, out *strings.Builder, preserved, compressed *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "import_statement", "import_from_statement":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "import")

		case "class_definition":
			c.extractPythonClass(child, source, out, preserved)

		case "function_definition":
			c.extractPythonFunc(child, source, out, preserved)

		case "decorated_definition":
			c.extractPythonDecorated(child, source, out, preserved)

		case "expression_statement":
			c.extractPythonModuleDocstring(child, source, out)
		}
	}
}

// extractPythonDecorated emits a decorated function/class, keeping its
// decorator lines and the underlying definition signature.
func (c *CodeCompressor) extractPythonDecorated(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	for j := 0; j < int(node.ChildCount()); j++ {
		dec := node.Child(j)
		if dec == nil {
			continue
		}
		switch dec.Type() {
		case "decorator":
			out.WriteString(c.nodeText(dec, source))
			out.WriteString("\n")
		case "function_definition":
			c.extractPythonFunc(dec, source, out, preserved)
		case "class_definition":
			c.extractPythonClass(dec, source, out, preserved)
		}
	}
}

// extractPythonModuleDocstring emits a module-level docstring when comment
// preservation is enabled.
func (c *CodeCompressor) extractPythonModuleDocstring(node *sitter.Node, source []byte, out *strings.Builder) {
	if !c.PreserveComments {
		return
	}
	text := c.nodeText(node, source)
	if strings.HasPrefix(text, `"""`) || strings.HasPrefix(text, `'''`) {
		out.WriteString(text)
		out.WriteString("\n")
	}
}

func (c *CodeCompressor) extractPythonFunc(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	// Build signature: def name(params) -> return_type:
	var sig strings.Builder
	var funcName string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "def":
			sig.WriteString("def ")
		case "identifier":
			funcName = c.nodeText(child, source)
			sig.WriteString(funcName)
		case "parameters":
			sig.WriteString(c.nodeText(child, source))
		case "type":
			sig.WriteString(" -> ")
			sig.WriteString(c.nodeText(child, source))
		case "block":
			sig.WriteString(":\n    ...")
		case ":":
			// Skip, handled with block
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, fmt.Sprintf("def %s", funcName))
}

func (c *CodeCompressor) extractPythonClass(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	var className string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "class":
			sig.WriteString("class ")
		case "identifier":
			className = c.nodeText(child, source)
			sig.WriteString(className)
		case "argument_list":
			sig.WriteString(c.nodeText(child, source))
		case "block":
			sig.WriteString(":\n")
			// Extract method signatures from class body
			c.extractPythonClassBody(child, source, &sig, preserved)
		case ":":
			// Skip
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n")
	*preserved = append(*preserved, fmt.Sprintf("class %s", className))
}

func (c *CodeCompressor) extractPythonClassBody(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if child.Type() == "function_definition" {
			out.WriteString("    ")
			c.extractPythonFuncSignatureOnly(child, source, out, preserved)
		} else if child.Type() == "decorated_definition" {
			for j := 0; j < int(child.ChildCount()); j++ {
				dec := child.Child(j)
				if dec != nil && dec.Type() == "decorator" {
					out.WriteString("    ")
					out.WriteString(c.nodeText(dec, source))
					out.WriteString("\n")
				} else if dec != nil && dec.Type() == "function_definition" {
					out.WriteString("    ")
					c.extractPythonFuncSignatureOnly(dec, source, out, preserved)
				}
			}
		}
	}
}

func (c *CodeCompressor) extractPythonFuncSignatureOnly(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "def":
			sig.WriteString("def ")
		case "identifier":
			sig.WriteString(c.nodeText(child, source))
		case "parameters":
			sig.WriteString(c.nodeText(child, source))
		case "type":
			sig.WriteString(" -> ")
			sig.WriteString(c.nodeText(child, source))
		case "block", ":":
			// Stop at body
		}
	}
	sig.WriteString(": ...")
	out.WriteString(sig.String())
	out.WriteString("\n")
}
