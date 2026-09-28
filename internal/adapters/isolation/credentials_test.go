package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPresentEnvKeys_OnlyKnownSetVars: a scoped allowlist forward carries
// ONLY the keys that are actually set, never the host's full environment.
func TestPresentEnvKeys_OnlyKnownSetVars(t *testing.T) {
	env := map[string]string{"TERM": "xterm", "COLORTERM": "", "PATH": "/x"}
	out := presentEnvKeys(func(k string) string { return env[k] }, []string{"TERM", "COLORTERM"})
	assert.Equal(t, []string{"TERM"}, out, "only set, allowlisted keys cross (empty + unlisted dropped)")
}
