package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ===== Escalation path 1: env cannot grant dirty_tree_commit_ack =============
//
// The key no longer exists in the schema at all (it moved to
// paths.DirtyTreeCommitAckPath), so this is a STRUCTURAL closure, not merely
// a ScopeAllows verdict — proving it requires showing the value never reaches
// cfg no matter how it is injected, and that DirtyTreeCommitAcknowledged is
// wired to the state store alone, never Load's result.

func TestLoad_EscalationPath1_EnvCannotGrantDirtyTreeCommitAck(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("version: 1\n"), 0644))

	overrides := confload.Overrides{Env: map[string]any{"DIRTY_TREE_COMMIT_ACK": true}}
	cfg, err := Load(WithFS(fs), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	// MUTATION TARGET: if dirty_tree_commit_ack were still a live schema/
	// struct field, this would be true.
	assert.False(t, DirtyTreeCommitAcknowledged(fs, appDir),
		"an env override must never grant the dirty-tree-commit acknowledgement — it is not even a config key any longer")
	// And the merged config must never have decoded a stray value onto
	// anything an accessor could reach; GetDirtyTreeHandler is untouched,
	// proving the env override didn't corrupt an unrelated sibling key either.
	assert.Equal(t, "", cfg.GetDirtyTreeHandler())
}

func TestLoad_EscalationPath1_ConfigSetCannotGrantDirtyTreeCommitAck(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("version: 1\n"), 0644))

	overrides := confload.Overrides{Flags: map[string]any{"dirty_tree_commit_ack": true}}
	_, err := Load(WithFS(fs), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	assert.False(t, DirtyTreeCommitAcknowledged(fs, appDir),
		"--config-set must never grant the dirty-tree-commit acknowledgement either")
}

// ===== Escalation path 2: an env var cannot mint a privileged agent =========

func TestLoad_EscalationPath2_EnvCannotMintPrivilegedAgent(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	// The project declares NO agents at all -- the measured case.
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("version: 1\n"), 0644))

	overrides := confload.Overrides{Env: map[string]any{
		"AGENTS_EVIL_PERMISSIONS": "bypass",
	}}
	cfg, err := Load(WithFS(fs), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	// MUTATION TARGET: with ScopeAllows/agentBindingMergeFunc removed, this
	// would be a real, permission-bypassing "evil" agent.
	_, exists := cfg.agents["evil"]
	assert.False(t, exists, "an env var must not be able to mint a brand-new agent binding at all")

	foundLayerScopeWarning := false
	for _, w := range cfg.warnings {
		if w.Kind == WarnKindLayerScope {
			foundLayerScopeWarning = true
		}
	}
	assert.True(t, foundLayerScopeWarning, "the drop must be reported as a WarnKindLayerScope finding, not silent")
}

// TestLoad_ConfigSetCanStillMintAPrivilegedAgent_ByDesign is the deliberate
// NON-goal alongside path 2: ScopeShared keeps --config-set reach (decision 6
// / Scope's own doc) because a flag is scoped to ONE invocation, auditable in
// a process listing, and never inherited by a spawned child -- unlike env,
// which every child inherits. An operator typing --config-set on their own
// invocation is exactly the flag's purpose, so this must still work; only the
// AMBIENT env channel is closed.
func TestLoad_ConfigSetCanStillMintAPrivilegedAgent_ByDesign(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("version: 1\n"), 0644))

	overrides := confload.Overrides{Flags: map[string]any{
		"agents.evil.permissions": "bypass",
	}}
	cfg, err := Load(WithFS(fs), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	evil, exists := cfg.agents["evil"]
	require.True(t, exists, "--config-set targets ScopeShared, which explicitly keeps flag reach")
	assert.Equal(t, "bypass", evil.Permissions)
}

// ===== Escalation path 3: home cannot escalate a project's same-named agent =

