package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// runRoots advises a run rooted at project with its session home at
// sessionHome, on the host — the roots claude's well-known and private-root
// approaches read respectively. Tests advise DISTINCT dirs so an assertion on
// one root cannot be satisfied by a write to another.
func runRoots(project, sessionHome string) present.Start {
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: project},
		SessionHome: present.Root{Host: sessionHome},
	}))
}

// sampleContext is a representative assembled context.
const sampleContext = "# Rules\nthe secret color is vermilion"

// newSystemPromptContext is claude's system-prompt writer over the real
// filesystem — the one contextApproach.DeliverContext drives at the session
// home.
func newSystemPromptContext() *systemPromptContext {
	return &systemPromptContext{content: sampleContext, fs: afero.NewOsFs()}
}

// ---- context surface -------------------------------------------------------

// The system-prompt approach's ONE form writes the framed <hash>.sysprompt.md
// beneath the private root (via appendFlagDelivery) and exposes it via Path()
// — and does NOT touch the well-known CLAUDE.md. Every cell reaches this form;
// there is no second one for a cell to pick instead.
func TestContextSurface_DeliverWritesSyspromptAndExposesPath(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	s := newSystemPromptContext()

	handle, err := s.Deliver(runRoots(cwd, home))
	require.NoError(t, err)

	path := s.Path()
	require.NotEmpty(t, path, "Path() exposes the framed file for --append-system-prompt-file")
	assert.Equal(t, home, filepath.Dir(path), "the framed file lands beneath the relocated engine home")
	assert.True(t, strings.HasSuffix(path, agent.SCMFramedContextSuffix))
	require.FileExists(t, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, agent.FrameProjectContext(sampleContext), string(data))

	// The cwd receives nothing, and the home holds no CLAUDE.md.
	assert.NoFileExists(t, filepath.Join(cwd, "CLAUDE.md"))
	assert.NoFileExists(t, filepath.Join(home, "CLAUDE.md"))

	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, path)
}

// ---- the declaration ---------------------------------------------------------

// Surfaces pins claude's per-surface declaration: context offers two
// approaches (native file, system prompt) — no hook carries it; MCP offers
// the private config file and the project file; settings, commands and skills
// offer only the native file, and settings refuses the retired engine-home
// record write by name.
//
// The DEFAULTS are the load-bearing half. MCP's is the PRIVATE form, and it is
// the one surface whose default is not the native file: delivering ctxloom's
// MCP set by writing the user's project .mcp.json is a shared/dangerous avenue,
// so it must be asked for by name. Context's default stays the native file —
// a shared launch derives the system prompt instead, which is a preference, not
// a declaration.
func TestSurfaces_DeclaresContextTwoWaysMCPTwoAndTheRestOnce(t *testing.T) {
	assert.ElementsMatch(t, []string{agent.ApproachUnsafeFile, ApproachSystemPrompt},
		testDeclaration().Names(agent.SurfaceContext))
	assert.ElementsMatch(t, []string{agent.ApproachUnsafeFile, ApproachMCPConfig}, testDeclaration().Names(agent.SurfaceMCP))
	for _, kind := range []agent.SurfaceKind{agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		assert.Equal(t, []string{agent.ApproachUnsafeFile}, testDeclaration().Names(kind), "%s", kind)
	}

	mcpDef, ok := testDeclaration().Default(agent.SurfaceMCP)
	require.True(t, ok)
	assert.Equal(t, ApproachMCPConfig, mcpDef,
		"the project .mcp.json must never be the default — it is reachable only by name")
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		def, ok := testDeclaration().Default(kind)
		require.True(t, ok, "%s has a default approach", kind)
		assert.Equal(t, agent.ApproachUnsafeFile, def)
	}
}

// TestPrivateRootApproaches_RefuseWithoutAnEngineHome is feeble-sway's
// no-fallback condition at the seam. An approach whose ONLY form lands beneath
// the private root, handed a run that advises none, refuses with
// ErrUnrootedSessionHome: it writes neither the project file nor anything
// else, and records no path for a launch flag to name. The tempting
// substitutions — CLAUDE.md for the system prompt, the project .mcp.json for
// the private one — are each a different product
// behaviour under a different name, and the surface is the wrong place to
// pick one.
func TestPrivateRootApproaches_RefuseWithoutAnEngineHome(t *testing.T) {
	project := t.TempDir()
	s := newSystemPromptContext()
	noHome := runRoots(project, "")

	for _, tc := range []struct {
		name    string
		deliver func(present.Start) (agent.Delivered, error)
		path    func() string
	}{
		{"context", s.Deliver, s.Path},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handle, err := tc.deliver(noHome)
			require.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
			assert.Nil(t, handle, "a refusal holds no cleanup handle: nothing was written")
			assert.Empty(t, tc.path(), "a refusal records no path — no flag may name it")
			assert.Empty(t, dirEntries(t, project), "no fallback to the project file")
		})
	}
}

// dirEntries lists the names directly beneath dir, so a test can assert a
// root received nothing (or nothing of a given shape).
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
