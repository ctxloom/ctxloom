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
// backend, and says nothing whatsoever about the missing one.
func TestAll_RegistryMembershipIsExact(t *testing.T) {
	var names []string
	for _, e := range All() {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"claude-code"}, names)
}
