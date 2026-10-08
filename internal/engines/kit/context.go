package kit

import (
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// AppendedSection delivers text as a section after the file's own content:
// the well-known context file at a root (the user's text in it stays
// theirs, and the ownership record owns what is appended and restores the
// prior bytes on removal). p is the file's presentation, announced or not as
// the engine reads it.
func AppendedSection(p present.Presentation, text []byte) present.Delivered {
	return present.Delivered{Presented: p,
		Claims: map[string][]present.Claim{p.HostPath: {{Pointer: present.AppendedSection, Value: slices.Clone(text)}}}}
}
