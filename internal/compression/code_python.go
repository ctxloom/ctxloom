//go:build treesitter

package compression

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractPython handles Python-specific AST extraction: one handler branch per
// top-level node kind.
func (c *CodeCompressor) extractPython(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "import_statement", "import_from_statement":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")

		case "class_definition":
			c.extractPythonClass(child, source, out)

		case "function_definition":
			c.extractPythonFunc(child, source, out)

		case "decorated_definition":
			c.extractPythonDecorated(child, source, out)

		case "expression_statement":
			c.extractPythonExpressionStatement(child, source, out)

		case "assignment", "augmented_assignment":
			// Bare module-level assignment (tree-sitter may surface it directly
			// rather than wrapped in an expression_statement).
			c.extractPythonAssignment(child, source, out)
		}
	}
}

// extractPythonExpressionStatement preserves module-level assignments
// (API_URL = "...", __all__ = [...], type aliases) verbatim — mirroring the
// Go/Rust/JS extractors that keep top-level const/var — and otherwise falls
// back to module-docstring handling. A bare side-effecting expression
// (e.g. a top-level function call) is dropped, as before.
func (c *CodeCompressor) extractPythonExpressionStatement(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		if child.Type() == "assignment" || child.Type() == "augmented_assignment" {
			c.extractPythonAssignment(child, source, out)
			return
		}
	}
	c.extractPythonModuleDocstring(node, source, out)
}

// extractPythonAssignment emits a module-level assignment, eliding a long
// right-hand-side value after the first '=' so the binding name (and any type
// annotation) survives without inflating the compressed output.
func (c *CodeCompressor) extractPythonAssignment(node *sitter.Node, source []byte, out *strings.Builder) {
	text := c.nodeText(node, source)
	if len(text) >= 100 {
		if idx := strings.IndexByte(text, '='); idx > 0 {
			text = strings.TrimRight(text[:idx], " ") + " = ..."
		}
	}
	out.WriteString(text)
	out.WriteString("\n")
}

// extractPythonDecorated emits a decorated function/class, keeping its
// decorator lines and the underlying definition signature.
func (c *CodeCompressor) extractPythonDecorated(node *sitter.Node, source []byte, out *strings.Builder) {
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
			c.extractPythonFunc(dec, source, out)
		case "class_definition":
			c.extractPythonClass(dec, source, out)
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

func (c *CodeCompressor) extractPythonFunc(node *sitter.Node, source []byte, out *strings.Builder) {
	// Build signature: def name(params) -> return_type:
	var sig strings.Builder

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "async":
			sig.WriteString("async ")
		case "def":
			sig.WriteString("def ")
		case "identifier":
			sig.WriteString(c.nodeText(child, source))
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
}

func (c *CodeCompressor) extractPythonClass(node *sitter.Node, source []byte, out *strings.Builder) {
	var sig strings.Builder

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "class":
			sig.WriteString("class ")
		case "identifier":
			sig.WriteString(c.nodeText(child, source))
		case "argument_list":
			sig.WriteString(c.nodeText(child, source))
		case "block":
			sig.WriteString(":\n")
			// Extract method signatures from class body
			c.extractPythonClassBody(child, source, &sig)
		case ":":
			// Skip
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n")
}

func (c *CodeCompressor) extractPythonClassBody(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if child.Type() == "function_definition" {
			out.WriteString("    ")
			c.extractPythonFuncSignatureOnly(child, source, out)
		} else if child.Type() == "decorated_definition" {
			for j := 0; j < int(child.ChildCount()); j++ {
				dec := child.Child(j)
				if dec != nil && dec.Type() == "decorator" {
					out.WriteString("    ")
					out.WriteString(c.nodeText(dec, source))
					out.WriteString("\n")
				} else if dec != nil && dec.Type() == "function_definition" {
					out.WriteString("    ")
					c.extractPythonFuncSignatureOnly(dec, source, out)
				}
			}
		}
	}
}

func (c *CodeCompressor) extractPythonFuncSignatureOnly(node *sitter.Node, source []byte, out *strings.Builder) {
	var sig strings.Builder
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "async":
			sig.WriteString("async ")
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
