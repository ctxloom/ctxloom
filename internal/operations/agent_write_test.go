package operations

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// loadConfigDir writes a minimal config.yaml under a fresh .ctxloom and loads it,
// returning the loaded config and the appDir. The write path (SetAgent/
// RemoveAgent) needs a real on-disk config to round-trip through Manager.Update.
// It is real-OS-fs (no config.WithFS), so HOME is isolated first: config.Load
// now also reads a home-layer config.yaml (D2/D3 layering), and this fixture
// must never pick up the developer's real ~/.ctxloom.
func loadConfigDir(t *testing.T, body string) (*config.Config, string) {
	t.Helper()
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0644))
	cfg, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	return cfg, appDir
}

// managerFor returns the Manager targeting the same on-disk appDir a
// loadConfigDir config was read from — the real read-modify-write transaction
// SetAgent/RemoveAgent/ScaffoldContainerBase now perform, not a stand-in.
func managerFor(appDir string) *config.Manager {
	return config.NewManager(config.WithAppDir(appDir))
}

// readAgentFromDisk re-reads appDir's config.yaml through ParseConfig (a
// single-document parse — no layering, no layerscope policy) and returns
// name's binding. Several fields SetAgent writes (Runtime is ScopeMachine —
// internal/config/layerscope) are no longer honored by a full config.Load
// against the committed PROJECT layer this test writes to (correctly: a
// committed project file is read by every clone, and whether THIS box has a
// container runtime is not a fact every clone shares). What THIS helper
// verifies is Save's own serialization fidelity — did SetAgent write the byte
// the caller asked for — independent of that load-time policy.
func readAgentFromDisk(t *testing.T, appDir, name string) (agents.Agent, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(appDir, "config.yaml"))
	require.NoError(t, err)
	cfg, err := config.ParseConfig(data)
	require.NoError(t, err)
	return cfg.Agent(name)
}

// TestSetAgent_RoundTripsThroughConfig proves the write half persists a
// binding under the `agents:` key and that a fresh load reads it back — the
// path the agent-assisted setup uses to record the user's choice.
func TestSetAgent_RoundTripsThroughConfig(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")

	entry, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{
		Name:     "finder",
		LLM:   ptr("claude-fast"),
		Profiles: ptr([]string{"p1", "p2"}),
	})
	require.NoError(t, err)
	assert.Equal(t, "finder", entry.Name)
	assert.Equal(t, agents.SourceConfig, entry.Source)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := reloaded.Agent("finder")
	require.True(t, ok, "set agent must survive a reload")
	assert.Equal(t, "claude-fast", sub.LLM)
	assert.Equal(t, []string{"p1", "p2"}, sub.Profiles)
}

// llmLabelsFixture declares a config-only LLM LABEL — a name that is NOT a
// registered backend — so the accepting-side assertion below does not depend on
// resources/default-config.yaml happening to ship one. Engine validation is a
// membership test over operations.AvailableLLMNames: registered backends UNION
// the labels the loaded config declares.
const llmLabelsFixture = `version: 5
llm:
  configs:
    claude-fast:
      type: claude-code
`

// TestSetAgent_RejectsUnknownEngine pins the binding-time engine check.
//
// `ctxloom agent edit dev --engine bogus-engine` used to exit 0 and WRITE the
// nonexistent engine into config.yaml: nothing validated the name at the moment
// it became the team's configuration, so the failure surfaced later and
// somewhere else, as whatever a missing engine happens to look like downstream.
// This is the same class as the silent-no-op bugs elsewhere in this codebase —
// a success message over a broken result.
//
// SetAgent is `agent create` and `agent edit`'s single shared body, so this one
// assertion covers both verbs; the CLI layer only differs in the
// name-already-taken precondition it enforces before calling.
func TestSetAgent_RejectsUnknownEngine(t *testing.T) {
	cfg, appDir := loadConfigDir(t, llmLabelsFixture)

	_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{
		Name:     "dev",
		LLM:   ptr("bogus-engine"),
		Profiles: ptr([]string{"default"}),
	})
	require.Error(t, err, "an engine no backend and no config label defines must be refused, not written")
	assert.Contains(t, err.Error(), "bogus-engine", "the refusal must name the value that was wrong")
	assert.Contains(t, err.Error(), "claude-code",
		"the refusal must list engines that DO exist — otherwise the user learns their spelling was wrong but not what the right spellings are")

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := reloaded.Agent("dev")
	assert.False(t, ok, "a rejected SetAgent call must persist nothing")
}

