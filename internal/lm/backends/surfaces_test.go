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
// backend with no native config format (acp), the mock, and an unregistered name
// all return an EmptySurfaceSet, so a caller (materialize) can iterate
// Deliveries() unconditionally and simply deliver nothing.
func TestBuildSurfaces_OptOutBackends(t *testing.T) {
	for _, name := range []string{"acp", "mock", "does-not-exist"} {
		t.Run(name, func(t *testing.T) {
			set := BuildSurfaces(name, agent.SurfaceInputs{}, afero.NewMemMapFs())
			assert.Empty(t, set.Deliveries(), "opt-out backend materializes no surfaces")
		})
	}
}

// TestBuildSurfaces_Claude proves the claude descriptor closure routes through
// claude.NewSurfaces: a full set of native surfaces is returned.
func TestBuildSurfaces_Claude(t *testing.T) {
	set := BuildSurfaces("claude-code", agent.SurfaceInputs{Context: "hello"}, afero.NewMemMapFs())
	assert.Len(t, set.Deliveries(), 4, "claude has context + MCP + settings + commands surfaces")
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
