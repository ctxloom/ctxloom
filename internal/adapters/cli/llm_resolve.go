package cli

import (
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
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
func isTestOnlyBackend(name string) bool { return operations.IsTestOnlyEngine(App().Engines(), name) }