// TestSetAgent_UnknownEngineLeavesExistingBindingIntact is the edit half of the
// same defect, and the damaging one: the agent already exists and works, and a
// typo'd `--engine` used to overwrite a live binding with a broken value. The
// rejection has to happen before the write, not after it.
func TestSetAgent_UnknownEngineLeavesExistingBindingIntact(t *testing.T) {
	cfg, appDir := loadConfigDir(t, llmLabelsFixture)
	mgr := managerFor(appDir)

	_, err := SetAgent(mgr, cfg, SetAgentRequest{
		Name:     "dev",
		LLM:   ptr("claude-code"),
		Profiles: ptr([]string{"default"}),
	})
	require.NoError(t, err)
	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)

	_, err = SetAgent(mgr, reloaded, SetAgentRequest{Name: "dev", LLM: ptr("bogus-engine")})
	require.Error(t, err)

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := final.Agent("dev")
	require.True(t, ok, "the existing agent must survive a refused edit")
	assert.Equal(t, "claude-code", sub.LLM, "a refused edit must not corrupt the binding it was editing")
	assert.Equal(t, []string{"default"}, sub.Profiles)
}

// TestSetAgent_AcceptsBackendNamesAndConfigLabels pins the ACCEPTING side of
// the check, which is the half a too-narrow fix would break. An agent's engine
// is a LABEL resolved through resolveOneshotLabel/ResolveBackend, not a backend
// name: `ctxloom agent create finder --engine claude-fast` is in this command's
// own help examples and binds a label the config declares. Validating against
// the backend registry ALONE would reject the documented invocation, so the
// membership set has to be the same one `llm default` accepts and lists.
func TestSetAgent_AcceptsBackendNamesAndConfigLabels(t *testing.T) {
	_, appDir := loadConfigDir(t, llmLabelsFixture)
	mgr := managerFor(appDir)

	for _, engine := range []string{
		"claude-code", // a registered backend
		"claude-fast", // a config-declared label, no such backend
		"",            // explicit clear: fall back to the project default
	} {
		reloaded, err := config.Load(config.WithAppDir(appDir))
		require.NoError(t, err)
		_, err = SetAgent(mgr, reloaded, SetAgentRequest{
			Name:     "dev",
			LLM:   ptr(engine),
			Profiles: ptr([]string{"default"}),
		})
		require.NoErrorf(t, err, "engine %q must be accepted", engine)
	}
}

// TestSetAgent_PersistsRuntime proves the runtime axis written by
// `agent set --runtime` survives the SAVE round-trip (Marshal serializes it
// faithfully) — read back via readAgentFromDisk (ParseConfig, no layering),
// not a full config.Load: agents.*.runtime is ScopeMachine
// (internal/config/layerscope), so a committed PROJECT file — every clone's
// copy — no longer has this value take effect on a real Load; that closure is
// covered by internal/config's own layerscope tests. What this test still
// pins is that SetAgent itself writes the byte, never silently discarding it.
// An unknown value is stored as written (advisory warn only; it acts as host
// at resolve time per fault tolerance).
func TestSetAgent_PersistsRuntime(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	_, err := SetAgent(mgr, cfg, SetAgentRequest{
		Name:     "developer",
		LLM:   ptr("claude-code"),
		Profiles: ptr([]string{"default"}),
		Runtime:  ptr("container"),
	})
	require.NoError(t, err)

	sub, ok := readAgentFromDisk(t, appDir, "developer")
	require.True(t, ok)
	assert.Equal(t, "container", sub.Runtime)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)

	// Unknown value: stored verbatim, never an error.
	_, err = SetAgent(mgr, reloaded, SetAgentRequest{Name: "odd", Runtime: ptr("podracer")})
	require.NoError(t, err, "unknown runtime warns, never errors")
	sub, ok = readAgentFromDisk(t, appDir, "odd")
	require.True(t, ok)
	assert.Equal(t, "podracer", sub.Runtime, "stored as written")
}

// TestSetAgent_PersistsPermissions proves the permission posture written by
// `agent set --permissions` survives the config round-trip — a per-agent posture
// is the "configurable by agent" knob the run resolver consults. An unknown value
// is stored as written (advisory warn only; it resolves to the default posture).
func TestSetAgent_PersistsPermissions(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	_, err := SetAgent(mgr, cfg, SetAgentRequest{
		Name:        "planner",
		LLM:      ptr("claude-code"),
		Profiles:    ptr([]string{"default"}),
		Permissions: ptr("plan"),
	})
	require.NoError(t, err)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := reloaded.Agent("planner")
	require.True(t, ok)
	assert.Equal(t, "plan", sub.Permissions)

	// Unknown value: stored verbatim, never an error.
	_, err = SetAgent(mgr, reloaded, SetAgentRequest{Name: "odd", Permissions: ptr("wildwest")})
	require.NoError(t, err, "unknown permissions warns, never errors")
	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok = final.Agent("odd")
	require.True(t, ok)
	assert.Equal(t, "wildwest", sub.Permissions, "stored as written")
}

