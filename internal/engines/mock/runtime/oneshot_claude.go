package runtime

import (
	"io"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// renderClaudeOneshot is claude's per-personality oneshot WIRE adapter. It
// renders the runtime's Outcome onto claude's oneshot format exactly as the
// real claude -p produces: the result as plain text on stdout. It is one of
// the per-personality oneshot wire adapters; the DISCOVERY walk and the
// prompt extraction are shared and L1-driven, only this wire rendering is
// claude-specific. See oneshot.go for the engine dispatch.
func renderClaudeOneshot(w io.Writer, _ agent.ParsedArgv, _ int, out Outcome) error {
	_, err := io.WriteString(w, out.Response)
	return err
}
