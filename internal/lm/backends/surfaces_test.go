package backends

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestBuildSurfaces_OptOutBackends pins the name→SurfaceSet seam's opt-out: a
// backend with no native config format (acp) and an unregistered name both
// return an EmptySurfaceSet, so a caller (materialize) can iterate
// Deliveries() unconditionally and simply deliver nothing. mock is NOT one of
// these any more — see TestBuildSurfaces_Mock: it registers a real
// (context-only) SurfaceSet so hermetic delivery tests have somewhere to look.
func TestBuildSurfaces_OptOutBackends(t *testing.T) {
	for _, name := range []string{"acp", "does-not-exist"} {
		t.Run(name, func(t *testing.T) {
			set := BuildSurfaces(name, agent.SurfaceInputs{}, afero.NewMemMapFs())
			resolved, err := agent.Select(set).WithEverything().Build()
			require.NoError(t, err)
			assert.Empty(t, resolved.Deliveries(), "opt-out backend materializes no surfaces")
		})
	}
}

// TestBuildSurfaces_Mock proves the mock descriptor closure routes through
// NewMockSurfaces: exactly the one context surface is returned (never zero,
// which was the whole bug this change fixes — EmptySurfaceSet made the mock
// backend unable to prove delivery at all), and — the payload assertion, not
// merely a count — the delivered file actually carries the composed context
// bytes rather than existing empty.
func TestBuildSurfaces_Mock(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	set := BuildSurfaces("mock", agent.SurfaceInputs{Context: "MOCK-CONTEXT-PAYLOAD"}, fs)
	resolved, err := agent.Select(set).WithEverything().Build()
	require.NoError(t, err)
	assert.Len(t, resolved.Deliveries(), 1, "mock has exactly the context surface")

	_, _, errs := resolved.DeliverUnder(dir)
	require.Empty(t, errs, "mock context surface delivers cleanly")

	got, err := afero.ReadFile(fs, filepath.Join(dir, mockContextFilename))
	require.NoError(t, err)
	assert.Contains(t, string(got), "MOCK-CONTEXT-PAYLOAD",
		"the delivered file must carry the actual composed context bytes, not merely exist")
}

// TestBuildSurfaces_Claude proves the claude descriptor closure routes through
// claude.NewSurfaces: a full set of native surfaces is returned.
func TestBuildSurfaces_Claude(t *testing.T) {
	set := BuildSurfaces("claude-code", agent.SurfaceInputs{Context: "hello"}, afero.NewMemMapFs())
	resolved, err := agent.Select(set).WithEverything().Build()
	require.NoError(t, err)
	assert.Len(t, resolved.Deliveries(), 5, "claude has context + MCP + settings + commands + skills surfaces")
}

// TestBuildSurfaces_Kiro proves the kiro descriptor closure routes through
// kiro.NewSurfaces and now includes the skills surface (Part B5) alongside
// context/MCP/settings/commands — kiro is the collision engine where commands
// and skills share one native directory, reconciled inside kiro.NewSurfaces.
func TestBuildSurfaces_Kiro(t *testing.T) {
	set := BuildSurfaces("kiro", agent.SurfaceInputs{Context: "hello"}, afero.NewMemMapFs())
	resolved, err := agent.Select(set).WithEverything().Build()
	require.NoError(t, err)
	assert.Len(t, resolved.Deliveries(), 5, "kiro has context + MCP + settings + commands + skills surfaces")
}

// TestBuildSurfaces_CodexNoNativeContextFile pins the codex opt-out invariant
// through the delivery seam: materializing codex writes only its config/cache
// surfaces — never a native context file (no CLAUDE.md, no AGENTS.md), which is
// what makes codex correct by CONSTRUCTION rather than by an orchestrator special
// case. contextHash "" is preserved by passing an empty (non-injecting) hook set.
func TestBuildSurfaces_CodexNoNativeContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	set := BuildSurfaces("codex", agent.SurfaceInputs{
		Context: "assembled context",
		Hooks:   &wire.HooksConfig{},
		MCP:     &wire.MCPConfig{Servers: map[string]wire.MCPServer{}},
	}, fs)
	_, _, errs := agent.Select(set).WithEverything().DeliverUnder(dir)
	require.Empty(t, errs, "codex surfaces deliver cleanly")

	for _, native := range []string{"CLAUDE.md", filepath.Join(".agents", "AGENTS.md")} {
		exists, _ := afero.Exists(fs, filepath.Join(dir, native))
		assert.False(t, exists, "codex must not write a native context file (%s)", native)
	}
}