func TestLoad_EscalationPath3_HomeCannotEscalateProjectAgent(t *testing.T) {
	home := testsupport.Isolate(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ctxloom"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ctxloom", "config.yaml"), []byte(`version: 1
agents:
  reviewer:
    permissions: bypass
    runtime: container
`), 0o644))

	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(`version: 1
agents:
  reviewer:
    profiles: [default]
`), 0644))

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	reviewer, ok := cfg.agents["reviewer"]
	require.True(t, ok, "the project's own agent must still be present")

	// MUTATION TARGET: with agentBindingMergeFunc removed (falling back to
	// koanf's default deep merge), these two would leak in from home.
	assert.Equal(t, "", reviewer.Permissions, "home must not be able to grant a permission bypass to the project's agent")
	assert.Equal(t, "", reviewer.Runtime, "home's runtime must not leak in either -- the whole binding comes from the layer that named it")
	assert.Equal(t, []string{"default"}, reviewer.Profiles, "the project's own field must survive untouched")

	foundLayerScopeWarning := false
	for _, w := range cfg.warnings {
		if w.Kind == WarnKindLayerScope {
			foundLayerScopeWarning = true
		}
	}
	assert.True(t, foundLayerScopeWarning, "home's disallowed per-key fields must still be reported")
}

// TestAgentBindingMergeFunc_AgentNamedOnlyByLowerLayerSurvives proves the
// MERGE mechanism's own half of decision 3's trade-off, isolated from the
// per-key scope check (an orthogonal, separately-tested closure): an agent
// the higher layer doesn't mention at all must still come through from the
// lower layer untouched at the merge step -- "whichever layer names the
// agent wins" is not "a lower layer's agent is wiped just because a higher
// layer exists".
//
// This is a merge-func-level test, not a full config.Load one, because
// EVERY per-agent leaf the schema declares is ScopeShared as of this change
// (agents.*.runtime included -- see policy_default.go's divergence comment
// on that rule): a real end-to-end Load can no longer demonstrate "an agent
// declared only in home survives WITH CONTENT" through config.yaml's agents:
// block, because every one of its fields is dropped by the per-key check
// regardless of which layer carries it. That is a SEPARATE, deliberate
// closure (home may never define ANY project-shared agent field, full
// stop) documented by TestLoad_EscalationPath3_HomeCannotEscalateProjectAgent
// and TestScopeAllows_UsesSharedPolicyTable; it does not mean the MERGE rule
// this test pins is untrue or untested — only that proving it end-to-end
// needs a hypothetical schema field the real one no longer has.
func TestAgentBindingMergeFunc_AgentNamedOnlyByLowerLayerSurvives(t *testing.T) {
	dest := map[string]any{
		"agents": map[string]any{
			"personal": map[string]any{"profiles": []any{"dev"}},
		},
	}
	src := map[string]any{
		"agents": map[string]any{
			"other": map[string]any{"profiles": []any{"default"}},
		},
	}
	require.NoError(t, agentBindingMergeFunc(src, dest))

	agents := dest["agents"].(map[string]any)
	require.Contains(t, agents, "personal", "an agent the higher layer never names must survive the merge untouched")
	assert.Equal(t, map[string]any{"profiles": []any{"dev"}}, agents["personal"])
	require.Contains(t, agents, "other")
}

// TestScopeAllows_UsesSharedPolicyTable is a narrow unit test of ctxloom's own
// confload.Product.ScopeAllows hook, independent of a full Load: it must
// consult the SAME layerscope.DefaultPolicy() the file-layer check uses (a
// dedicated, drifted copy would be the exact class of bug this design closes).
func TestScopeAllows_UsesSharedPolicyTable(t *testing.T) {
	ok, why := scopeAllows(confload.SourceEnv, []string{"agents", "reviewer", "permissions"})
	assert.False(t, ok)
	assert.NotEmpty(t, why)

	ok, _ = scopeAllows(confload.SourceEnv, []string{"delegation", "concurrency"})
	assert.True(t, ok, "delegation.concurrency is ScopeMachine, which env is allowed to set")

	ok, _ = scopeAllows(confload.SourceFlag, []string{"agents", "reviewer", "permissions"})
	assert.True(t, ok, "agents.*.permissions is ScopeShared, which --config-set is allowed to set")

	// A path with no policy opinion at all must be permissive (unknown-key
	// handling is separate machinery this hook must not duplicate).
	ok, why = scopeAllows(confload.SourceEnv, []string{"totally_unrecognized_key"})
	assert.True(t, ok)
	assert.Empty(t, why)
}

// TestAgentBindingMergeFunc_ReplacesWholesale is a narrow unit test of
// ctxloom's own merge func, independent of the full Load pipeline.
func TestAgentBindingMergeFunc_ReplacesWholesale(t *testing.T) {
	dest := map[string]any{
		"agents": map[string]any{
			"reviewer": map[string]any{"permissions": "bypass", "coordinator": true, "runtime": "container"},
		},
	}
	src := map[string]any{
		"agents": map[string]any{
			"reviewer": map[string]any{"profiles": []any{"default"}},
		},
	}
	require.NoError(t, agentBindingMergeFunc(src, dest))

	agents := dest["agents"].(map[string]any)
	reviewer := agents["reviewer"].(map[string]any)
	assert.Equal(t, map[string]any{"profiles": []any{"default"}}, reviewer)
}

