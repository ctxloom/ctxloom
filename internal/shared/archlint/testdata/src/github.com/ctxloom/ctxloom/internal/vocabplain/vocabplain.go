// Package vocabplain converts into vocabowner.Mode through its owner's own
// import name.
package vocabplain

import "github.com/ctxloom/ctxloom/internal/vocabowner"

// FromFlag asserts a runtime string into the vocabulary.
func FromFlag(s string) vocabowner.Mode {
	return vocabowner.Mode(s) // want `FromFlag converts a runtime string into the closed vocabulary internal/vocabowner.Mode`
}
