package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/resources"
)

// goldenHomeConfigYAML is a FROZEN snapshot of a real developer's
// ~/.ctxloom/config.yaml (captured 2026-07-19, this repo's maintainer
// machine, before this change) — an LLM registry spanning two backends
// (claude-code, antigravity) plus the legacy (pre-v6) `profiles.defaults`
// key. It is a literal here, never read live: this test must stay hermetic
// and portable, and must never touch the real ~/.ctxloom (see testsupport's
// isolation charter). It exists so the D3 characterization below exercises
// layering against a REALISTIC home shape, not a toy.
const goldenHomeConfigYAML = `llm:
  configs:
    claude-code:
      type: claude-code
      model: claude-opus-4-8
    claude-fast:
      type: claude-code
      model: claude-haiku-4-5-20251001
    gemini-code:
      type: antigravity
      model: gemini-2.5-pro
    gemini-fast:
      type: antigravity
      model: gemini-2.5-flash
  defaults:
    primary: claude-code
    fast: claude-fast
profiles:
  defaults:
    - https://github.com/benjaminabbitt/ctxloom-personal@bundles/go-development#profiles/go-developer
version: 5
`

// TestGoldenFixture_CurrentEffectiveConfig_D3Characterization is the REQUIRED
// MITIGATION D3 calls for: before accepting "a project inherits home instead
// of replacing it", pin the CURRENT (pre-layering-in-practice) effective
// config for a realistic pairing, so any drift beyond the accepted D3 change
// fails a test instead of surfacing later as mystery behavior.
//
// The project layer is resources/init-config.yaml — THIS repo's own real,
// committed `ctxloom init` project template (read live via
// resources.GetInitConfig, not copied, so it never drifts from what init
// actually writes). The home layer is goldenHomeConfigYAML, a frozen
// real-world home config shape.
//
// "projectOnly" reproduces the PRE-layering resolution exactly: home is
// simply absent from that run's filesystem, which is what project-XOR-home
// always saw once a project existed (home was never even opened). "layered"
// is what THIS change produces with the identical project template plus a
// home layer present. Diffing the two IS the D3 behavior change made
// concrete — the test below enumerates it field by field, and the change
// must be ADDITIVE ONLY: every value projectOnly pins must still hold in
// layered (project never loses to an inherited value), and every new value
// layered carries must trace to a home-only key init-config.yaml never sets.
func TestGoldenFixture_CurrentEffectiveConfig_D3Characterization(t *testing.T) {
	home := testsupport.Isolate(t)
	projectAppDir := "/proj/.ctxloom"

	initConfig, err := resources.GetInitConfig()
	require.NoError(t, err)

	// ---- PRE-layering baseline: project alone, home absent from disk ----
	fsProjectOnly := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsProjectOnly, paths.ConfigPath(projectAppDir), initConfig, 0644))
	projectOnly, err := Load(WithFS(fsProjectOnly), WithAppDir(projectAppDir))
	require.NoError(t, err)

	// Pin today's known-good shape of the shipped init template alone, so a
	// FUTURE edit to init-config.yaml (or the upgrade/default-overlay
	// machinery) that silently changes its effective config also fails here,
	// independent of layering.
	assert.True(t, projectOnly.ShouldUseDistilled())
	assert.Empty(t, projectOnly.defaultAgent,
		"init-config.yaml deliberately carries no default_agent — init fills it in after engine selection")
	assert.Empty(t, projectOnly.agents)
	assert.NotEmpty(t, projectOnly.lm.Configs,
		"an empty user registry adopts ctxloom's EMBEDDED built-in default (mergeDefaultConfig) when nothing else fills it")
	_, hasBuiltinClaudeCode := projectOnly.lm.Configs["claude-code"]
	assert.True(t, hasBuiltinClaudeCode, "the built-in default registry's label")

	// ---- POST-layering: identical project template, home now participates ----
	fsLayered := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsLayered, paths.ConfigPath(projectAppDir), initConfig, 0644))
	homeAppDir := filepath.Join(home, AppDirName)
	require.NoError(t, afero.WriteFile(fsLayered, paths.ConfigPath(homeAppDir), []byte(goldenHomeConfigYAML), 0644))
	layered, err := Load(WithFS(fsLayered), WithAppDir(projectAppDir))
	require.NoError(t, err)

	// Every value the PROJECT template itself sets survives unchanged —
	// layering must never let an inherited home value beat an explicit
	// project one (that would be the OTHER direction, not D3).
	assert.Equal(t, projectOnly.ShouldUseDistilled(), layered.ShouldUseDistilled(),
		"config.use_distilled is project-set; home carries no config: section at all")
	// THE DRIFT: keys init-config.yaml never mentions are now INHERITED from
	// home instead of staying empty/built-in-default. This is the accepted D3
	// behavior change, pinned explicitly rather than left to be discovered.
	assert.Len(t, layered.lm.Configs, 4,
		"DRIFT (intended): the project's empty llm.configs no longer falls back to ctxloom's built-in "+
			"default registry — it inherits the user's REAL home registry instead")
	assert.Contains(t, layered.lm.Configs, "gemini-code", "a home-only LLM label now reaches the project")
	assert.Equal(t, "claude-code", layered.lm.Defaults.Primary,
		"DRIFT (intended): llm.defaults.primary is now inherited from home")
	assert.Equal(t, "claude-fast", layered.lm.Defaults.Fast)

	// NOT DRIFT (layerscope closed this): default_agent and agents.* are
	// ScopeShared — which agent a bare `ctxloom run` resolves is project
	// policy, so home may not gap-fill it even via a legacy profiles.defaults
	// migrating forward in memory. Before layerscope, this WAS additional
	// drift (home's migrated default_agent leaking into a project that never
	// set one); it is exactly escalation path #2's home-gap-fill half (see
	// TestLoad_EscalationPath3_HomeCannotEscalateProjectAgent), so this
	// golden fixture now pins its ABSENCE instead.
	assert.Empty(t, layered.defaultAgent,
		"home's legacy profiles.defaults must NOT supply a default_agent the project template itself never set")
	assert.NotContains(t, layered.agents, "default",
		"home's migrated agents.default binding must not leak into a project that names no such agent")
}

