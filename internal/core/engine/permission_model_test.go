package engine_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func TestApprover_Reviewer(t *testing.T) {
	a, ok := engine.ParseApprover("reviewer")
	require.True(t, ok)
	assert.Equal(t, engine.ApproverReviewer, a)
	assert.Equal(t, "reviewer", engine.ApproverReviewer.String())
	assert.Equal(t, []string{"human", "none", "reviewer"}, engine.ApproverNames())
}

// The sandbox vocabulary is engine-neutral; each engine maps it. The zero
// value is "nobody declared one", which the engine's default replaces at
// resolve — it is never a resolved sandbox.
func TestSandbox_Vocabulary(t *testing.T) {
	var zero engine.Sandbox
	assert.Equal(t, engine.SandboxUnspecified, zero)
	for in, want := range map[string]engine.Sandbox{"read-only": engine.SandboxReadOnly, "workspace-write": engine.SandboxWorkspaceWrite, " Full ": engine.SandboxFull} {
		got, ok := engine.ParseSandbox(in)
		require.Truef(t, ok, "%q", in)
		assert.Equal(t, want, got)
		assert.Equal(t, got, mustParse(t, got.String()), "String round-trips")
	}
	for _, in := range []string{"", "workspace", "none", "danger-full-access"} {
		_, ok := engine.ParseSandbox(in)
		assert.Falsef(t, ok, "%q", in)
	}
	assert.Equal(t, []string{"read-only", "workspace-write", "full"}, engine.SandboxNames())
	assert.Contains(t, engine.SandboxUnspecified.String(), "unspecified")
}

func mustParse(t *testing.T, s string) engine.Sandbox {
	t.Helper()
	got, ok := engine.ParseSandbox(s)
	require.True(t, ok, s)
	return got
}

// A posture is carried by core without being read: the engine's name and
// its own document.
func TestPosture_IsOpaqueToCore(t *testing.T) {
	p := engine.Posture{Engine: "claude-code", Document: map[string]any{"mode": "plan"}}
	c := p.Clone()
	c.Document["mode"] = "bypass"
	assert.Equal(t, "plan", p.Document["mode"], "a clone never aliases the document")
}
