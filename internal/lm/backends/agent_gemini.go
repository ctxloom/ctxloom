package backends

import "github.com/ctxloom/ctxloom/internal/agent/gemini"

// The gemini launch backend (Gemini + capabilities) lives in
// internal/agent/gemini alongside its settings writer. This alias keeps the
// wiring layer's config decoder and external callers (cmd/llm_resolve)
// referencing the typed config unchanged. The backend itself is constructed via
// gemini.NewGemini in the registry.
type GeminiConfig = gemini.GeminiConfig
