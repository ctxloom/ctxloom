package mockengine

import "io"

// renderCodexOneshot is codex's per-personality oneshot WIRE adapter. It writes
// the response as PLAIN assistant text, with NO envelope of any kind.
//
// This mirrors codex's REAL driver, not a guess. codex's oneshot Execute
// (internal/codex/backend.go:404) delegates straight to LaunchBackend.ExecuteCLI
// with a nil result parser, and for a oneshot run ExecuteCLI calls
// RunNonInteractive (internal/shared/agent/launch_backend.go:141), which streams
// the child's stdout directly to the caller's writer and decodes NOTHING. There
// is no `codex exec --output-format`/`parseCodexJSONResult` equivalent — unlike
// claude, whose SkipSetup path buffers stdout and runs parseClaudeJSONResult
// over a {result,modelUsage} envelope (internal/claude/claudecode.go). So
// whatever codex prints on stdout IS the assistant reply; if this adapter
// emitted claude's JSON envelope, codex's driver would hand that raw JSON back
// verbatim as the model's answer.
//
// Because the contract is "stdout is the reply", this adapter reads nothing off
// the argv (codex declares no wire-shaping flag) and needs no prompt length —
// its whole job is to put out.Response on stdout unchanged. The shared,
// L1-driven prompt extraction and discovery report live in the runtime; this is
// only the wire.
func renderCodexOneshot(w io.Writer, out Outcome) error {
	_, err := io.WriteString(w, out.Response)
	return err
}