// TestGoldenFixture_D3Drift_IsAdditiveOnly is the general-purpose companion to
// the concrete characterization above: for the SAME project/home pairing, it
// walks every top-level key init-config.yaml sets and asserts layering left
// it byte-identical, so a future change that lets home creep into a
// project-set key (the wrong direction — project must always win) fails
// loudly here rather than only being caught by the field-by-field pins above.
func TestGoldenFixture_D3Drift_IsAdditiveOnly(t *testing.T) {
	home := testsupport.Isolate(t)
	projectAppDir := "/proj/.ctxloom"

	initConfig, err := resources.GetInitConfig()
	require.NoError(t, err)

	fsProjectOnly := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsProjectOnly, paths.ConfigPath(projectAppDir), initConfig, 0644))
	projectOnly, err := Load(WithFS(fsProjectOnly), WithAppDir(projectAppDir))
	require.NoError(t, err)

	fsLayered := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsLayered, paths.ConfigPath(projectAppDir), initConfig, 0644))
	homeAppDir := filepath.Join(home, AppDirName)
	require.NoError(t, afero.WriteFile(fsLayered, paths.ConfigPath(homeAppDir), []byte(goldenHomeConfigYAML), 0644))
	layered, err := Load(WithFS(fsLayered), WithAppDir(projectAppDir))
	require.NoError(t, err)

	// init-config.yaml sets exactly: version and config.use_distilled (see the
	// template's own doc comment — llm, agents, default_agent are deliberately
	// absent, filled in by `init` itself, not the static template).
	assert.Equal(t, projectOnly.version, layered.version)
	assert.Equal(t, projectOnly.settings.UseDistilled, layered.settings.UseDistilled)
}
