package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// SupportsSkills must have BOTH arms available in the registry.
//
// Until mock-noskills existed, every registered backend declared skillExports,
// so this predicate could only ever return true and every caller's
// missing-skills branch was unreachable from a test. A suite in that state
// looks well covered and proves one half of a two-way decision.
//
// If a future change gives mock-noskills a skillExports mapper, THIS TEST IS
// THE ONE THAT SHOULD STOP YOU: its absence is the double's entire purpose.
func TestSupportsSkills_HasATrueArmAndAFalseArm(t *testing.T) {
	assert.True(t, SupportsSkills(config.BackendMock),
		"mock declares skillExports and must report true")
	assert.False(t, SupportsSkills(config.BackendMockNoSkills),
		"mock-noskills declares NO skillExports and must report false — it is the only subject the missing-surface arm has")
	assert.True(t, SupportsSkills("claude-code"),
		"the real backend still reports true; the new double must not have changed it")
}
