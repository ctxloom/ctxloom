// Package lockfilemig holds the lockfile's schemaver chain.
package lockfilemig

import (
	"github.com/ctxloom/ctxloom/internal/migrations/lockfile/internal/v3"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

// Steps is the lockfile's chain, oldest first.
func Steps() []schemaver.Step {
	return []schemaver.Step{v3.Step{}}
}
