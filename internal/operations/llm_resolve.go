package operations

import (
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// DecodeBackendConfig decodes the labeled LLM entry into its backend's typed
// config via the backend registry. The label is looked up verbatim; the
// backend is chosen solely by the entry's type. Returns nil on a missing label
// or unknown/undecodable type (fault tolerant — caller degrades to defaults).
func DecodeBackendConfig(cfg *config.Config, label string) agent.BackendConfig {
	entry, ok := cfg.GetLLMEntry(label)
	if !ok {
		return nil
	}
	bc, err := backends.DecodeLLMConfig(entry.EffectiveType(), entry.Body)
	if err != nil {
		clidiag.Warn("ctxloom", "LLM config %q: %v", label, err)
		if entry.EffectiveType() == "gemini" || entry.EffectiveType() == "antigravity" {
			// Both "gemini" (the pre-v4 name) and its v4 successor
			// "antigravity" are removed backends with no supported
			// replacement — 0.7.0 dropped the Antigravity CLI (agy) engine
			// entirely, not just renamed it. It rides clidiag like the line
			// above it: that is the one channel that honours the process's
			// structured-diagnostics wire shape and the TUI's sink redirect,
			// both of which a bare write to os.Stderr corrupts.
			clidiag.Warn("ctxloom", "the %q backend is not supported in this release; point this entry's type at a currently-supported engine (claude-code, codex, opencode)", entry.EffectiveType())
		}
		return nil
	}
	return bc
}

// mockControlConfig is implemented by the mock doubles' BackendConfig types
// (backends.MockConfig and its siblings), the only label bodies that carry a
// map of variables for the launched process: the CTXLOOM_MOCK_* test-control
// knobs. No real engine's config carries one — an engine's credentials and
// environment are ambient, never ctxloom's (config.RetiredLLMEnvKey). It is a
// structural interface local to this package, not part of
// agent.BackendConfig, so MockControlFor reaches the map without a
// concrete-type switch: internal/operations (the ADR-0026 core) must not
// branch on a backend's identity — the dispatch shape agent.BackendConfig's
// own doc prescribes: "shared code carries the interface and never
// type-switches on the backend."
type mockControlConfig interface {
	MockControl() map[string]string
}

// MockControlFor returns the test-control map a labeled entry carries, for
// callers that pass env through the run request. Empty when the label is
// unset, carries none, or its decoded config type does not implement
// mockControlConfig — every real engine.
func MockControlFor(cfg *config.Config, label string) map[string]string {
	bc := DecodeBackendConfig(cfg, label)
	if mc, ok := bc.(mockControlConfig); ok {
		return mc.MockControl()
	}
	return nil
}
