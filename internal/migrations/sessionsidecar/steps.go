// Package sessionsidecarmig holds the session sidecar's schemaver chain.
package sessionsidecarmig

import (
	"github.com/ctxloom/ctxloom/internal/migrations/sessionsidecar/internal/v2"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

// Steps is the session sidecar's chain, oldest first.
func Steps() []schemaver.Step {
	return []schemaver.Step{v2.Step{}}
}
