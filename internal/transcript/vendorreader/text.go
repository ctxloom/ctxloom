package vendorreader

import "strings"

// JoinNonEmpty joins the non-empty elements of parts with a blank line
// between each — the "several content blocks that together form one visible
// turn" convention every engine's message-mapping repeats: codex's
// response_item content blocks and reasoning.summary elements, claude's
// consecutive text blocks and a tool_result's own nested text blocks,
// kiro's tool_use_results content elements. A blank line reads naturally as
// a paragraph break for that case; an empty element (a block that carries no
// text on a given vendor build) contributes nothing rather than a stray gap.
// Callers pass every candidate part, filtered or not — filtering here once
// means no caller needs its own "skip if empty" loop before joining.
func JoinNonEmpty(parts []string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}
