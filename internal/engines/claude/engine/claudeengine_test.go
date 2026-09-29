package claudeengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

func TestTranscripts_AreTheReadersAsPortValues(t *testing.T) {
	readers := Transcripts()
	require.NotEmpty(t, readers)
	for _, r := range readers {
		min, max := r.Versions()
		assert.NotEmpty(t, min)
		assert.NotEmpty(t, max)
	}
}

func TestVersion_IsDeclaredAndVersionLeadsNameFollows(t *testing.T) {
	v := Version()
	require.True(t, v.Declared())
	got, err := v.Parse("2.1.225 (Claude Code)\n")
	require.NoError(t, err)
	assert.Equal(t, "2.1.225", got)
}

// The kind the root composes with these injections is Hosted: every
// capability claude carries is reachable off the engine value by name.
func TestBuild_WithTheRootsInjections_IsHostedAndVersioned(t *testing.T) {
	e, err := claude.Build(claude.WithTranscripts(Transcripts()...), claude.WithVersion(Version()))
	require.NoError(t, err)
	h, ok := e.(agent.Hosted)
	require.True(t, ok)
	assert.Equal(t, claude.EngineName, h.Backend(nil).Name())
	assert.Equal(t, claude.EngineName, h.NewConfig().BackendType())
	_, scoped := h.HookGlobalScope()
	assert.True(t, scoped)
	assert.True(t, e.Root().Version.Declared())
	assert.Len(t, e.Transcripts(), len(Transcripts()))
}

// TestVersion_Floor: ctxloom drives claude only from the version its
// headless route (--permission-prompts, permission_denied frames) was
// verified on.
func TestVersion_Floor(t *testing.T) {
	require.Equal(t, "2.1.283", Version().Floor)
}
