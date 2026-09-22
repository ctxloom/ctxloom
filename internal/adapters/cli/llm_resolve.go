package cli

import (
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// isTestOnlyBackend reports whether name is a test/development double that
// must never surface in a user-facing engine list. Such a backend is
// registered in the production descriptor table and reachable at runtime
// (`--llm mock`), but is not something a real user should be offered.
//
// It asks the REGISTRY rather than comparing a name: a backend declares that
// it is test-only where it is registered, so a newly registered double is
// hidden everywhere at once rather than wherever someone remembers to skip
// its name.
func isTestOnlyBackend(name string) bool { return operations.IsTestOnlyEngine(name) }

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
		return ok && cfg.EffectiveType(entry) == backendType
	}
	// Short-circuit on the primary label only when it actually decodes; an
	// undecodable primary (operations.DecodeBackendConfig warns and returns nil)
	// must fall through to a same-type sibling that can decode rather than
	// degrading the whole resolution to unconfigured defaults.
	if primary := cfg.PrimaryLabel(); matchesType(primary) {
		if bc := operations.DecodeBackendConfig(cfg, primary); bc != nil {
			return bc
		}
	}
	for _, label := range cfg.GetLLMLabels() {
		if matchesType(label) {
			if bc := operations.DecodeBackendConfig(cfg, label); bc != nil {
				return bc
			}
		}
	}
	return nil
}
