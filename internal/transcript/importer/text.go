package importer

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

// JoinNonEmptyFunc extracts one string from each element of items via get,
// then joins the non-empty results via JoinNonEmpty — the "flatten a slice
// of vendor-typed content blocks/results into one joined text" step codex's
// content blocks and kiro's tool-result content elements both need for
// their own (different) element type.
func JoinNonEmptyFunc[T any](items []T, get func(T) string) string {
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = get(item)
	}
	return JoinNonEmpty(parts)
}
