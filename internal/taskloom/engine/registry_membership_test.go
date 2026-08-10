package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAll_RegistryMembershipIsExact pins exactly which engines
// `taskloom manage install` walks when no --engine is named, and is the
// tripwire for the snowy-worst failure shape: an engine ctxloom carries as a
// backend but that has no agent.MCPRegistrar is not merely unsupported here —
// with ANOTHER backend also present, auto-register succeeds, reports the other
// backend, and says nothing whatsoever about the missing one. That is how kiro
// went unregistered (see TestKiro_RegisteredInEngineRegistry).
//
// opencode is deliberately absent and must stay a DECISION rather than an
// oversight. It has no home-rooted config at all — its only MCP surface is the
// `mcp` key of a project-cwd opencode.json, which the live run/oneshot path
// writes TRANSIENTLY and restores afterwards (internal/opencode/chat.go,
// surfaces.go's "live vs static" note). A persistent registration written
// there would be a new surface with different lifetime rules from every other
// engine's, so giving opencode an MCPRegistrar is a design decision, not a
// registry line. Whoever makes that decision changes this list; nobody changes
// the registry without noticing this test.
func TestAll_RegistryMembershipIsExact(t *testing.T) {
	var names []string
	for _, e := range All() {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"claude-code", "codex", "kiro"}, names)
	assert.NotContains(t, names, "opencode",
		"opencode's absence is a decision (no persistent MCP surface); if it gained one, update this pin and the refusal text with it")
}
