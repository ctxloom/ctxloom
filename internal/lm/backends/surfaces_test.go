package backends

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// TestDeclared_OptOutBackends pins the name→Declaration seam's opt-out: a
// name no descriptor claims returns an empty Declaration rather than failing,
// so a caller (materialize) can iterate Deliveries() unconditionally and
// simply deliver nothing. mock is NOT one of these — it registers a real
// Declaration so hermetic delivery tests have somewhere to look.
func TestDeclared_OptOutBackends(t *testing.T) {
	for _, name := range []string{"does-not-exist"} {
		t.Run(name, func(t *testing.T) {
			resolved, err := agent.Select(Declared(name)).WithEverything().Build(agent.SurfaceInputs{}, afero.NewMemMapFs())
			require.NoError(t, err)
			assert.Empty(t, resolved.Deliveries(), "opt-out backend materializes no surfaces")
		})
	}
}

// TestBuildSurfaces_Mock proves the mock descriptor closure routes through
// NewMockSurfaces: EVERY surface is returned — never zero, which was the
// original bug (EmptySurfaceSet made the mock backend unable to prove delivery
// at all), and never a partial set, which was the later one (a double whose
// gaps other fixtures quietly came to depend on). The payload assertions below
// matter more than the count: a delivered file that exists empty is the silent
// no-op this backend exists to catch in others.
func TestDeclared_Mock(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	resolved, err := agent.Select(Declared("mock")).WithEverything().Build(agent.SurfaceInputs{
		Context: "MOCK-CONTEXT-PAYLOAD",
		Skills: []agent.SkillExport{{Name: "reviewer", Enabled: true, Files: []agent.PackageFile{
			{RelPath: "SKILL.md", Content: []byte("MOCK-SKILL-PAYLOAD")},
		}}},
	}, fs)
	require.NoError(t, err)
	assert.Len(t, resolved.Deliveries(), 5, "mock is a complete engine and carries every surface")

	_, _, errs := resolved.DeliverUnder(present.ProjectOnHost(dir))
	require.Empty(t, errs, "mock's surfaces deliver cleanly")

	got, err := afero.ReadFile(fs, filepath.Join(dir, mockContextFilename))
	require.NoError(t, err)
	assert.Contains(t, string(got), "MOCK-CONTEXT-PAYLOAD",
		"the delivered file must carry the actual composed context bytes, not merely exist")

	skill, err := afero.ReadFile(fs, filepath.Join(mockSkillsPath(dir), "reviewer", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "MOCK-SKILL-PAYLOAD", string(skill),
		"the delivered skill package must carry the export's actual bytes")
}

// TestBuildSurfaces_Claude proves the claude descriptor closure routes through
// claude.NewSurfaces: a full set of native surfaces is returned.
func TestDeclared_Claude(t *testing.T) {
	resolved, err := agent.Select(Declared("claude-code")).WithEverything().Build(agent.SurfaceInputs{Context: "hello"}, afero.NewMemMapFs())
	require.NoError(t, err)
	assert.Len(t, resolved.Deliveries(), 5, "claude has context + MCP + settings + commands + skills surfaces")
}

// TestBuildSurfaces_WritesOnlyItsOwnNativeContextFile pins, through the
// delivery seam, that a backend's context surface lands on ITS OWN well-known
// file and never on another engine's — correct by CONSTRUCTION rather than by
// an orchestrator special case. contextHash "" is preserved by passing an empty
// (non-injecting) hook set.
//
// It asserts the POSITIVE first, and that ordering is the point: an assertion
// that some other engine's file is absent is satisfied for free by a backend
// that delivered nothing at all, so the absence half only means something once
// the delivery is known to have happened.
func TestDeclared_WritesOnlyItsOwnNativeContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	_, _, errs := agent.Select(Declared("mock")).WithEverything().DeliverUnder(agent.SurfaceInputs{
		Context:   "assembled context",
		Hooks:     &wire.HooksConfig{},
		BundleMCP: map[string]wire.MCPServer{},
	}, fs, present.ProjectOnHost(dir))
	require.Empty(t, errs, "mock surfaces deliver cleanly")

	own, err := afero.ReadFile(fs, filepath.Join(dir, "MOCK_CONTEXT.md"))
	require.NoError(t, err, "mock's context surface must write its own well-known file")
	assert.Contains(t, string(own), "assembled context",
		"the delivered file must carry the assembled context, not merely exist")

	for _, foreign := range []string{"CLAUDE.md", filepath.Join(".agents", "AGENTS.md")} {
		exists, _ := afero.Exists(fs, filepath.Join(dir, foreign))
		assert.False(t, exists, "a backend must not write another engine's native context file (%s)", foreign)
	}
}
