package cli

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/antigravity"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// mockBackendName is backends.List()'s one entry that must never surface in a
// user-facing engine list: the "mock" backend is a test/development double
// registered into the production descriptor table (registry.go), reachable
// at runtime as `--llm mock`, but not something a real user should ever be
// offered as a choice. Every user-facing enumeration over backends.List()
// filters it out through this one name — U057-F14 found two independent
// hand-written `== "mock"` skips (here and in init.go) before this constant
// existed; consolidating the literal to one place is the minimal fix that
// keeps working without reaching into the backends package's registration
// table (which is outside this package).
const mockBackendName = "mock"

// isMockBackend reports whether name is the test-only mock backend.
func isMockBackend(name string) bool { return name == mockBackendName }

// decodeBackendConfig decodes the labeled LLM entry into its backend's typed
// config via the backend registry. The label is looked up verbatim; the
// backend is chosen solely by the entry's type. Returns nil on a missing label
// or unknown/undecodable type (fault tolerant — caller degrades to defaults).
func decodeBackendConfig(cfg *config.Config, label string) agent.BackendConfig {
	entry, ok := cfg.GetLLMEntry(label)
	if !ok {
		return nil
	}
	bc, err := backends.DecodeLLMConfig(entry.EffectiveType(), entry.Body)
	if err != nil {
		clidiag.Warn("ctxloom", "LLM config %q: %v", label, err)
		if entry.EffectiveType() == "gemini" {
			// Removed backend with a known successor: point at the fix. The
			// config upgrade rewrites this on load; the hint covers configs
			// where that rewrite has not been committed yet.
			fmt.Fprintf(os.Stderr, "ctxloom: note: the \"gemini\" backend was replaced by \"antigravity\" (Antigravity CLI, binary agy); update the entry's type to \"antigravity\" or re-run ctxloom to apply the config upgrade\n")
		}
		return nil
	}
	return bc
}

// decodeBackendConfigForType returns the decoded config of a labeled entry
// whose type matches backendType. Used where only a backend type is known
// (the self-invoked serve transport without --label). Selection is
// deterministic: the primary role's label wins when its type matches, then
// the lexicographically first matching label — never Go map order, which
// with two labels of the same type would configure a random entry per
// process.
func decodeBackendConfigForType(cfg *config.Config, backendType string) agent.BackendConfig {
	matchesType := func(label string) bool {
		entry, ok := cfg.GetLLMEntry(label)
		return ok && entry.EffectiveType() == backendType
	}
	// Short-circuit on the primary label only when it actually decodes; an
	// undecodable primary (decodeBackendConfig warns and returns nil) must fall
	// through to a same-type sibling that can decode rather than degrading the
	// whole resolution to unconfigured defaults.
	if primary := cfg.PrimaryLabel(); matchesType(primary) {
		if bc := decodeBackendConfig(cfg, primary); bc != nil {
			return bc
		}
	}
	for _, label := range cfg.GetLLMLabels() {
		if matchesType(label) {
			if bc := decodeBackendConfig(cfg, label); bc != nil {
				return bc
			}
		}
	}
	return nil
}

// llmEnvFor returns the env map a labeled entry carries, for callers that pass
// env through the run request. Empty when the label is unset or carries none.
func llmEnvFor(cfg *config.Config, label string) map[string]string {
	bc := decodeBackendConfig(cfg, label)
	switch c := bc.(type) {
	case *claude.ClaudeConfig:
		return c.Env
	case *antigravity.AntigravityConfig:
		return c.Env
	case *codex.CodexConfig:
		return c.Env
	case *backends.MockConfig:
		return c.Env
	}
	return nil
}