func TestAgentBindingMergeFunc_NonAgentKeysStillDeepMerge(t *testing.T) {
	dest := map[string]any{"editor": map[string]any{"command": "vim", "args": []any{"-p"}}}
	src := map[string]any{"editor": map[string]any{"command": "nano"}}
	require.NoError(t, agentBindingMergeFunc(src, dest))

	editor := dest["editor"].(map[string]any)
	assert.Equal(t, "nano", editor["command"], "the higher layer's value must win")
	assert.Equal(t, []any{"-p"}, editor["args"], "an untouched sibling field must survive (deep merge, not replace)")
}

// TestLoad_ConfigSetPatchesOneAgentFieldWithoutWipingSiblings is the
// end-to-end regression test for a bug MEASURED against a running binary
// while building this seam: --config-set targeting ONE field of an agent the
// project already declares used to wipe out every OTHER field of that same
// agent (profiles, engine, ...), because ApplyOverrides used to merge the
// flag layer through the SAME atomic-replace-aware path (agentBindingMergeFunc)
// Load's file-layer merge uses — the flag's one-field patch "named" the
// agent, so agentBindingMergeFunc replaced the WHOLE binding with just that
// field. Fixed by confload.ApplyOverrides always resolving overrides through
// the package's plain Merge, never a Product's own MergeFunc (see
// internal/shared/confload's TestApplyOverrides_FlagOverride_
// NeverGoesThroughProductMergeFunc for the generic, product-agnostic proof).
func TestLoad_ConfigSetPatchesOneAgentFieldWithoutWipingSiblings(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(`version: 1
agents:
  reviewer:
    profiles: [default]
    llm: claude-code
`), 0644))

	overrides := confload.Overrides{Flags: map[string]any{"agents.reviewer.permissions": "bypass"}}
	cfg, err := Load(WithFS(fs), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	reviewer, ok := cfg.agents["reviewer"]
	require.True(t, ok)
	assert.Equal(t, "bypass", reviewer.Permissions, "the override itself must still apply")
	assert.Equal(t, []string{"default"}, reviewer.Profiles, "a sibling field the override never touched must survive")
	assert.Equal(t, "claude-code", reviewer.LLM, "same for a second untouched sibling field")
}

// ===== saveLocked must not leak a home-inherited Machine value into the ====
// ===== committed project file ===============================================

// TestManagerUpdate_DoesNotPersistHomeInheritedMachineValueIntoProjectFile
// pins the SAVE-time half of the layer-scope closure, distinct from every
// other test in this file (all LOAD-time): Manager.Update's draft is built
// from loadUncached's FULLY MERGED view (home < project), so an unrelated
// write (setting default_agent here) must not silently duplicate home's own
// editor.command -- ScopeMachine, legitimate in home, never in a committed
// project file -- into the persisted project config.yaml. Left unfixed, the
// NEXT load of that same file rediscovers the leaked value at LayerProject
// and (FATAL-class strictness) refuses to start entirely: a command with
// nothing to do with the editor would brick every subsequent strict-mode
// invocation merely by having run once.
func TestManagerUpdate_DoesNotPersistHomeInheritedMachineValueIntoProjectFile(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(filepath.Join(home, ".ctxloom")), []byte("version: 1\neditor:\n  command: vim\n"), 0644))

	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("version: 1\n"), 0644))

	mgr := NewManager(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, mgr.Update(func(d *Draft) error {
		d.DefaultAgent = "reviewer" // any write unrelated to the editor
		return nil
	}))

	// MUTATION TARGET: read the raw PROJECT file (ParseConfig, no layering,
	// no merge) -- if saveLocked's scope filter is skipped/disabled, home's
	// editor.command shows up here, persisted into the committed project
	// file by a write that never touched the editor at all.
	data, err := afero.ReadFile(fs, paths.ConfigPath(appDir))
	require.NoError(t, err)
	persisted, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, "", persisted.editor.Command, "home's Machine-scoped editor.command must never be written into the committed project file")

	// The unrelated write must still have landed -- this filter must not
	// swallow legitimate project-scope content along with what it drops.
	assert.Equal(t, "reviewer", persisted.defaultAgent)
}
