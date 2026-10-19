// Package textblocks joins text blocks into one paragraph-separated text.
package textblocks

import "strings"

// Join joins the non-empty parts with a blank line, so a run of text blocks
// reads as paragraphs and an empty block contributes nothing rather than a
// stray gap. Filtering here once means no caller needs its own "skip if
// empty" loop before joining.
func Join(parts ...string) string { return JoinWith("\n\n", parts...) }

// JoinWith joins the non-empty parts with sep: Join for any separator.
func JoinWith(sep string, parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, sep)
}
