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
			clidiag.Warn("ctxloom", "the %q backend is not supported in this release; point this entry's type at a currently-supported engine (claude-code, codex, kiro, opencode)", entry.EffectiveType())
		}
		return nil
	}
	return bc
}

// envConfig is implemented by every BackendConfig that carries a labeled
// entry's env map (ClaudeConfig, CodexConfig, MockConfig today). It is a
// structural interface local to this package, not part of agent.BackendConfig
// itself, so LLMEnvFor can reach a decoded config's Env without a
// concrete-type switch — internal/operations (the ADR-0026 core) must not
// import engine plugin packages directly (see
// tests/arch/engine_identity_arch_test.go's
// TestArch_Operations_DoesNotImportEnginePlugins), which a
// *claude.ClaudeConfig/*codex.CodexConfig type switch would violate. This is
// exactly the dispatch shape agent.BackendConfig's own doc already prescribes:
// "shared code carries the interface and never type-switches on the backend."
type envConfig interface {
	GetEnv() map[string]string
}

// LLMEnvFor returns the env map a labeled entry carries, for callers that pass
// env through the run request. Empty when the label is unset, carries none, or
// its decoded config type does not implement envConfig.
func LLMEnvFor(cfg *config.Config, label string) map[string]string {
	bc := DecodeBackendConfig(cfg, label)
	if ec, ok := bc.(envConfig); ok {
		return ec.GetEnv()
	}
	return nil
}
