//go:build parked_engines

// PARKED (parked_engines): every test in this file exercises "codex"'s
// launch-only declaration specifically, and internal/codex is out of the
// default build. grep -rn parked_engines finds every parked site.
package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// launchOnlyInputs carries one of each surface a harpless caller would try to
// deliver, so the "only report what the inputs actually carry" rule can be
// tested against a full set and, below, against an empty one.
func launchOnlyInputs() agent.SurfaceInputs {
	return agent.SurfaceInputs{
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "echo team-guardrail"}},
		}},
		BundleMCP: map[string]wire.MCPServer{"toolserver": {Command: "tool"}},
		Commands:  []agent.CommandExport{{Name: "review", Enabled: true}},
		Skills:    []agent.SkillExport{{Name: "humanize", Enabled: true}},
	}
}

// TestLaunchOnlySurfaces_ReportsCodexsFourSurfaces pins the declaration's read
// side: every surface the inputs carry is reported, each quoting the ONE
// declared reason, so a `--format json` consumer of `profile materialize` sees
// the loss structurally rather than only in a rendered line.
func TestLaunchOnlySurfaces_ReportsCodexsFourSurfaces(t *testing.T) {
	losses := LaunchOnlySurfaces("codex", launchOnlyInputs())

	bySurface := map[string]agent.SurfaceLoss{}
	for _, l := range losses {
		bySurface[l.Surface] = l
	}
	for _, surface := range []string{"hooks", "mcp", "commands", "skills"} {
		loss, ok := bySurface[surface]
		require.True(t, ok, "codex's %s surface is launch-only and must be reported; got %v", surface, losses)
		assert.Equal(t, codex.LaunchOnlySettingsReason, loss.Reason,
			"every launch-only loss quotes the engine's own declaration verbatim")
		assert.NotEmpty(t, loss.Detail, "a loss that does not say WHAT was lost cannot be acted on")
	}
	assert.Contains(t, bySurface["hooks"].Detail, "session_start")
	assert.Contains(t, bySurface["mcp"].Detail, "ctxloom",
		"ctxloom's own auto-registered server is the one whose absence costs the user every tool")
}

// TestLaunchOnlySurfaces_QuietForEveryOtherBackend is the false-alarm guard.
// The declaration is codex's alone; a report that named a launch-only loss for
// claude — which writes .claude/settings.json right there in the project — is
// the fastest way to teach a reader to skip the "NOT carried" lines, taking the
// real codex ones with them.
func TestLaunchOnlySurfaces_QuietForEveryOtherBackend(t *testing.T) {
	for _, name := range []string{"claude-code", "kiro", "opencode", "mock", "acp", "no-such-backend"} {
		assert.Empty(t, LaunchOnlySurfaces(name, launchOnlyInputs()),
			"%s has a durable project surface (or no descriptor at all); nothing about it is launch-only", name)
		assert.Empty(t, LaunchOnlySettingsReason(name), "%s declares no launch-only reason", name)
	}
	assert.Equal(t, codex.LaunchOnlySettingsReason, LaunchOnlySettingsReason("codex"))
}

// TestLaunchOnlySurfaces_ReportsOnlyWhatWasAsked applies the same rule
// UncarriedSurfaces already does: a capability gap nobody asked to use costs
// nothing and stays quiet.
//
// A real loadout always carries ctxloom's own MCP server (the builtin ctxloom
// bundle ships it into every session), so the mcp surface is genuinely lost
// even when the user configured nothing else — that is the row below. A
// SurfaceInputs carrying literally nothing is the degenerate case a resolver
// failure produces, and it must stay silent rather than report a loss of
// something nobody was going to get.
func TestLaunchOnlySurfaces_ReportsOnlyWhatWasAsked(t *testing.T) {
	losses := LaunchOnlySurfaces("codex", agent.SurfaceInputs{
		BundleMCP: map[string]wire.MCPServer{agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}}},
	})

	require.Len(t, losses, 1, "a loadout carrying only ctxloom's own server loses mcp and nothing else; got %v", losses)
	assert.Equal(t, "mcp", losses[0].Surface)

	assert.Empty(t, LaunchOnlySurfaces("codex", agent.SurfaceInputs{}),
		"an input carrying nothing at all loses nothing at all")
}

// TestLaunchOnlySurfaces_IsNotUncarriedSurfaces pins the separation that keeps
// `agent show` truthful. UncarriedSurfaces answers "what can this ENGINE never
// carry" and is read about a live binding — where a codex agent declaring
// `config_home: project` receives every hook at launch. Folding the two would
// tell that user their hooks are lost, about a run that works.
func TestLaunchOnlySurfaces_IsNotUncarriedSurfaces(t *testing.T) {
	in := launchOnlyInputs()

	for _, l := range UncarriedSurfaces("codex", in) {
		assert.NotEqual(t, codex.LaunchOnlySettingsReason, l.Reason,
			"the launch-only declaration must not leak into the engine-capability report")
	}
	assert.NotEmpty(t, LaunchOnlySurfaces("codex", in),
		"precondition: the launch-only report is non-empty for these inputs, so the check above is not vacuous")
}
