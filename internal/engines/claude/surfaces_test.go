package claude

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
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
// no-fallback condition at the seam. The context approach's session-home
// form lands beneath the private root; handed a run that advises none, it
// refuses with ErrUnrootedSessionHome and writes neither the project's
// CLAUDE.md nor anything else, and presents nothing a launch flag could
// name. The tempting substitution — CLAUDE.md for the system prompt — is a
// different product behaviour under a different name, and the surface is
// the wrong place to pick one.
func TestPrivateRootApproaches_RefuseWithoutAnEngineHome(t *testing.T) {
	project := t.TempDir()
	d, err := claudeDef(t).Context.DeliverContext(runRoots(project, ""), present.RootSessionHome,
		engine.ContextInputs{Text: []byte("# Rules\nthe secret color is vermilion")}, nil)
	require.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
	assert.Equal(t, present.Delivered{}, d, "a refusal declares and presents nothing")
	assert.Empty(t, dirEntries(t, project), "no fallback to the project file")
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
