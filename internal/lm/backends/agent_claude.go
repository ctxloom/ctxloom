package backends

import "github.com/ctxloom/claude"

// The claude launch backend (ClaudeCode + capabilities) lives in its own module
// github.com/ctxloom/claude alongside its settings writer. These aliases keep the
// wiring layer (registry config decoder) and external callers (cmd/llm_resolve)
// referencing the typed config unchanged. The backend itself is constructed via
// claude.NewClaudeCode in the registry.
type ClaudeConfig = claude.ClaudeConfig