// TestSetAgent_PersistsDriving proves the driving axis written by
// `agent set --driving` survives the config round-trip. UNLIKE
// Runtime/Permissions above, an unknown value is REJECTED outright — SetAgent
// errors and nothing is persisted (agents.ValidateDriving's doc: a typo here
// changes execution semantics, so it never gets the advisory-warn treatment).
func TestSetAgent_PersistsDriving(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	_, err := SetAgent(mgr, cfg, SetAgentRequest{
		Name:     "shooter",
		LLM:   ptr("claude-code"),
		Profiles: ptr([]string{"default"}),
		Driving:  ptr("oneshot"),
	})
	require.NoError(t, err)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := reloaded.Agent("shooter")
	require.True(t, ok)
	assert.Equal(t, agents.DrivingOneshot, sub.Driving)

	// Unknown value: REJECTED — nothing written, unlike Runtime/Permissions.
	_, err = SetAgent(mgr, reloaded, SetAgentRequest{Name: "odd", Driving: ptr("wildwest")})
	require.Error(t, err, "unknown driving must be rejected, not stored")
	assert.Contains(t, err.Error(), "wildwest")
	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok = final.Agent("odd")
	assert.False(t, ok, "a rejected SetAgent call must persist nothing")
}

// TestSetAgent_UpdatesExisting proves a second set with the same name REPLACES
// the binding (whole-binding rewrite, not a merge).
func TestSetAgent_UpdatesExisting(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	// Real engine names, not "a"/"b" placeholders: SetAgent now validates the
	// engine against the known set, so a stand-in string no longer stands in.
	_, err := SetAgent(mgr, cfg, SetAgentRequest{Name: "dev", LLM: ptr("claude-code"), Profiles: ptr([]string{"x"})})
	require.NoError(t, err)
	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, err = SetAgent(mgr, reloaded, SetAgentRequest{Name: "dev", LLM: ptr("codex"), Profiles: ptr([]string{"y", "z"})})
	require.NoError(t, err)

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := final.Agent("dev")
	require.True(t, ok)
	assert.Equal(t, "codex", sub.LLM, "engine replaced")
	assert.Equal(t, []string{"y", "z"}, sub.Profiles, "profiles replaced, not unioned")
}

// TestGetAgent_SurfacesEscalation proves the approval-policy ladder —
// previously invisible from every read path (AgentEntry had no Escalation
// field) — is now visible via GetAgent/ListAgents, and survives a SetAgent
// write that does not name it (the merge landed on the write side; this
// pins the read half of the same fix).
func TestGetAgent_SurfacesEscalation(t *testing.T) {
	cfg, appDir := loadConfigDir(t, `version: 5
agents:
  dev:
    profiles: [x]
    escalation:
      - action: auto_accept
        kinds: [COMMAND_EXECUTION]
`)
	mgr := managerFor(appDir)

	entry, err := GetAgent(cfg, "dev")
	require.NoError(t, err)
	require.Len(t, entry.Escalation, 1, "escalation ladder must be readable, not invisible")
	assert.Equal(t, "auto_accept", entry.Escalation[0].Action)

	list := ListAgents(cfg)
	require.Len(t, list, 1)
	require.Len(t, list[0].Escalation, 1, "ListAgents must surface it too")

	// A write that doesn't name Escalation must not wipe it.
	_, err = SetAgent(mgr, cfg, SetAgentRequest{Name: "dev", Runtime: ptr("container")})
	require.NoError(t, err)
	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	entry, err = GetAgent(reloaded, "dev")
	require.NoError(t, err)
	assert.Len(t, entry.Escalation, 1, "escalation must survive an unrelated field write")
}

// TestSetAgent_EmptyName errors rather than writing a nameless binding.
func TestSetAgent_EmptyName(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{Name: "", Profiles: ptr([]string{"p"})})
	assert.Error(t, err)
}

// TestRemoveAgent_RoundTrips proves remove deletes the config-key entry and
// persists the removal.
func TestRemoveAgent_RoundTrips(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)
	_, err := SetAgent(mgr, cfg, SetAgentRequest{Name: "finder", Profiles: ptr([]string{"p1"})})
	require.NoError(t, err)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	require.NoError(t, RemoveAgent(mgr, reloaded, "finder"))

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := final.Agent("finder")
	assert.False(t, ok, "removed agent must be gone after reload")
}

// TestRemoveAgent_NotFound errors on an unknown name.
func TestRemoveAgent_NotFound(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	assert.Error(t, RemoveAgent(managerFor(appDir), cfg, "nope"))
}

// TestRemoveAgent_DirectorySourceRefused proves a directory-defined agent
// is NOT removable via the config-key write path (it is its own file): remove
// errors with a clear pointer rather than silently no-op'ing.
func TestRemoveAgent_DirectorySourceRefused(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	writeFile(t, filepath.Join(appDir, "agents", "filed.yaml"), "llm: x\nprofiles: [p]\n")

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := reloaded.Agent("filed")
	require.True(t, ok, "directory agent should be visible")

	err = RemoveAgent(managerFor(appDir), reloaded, "filed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filed.yaml", "error must point at the file to delete")
}

