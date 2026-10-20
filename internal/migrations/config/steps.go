// Package configmig holds the ctxloom config's schemaver chain.
package configmig

import (
	"github.com/ctxloom/ctxloom/internal/migrations/config/internal/v8"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

// Steps is the config's chain, oldest first.
func Steps() []schemaver.Step {
	return []schemaver.Step{v8.Step{}}
}
