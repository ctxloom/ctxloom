package cli

import (
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// mockBackendName is backends.List()'s one entry that must never surface in a
// user-facing engine list: the "mock" backend is a test/development double
// registered into the production descriptor table (registry.go), reachable
// at runtime as `--llm mock`, but not something a real user should ever be
// offered as a choice. Every user-facing enumeration over backends.List()
// filters it out through this one name — a prior review found two independent
// hand-written `== "mock"` skips (here and in init.go) before this constant
// existed; consolidating the literal to one place is the minimal fix that
// keeps working without reaching into the backends package's registration
// table (which is outside this package).
const mockBackendName = "mock"

// isMockBackend reports whether name is the test-only mock backend.
func isMockBackend(name string) bool { return name == mockBackendName }

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
