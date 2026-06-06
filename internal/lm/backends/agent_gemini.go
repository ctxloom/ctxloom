package backends

import "github.com/ctxloom/gemini"

// The gemini launch backend (Gemini + capabilities) lives in its own module
// github.com/ctxloom/gemini alongside its settings writer. This alias keeps the
// wiring layer's config decoder and external callers (cmd/llm_resolve)
// referencing the typed config unchanged. The backend itself is constructed via
// gemini.NewGemini in the registry.
type GeminiConfig = gemini.GeminiConfig
