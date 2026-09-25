// Package vocabuser converts into vocabowner.Mode every way a consumer can
// spell it.
package vocabuser

import (
	vo "github.com/ctxloom/ctxloom/internal/vocabowner"

	"github.com/ctxloom/ctxloom/internal/vocabalias"
)

// FromFlag reaches the vocabulary through a renamed import.
func FromFlag(s string) vo.Mode {
	return vo.Mode(s) // want `FromFlag converts a runtime string into the closed vocabulary internal/vocabowner.Mode`
}

// ViaAlias reaches it through a re-exporting alias.
func ViaAlias(s string) vocabalias.Mode {
	return vocabalias.Mode(s) // want `ViaAlias converts a runtime string into the closed vocabulary internal/vocabowner.Mode`
}

// Member converts a declared member, which is in the set by inspection.
func Member() vo.Mode { return vo.Mode("fast") }

// Stray converts a literal that is not a member.
func Stray() vo.Mode {
	return vo.Mode("medium") // want `Stray converts the literal "medium", which is not one of its members,`
}
