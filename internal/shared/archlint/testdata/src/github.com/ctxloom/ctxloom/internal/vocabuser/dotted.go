package vocabuser

import . "github.com/ctxloom/ctxloom/internal/vocabowner"

// Dotted reaches the vocabulary through a dot import.
func Dotted(s string) Mode {
	return Mode(s) // want `Dotted converts a runtime string into the closed vocabulary internal/vocabowner.Mode`
}
