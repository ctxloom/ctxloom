package backends

import (
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/lm/engine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// mockExports resolves mock's per-prompt command export. Every prompt is
// ENABLED: mock has no per-engine export block of its own in a bundle's LLM
// section, and a mock that silently exported nothing would be a commands
// surface that reports success and writes zero bytes — precisely the silent
// no-op the mock engine exists to catch in others. It mirrors
// mockSkillExports's "everything is enabled" for the same reason.
func mockExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	return engine.BuildCommandExports(prompts, func(*bundles.LoadedContent) agent.CommandExport {
		return agent.CommandExport{Enabled: true}
	})
}
