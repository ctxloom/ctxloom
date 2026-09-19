//go:build treesitter

package compression

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// goVerbatim lists Go top-level node kinds kept verbatim (text + suffix).
var goVerbatim = map[string]string{
	"package_clause":     "\n\n",
	"import_declaration": "\n",
	"type_declaration":   "\n\n",
	"const_declaration":  "\n",
	"var_declaration":    "\n",
}

// extractGo handles Go-specific AST extraction: one verbatim/handler branch
// per top-level node kind.
func (c *CodeCompressor) extractGo(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if suffix, ok := goVerbatim[child.Type()]; ok {
			c.emitVerbatim(child, source, out, suffix)
			continue
		}

		switch child.Type() {
		case "function_declaration", "method_declaration":
			c.extractGoFunc(child, source, out)
		case "comment":
			c.emitGoComment(child, source, out)
		}
	}
}

// emitGoComment writes a doc comment when comment preservation is enabled.
func (c *CodeCompressor) emitGoComment(child *sitter.Node, source []byte, out *strings.Builder) {
	if c.PreserveComments && c.isDocComment(child, source) {
		out.WriteString(c.nodeText(child, source))
		out.WriteString("\n")
	}
}

func (c *CodeCompressor) extractGoFunc(node *sitter.Node, source []byte, out *strings.Builder) {
	// For Go, the simplest approach is to get the full signature from source
	// by finding the block and taking everything before it
	var blockStart uint32
	hasBlock := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
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
}
