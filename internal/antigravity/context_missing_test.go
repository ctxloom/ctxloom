package antigravity

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestAntigravityHookWriter_UnresolvableContextHashIsLoud pins a real bug,
// the twin of kiro's identical one at the same seam: an unreadable context
// file was downgraded to content == "", and empty content is how the caller says "no
// context" — so WriteManagedContext STRIPPED the managed section (removing
// AGENTS.md when nothing user-authored remained) and WriteSettings returned nil.
// The agent then launched with zero delivered context, exit 0, and the last good
// delivery destroyed. A non-empty hash asserts context exists; failing to
// resolve it is a failed determination, not a request to deliver nothing.
func TestAntigravityHookWriter_UnresolvableContextHashIsLoud(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}

	// A managed section from a previous, successful delivery.
	writeContextFixture(t, fs, "goodhash", "LAST GOOD CONTEXT")
	require.NoError(t, writer.WriteSettings(&wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{ContextHash: "goodhash"}},
	}}, nil, nil, "/project"))

	// Now the same writer is asked to deliver a hash that was never written.
	err := writer.WriteSettings(&wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{ContextHash: "deadbeefdeadbeef"}},
	}}, nil, nil, "/project")
	require.Error(t, err, "an unresolvable context hash must fail, not strip the section quietly")
	assert.Contains(t, err.Error(), "deadbeefdeadbeef", "the error must name the hash it could not resolve")

	data, readErr := afero.ReadFile(fs, "/project/.agents/AGENTS.md")
	require.NoError(t, readErr, "the previous managed section must survive a failed reconcile")
	assert.Contains(t, string(data), "LAST GOOD CONTEXT")
}

// An empty hash still strips the managed section: no context configured is a
// legitimate nothing-to-do, and it is also how teardown removes the section.
func TestAntigravityHookWriter_EmptyHashStillStrips(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	writeContextFixture(t, fs, "goodhash", "OLD CONTEXT")
	require.NoError(t, writer.WriteSettings(&wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{ContextHash: "goodhash"}},
	}}, nil, nil, "/project"))

	require.NoError(t, writer.WriteSettings(&wire.HooksConfig{}, nil, nil, "/project"))

	data, err := afero.ReadFile(fs, "/project/.agents/AGENTS.md")
	if err == nil {
		assert.NotContains(t, string(data), "OLD CONTEXT")
	}
}