// TestSetAgent_ConcurrentWritesAllSurvive proves the migrated write path: N
// goroutines each SetAgent-ing a DISTINCT name against the SAME on-disk
// project all survive. This is the concrete behavioural gain of routing
// through Manager.Update — under the old cfg.Load()-mutate-cfg.Save()
// bridge, each goroutine's Save() would persist only the agents its own,
// possibly-stale Load() had seen, so concurrent writers routinely clobbered
// one another. Manager.Update's fresh reload happens INSIDE the lock, so
// every writer's change survives regardless of interleaving.
func TestSetAgent_ConcurrentWritesAllSurvive(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = SetAgent(mgr, cfg, SetAgentRequest{
				Name:     fmt.Sprintf("agent-%02d", i),
				Profiles: ptr([]string{"p"}),
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoErrorf(t, err, "writer %d", i)
	}

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("agent-%02d", i)
		_, ok := final.Agent(name)
		assert.Truef(t, ok, "%s must survive concurrent SetAgent calls — a lost write means the mutation escaped Manager.Update's locked transaction", name)
	}
}

// --- the SessionStart detection nudge ------------------------------------

// TestAgentSetupNudge_FiresOnlyWhenProfilesPresentNoAgents pins the exact
// trigger condition: profiles present AND zero agents → nudge; any other
// combination → silent.
func TestAgentSetupNudge_FiresOnlyWhenProfilesPresentNoAgents(t *testing.T) {
	appDir := func() string { return filepath.Join(t.TempDir(), ".ctxloom") }

	t.Run("profiles present, no agents → nudge", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{
			AppPaths: []string{appDir()},
			Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{"default": {}}},
		})
		assert.NotEmpty(t, AgentSetupNudge(cfg))
	})

	t.Run("profiles present, agent configured → silent", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{
			AppPaths: []string{appDir()},
			Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{"default": {}}},
			Agents:   map[string]agents.Agent{"dev": {Profiles: []string{"default"}}},
		})
		assert.Empty(t, AgentSetupNudge(cfg), "any agent silences the nudge")
	})

	t.Run("no profiles, no agents → silent", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir()}})
		assert.Empty(t, AgentSetupNudge(cfg), "nothing to bind → no nudge")
	})

	t.Run("nil config → silent", func(t *testing.T) {
		assert.Empty(t, AgentSetupNudge(nil))
	})
}

// TestAgentSetupNudge_InlineDefinitionsCountAsProfiles proves the
// profiles-present check also counts inline profile definitions (not just
// defaults), so a project that configured profiles inline still gets nudged.
func TestAgentSetupNudge_InlineDefinitionsCountAsProfiles(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{filepath.Join(t.TempDir(), ".ctxloom")},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{"x": {}},
		},
	})
	assert.NotEmpty(t, AgentSetupNudge(cfg))
}

// TestSetAgent_PersistsTheSurfacePreference is a REGRESSION on a defect that
// shipped for real in this feature's first draft: the preference was parsed by
// the CLI, validated by SetAgent — and then never copied onto the stored entry.
// `agent create writer --engine claude-code --surface context=system-prompt`
// reported "Created agent" and wrote a binding with no surfaces key at all.
//
// That is this project's characteristic shape wearing new clothes: a flag
// accepted, a success line printed, and nothing recorded. Only an assertion on
// what RELOADS can catch it — the create call's own return value carried the
// engine correctly and would have looked fine.
func TestSetAgent_PersistsTheSurfacePreference(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")

	_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{
		Name:     "writer",
		LLM:   ptr("claude-code"),
		Surfaces: map[string]string{"context": "system-prompt"},
	})
	require.NoError(t, err)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := reloaded.Agent("writer")
	require.True(t, ok)
	assert.Equal(t, map[string]string{"context": "system-prompt"}, sub.Surfaces,
		"the preference must survive the write and a reload, not just the validation")
}

// A preference the engine cannot honour is refused, and the refusal must leave
// NOTHING behind — the whole point of validating before the transaction opens.
func TestSetAgent_RefusedSurfacePreferenceWritesNothing(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")

	_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{
		Name:     "scout",
		LLM:   ptr("kiro"),
		Surfaces: map[string]string{"context": "system-prompt"},
	})
	require.Error(t, err, "system-prompt is claude-only; kiro must refuse it")

	reloaded, rerr := config.Load(config.WithAppDir(appDir))
	require.NoError(t, rerr)
	_, ok := reloaded.Agent("scout")
	assert.False(t, ok, "a refused write must not half-apply a binding")
}
