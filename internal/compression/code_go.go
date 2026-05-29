//go:build treesitter

package compression

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func (c *CodeCompressor) extractGo(node *sitter.Node, source []byte, out *strings.Builder, preserved, compressed *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "package_clause":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "package clause")

		case "import_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "imports")

		case "type_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "type declaration")

		case "const_declaration", "var_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "const/var declaration")

		case "function_declaration", "method_declaration":
			c.extractGoFunc(child, source, out, preserved)

		case "comment":
			if c.PreserveComments && c.isDocComment(child, source) {
				out.WriteString(c.nodeText(child, source))
				out.WriteString("\n")
			}
		}
	}
}

func (c *CodeCompressor) extractGoFunc(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	// For Go, the simplest approach is to get the full signature from source
	// by finding the block and taking everything before it
	var funcName string
	var blockStart uint32
	hasBlock := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		if child.Type() == "identifier" && funcName == "" {
			funcName = c.nodeText(child, source)
		}
		if child.Type() == "block" {
			blockStart = child.StartByte()
			hasBlock = true
			break
		}
	}

	if hasBlock {
		// Get the signature (everything from func start to block start).
		signature := strings.TrimSpace(string(source[node.StartByte():blockStart]))
		out.WriteString(signature)
		out.WriteString(" { ... }\n\n")
	} else {
		// No block child (e.g. forward declaration) - emit the node verbatim.
		out.WriteString(strings.TrimSpace(c.nodeText(node, source)))
		out.WriteString("\n\n")
	}

	if funcName != "" {
		*preserved = append(*preserved, fmt.Sprintf("func %s", funcName))
	}
}
