//go:build parked_engines

package kiro

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestKiroWriter_UnreadableContextHashIsLoud pins a real bug: a non-empty
// context hash whose file cannot be read used to be downgraded to content ==
// "", which writeSteering interprets as "the caller wants no context" — so it
// REMOVED the steering file kiro auto-loads and returned nil. The session
// then launched with zero context bytes and exit 0, and the
// previously-delivered context was destroyed on the way out. A hash is an
// assertion that context exists; failing to resolve it is a failure to
// determine what to deliver, not a request to deliver nothing.
func TestKiroWriter_UnreadableContextHashIsLoud(t *testing.T) {
	w, fs := newTestWriter()

	// A steering file from a previous, successful delivery.
	require.NoError(t, fs.MkdirAll("/proj/.kiro/steering", 0o755))
	require.NoError(t, afero.WriteFile(fs, "/proj/.kiro/steering/ctxloom-context.md",
		[]byte("---\ninclusion: always\n---\n\nLAST GOOD CONTEXT\n"), 0o644))

	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		// A hash that was never written (reaped cache / wrong workDir).
		SessionStart: []wire.Hook{{ContextHash: "deadbeefdeadbeef"}},
	}}
	err := w.WriteSettings(hooks, nil, "/proj")
	require.Error(t, err, "an unresolvable context hash must fail, not deliver nothing quietly")
	assert.Contains(t, err.Error(), "deadbeefdeadbeef", "the error must name the hash it could not resolve")

	// And it must not have destroyed the last good delivery on the way out.
	body, readErr := afero.ReadFile(fs, "/proj/.kiro/steering/ctxloom-context.md")
	require.NoError(t, readErr, "the previously-delivered steering file must survive a failed reconcile")
	assert.Contains(t, string(body), "LAST GOOD CONTEXT")
}

// An EMPTY hash still means "no context configured" and still removes the
// steering file: that is a legitimate nothing-to-do, and the discriminator that
// keeps the fix above from breaking teardown (RemoveSettings routes through the
// same reconcile with an empty hash).
func TestKiroWriter_EmptyHashStillRemovesSteering(t *testing.T) {
	w, fs := newTestWriter()
	require.NoError(t, fs.MkdirAll("/proj/.kiro/steering", 0o755))
	require.NoError(t, afero.WriteFile(fs, "/proj/.kiro/steering/ctxloom-context.md", []byte("stale"), 0o644))

	require.NoError(t, w.WriteSettings(&wire.HooksConfig{}, nil, "/proj"))

	exists, err := afero.Exists(fs, "/proj/.kiro/steering/ctxloom-context.md")
	require.NoError(t, err)
	assert.False(t, exists, "no configured context still removes the ctxloom-owned steering file")
}

// The happy path is unchanged: a resolvable hash delivers its content.
func TestKiroWriter_ResolvableHashStillDelivers(t *testing.T) {
	w, fs := newTestWriter()
	hash, err := agent.WriteContextFile("/proj", []*agent.Fragment{{Content: "PROJECT RULES"}}, agent.WithContextFS(fs))
	require.NoError(t, err)

	require.NoError(t, w.WriteSettings(&wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{ContextHash: hash}},
	}}, nil, "/proj"))

	body, err := afero.ReadFile(fs, "/proj/.kiro/steering/ctxloom-context.md")
	require.NoError(t, err)
	assert.Contains(t, string(body), "PROJECT RULES")
}
