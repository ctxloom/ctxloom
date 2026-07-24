package mockengine

import (
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The oneshot surface is SHARED across personalities, but its stdout contract is
// NOT — and it is not derivable from L1. L1 declares which flags a surface
// accepts and how the prompt is delivered; it does NOT declare "value X on flag
// Y ⇒ emit envelope Z". That wire convention lives with the engine's real driver
// (internal/<engine>/*.go), so each personality carries its OWN oneshot wire
// adapter here:
//
//   - claude (renderClaudeOneshot): plain text, or a {result,modelUsage} JSON
//     envelope under --output-format json, which claude's driver decodes with
//     parseClaudeJSONResult.
//   - codex (renderCodexOneshot): plain assistant text with NO envelope — codex's
//     driver (backend.go Execute → RunNonInteractive) streams the child's stdout
//     straight through and parses nothing.
//
// The two conventions are genuinely different, so they are two clear adapters
// rather than one adapter with flags. The prompt EXTRACTION (Runtime.readPrompt,
// off L1's PromptDelivery) and the discovery WALK stay fully shared and
// L1-driven; only this wire rendering is per-engine.
const (
	engineClaudeCode = "claude-code"
	engineCodex      = "codex"
)

// renderOneshotWire dispatches to the named engine's oneshot wire adapter. An
// engine with no registered oneshot adapter is a LOUD error rather than a silent
// fall-through to claude's shape — a fake that rendered the wrong engine's wire
// would fail the driver on a run it believed succeeded, exactly the silent-no-op
// failure mode this whole probe exists to surface.
func renderOneshotWire(engine string, w io.Writer, argv agent.ParsedArgv, promptLen int, out Outcome) error {
	switch engine {
	case engineClaudeCode:
		return renderClaudeOneshot(w, argv, promptLen, out)
	case engineCodex:
		return renderCodexOneshot(w, out)
	default:
		return fmt.Errorf("mock-engine: no oneshot wire adapter for engine %q", engine)
	}
}
