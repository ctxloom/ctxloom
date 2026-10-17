package fsstatic_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

const sweepUsersHook = "echo mine"

// trackedClaudeSettings delivers claude's hooks and settings — ctxloom's
// callbacks and the HUD statusline — into a project under a record this test
// then discards, and adds a hook of the user's: the settings file a
// repository tracks, as a clone carries it onto a machine whose record has
// never seen it.
func trackedClaudeSettings(t *testing.T, fs afero.Fs, eng engine.Engine) string {
	t.Helper()
	project := t.TempDir()
	pkg := compositetest.Fixture(t)
	pkg.Hooks.Unified.SessionStart = []wire.Hook{agent.NewSessionStartHook()}
	pkg.Hooks.Unified.TurnEnd = []wire.Hook{agent.NewNextStepHook()}
	pkg.Statusline = true
	items := pkg.EngineItems(eng.Root().Name)
	exports, err := eng.Exports(items)
	require.NoError(t, err)
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{present.Hooks: present.RootProjectRoot, present.Settings: present.RootProjectRoot}}
	plan, err := delivery.Route(items, eng.Root(), pref, present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}})
	require.NoError(t, err)
	elsewhere, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	_, err = fsstatic.New(safefs.NewMem(fs)).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}, eng.Root(),
		delivery.Target{Root: present.ProjectOnHost(project), Ownership: elsewhere, Writer: delivery.ProjectWriterFor("claude-code")})
	require.NoError(t, err)

	doc := claudeSettings(t, project)
	require.Contains(t, doc, "statusLine", "the delivery wrote the HUD statusline")
	hooks := doc["hooks"].(map[string]any)
	require.Contains(t, hooks, "SessionStart", "the delivery wrote ctxloom's callbacks")
	hooks["PreToolUse"] = []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": sweepUsersHook}}}}
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(claude.ProjectSettingsPath(project), data, 0o644))
	return project
}

func claudeSettings(t *testing.T, project string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(claude.ProjectSettingsPath(project))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc
}

// releaseUnrecorded is the empty plan for kinds (nil: every kind) under a
// record that has never seen the project.
func releaseUnrecorded(t *testing.T, fs afero.Fs, eng engine.Engine, project string, kinds []present.Kind) {
	t.Helper()
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	_, err = fsstatic.New(safefs.NewMem(fs)).Deliver(context.Background(), delivery.Loadout{Package: compositetest.Fixture(t)}, eng.Root(),
		delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriterFor("claude-code"), Kinds: kinds})
	require.NoError(t, err)
}

// A release with no record (a fresh clone's `manage uninstall`) sweeps the
// tracked settings file the hooks and settings approaches name: ctxloom's
// callbacks and HUD statusline leave, the user's hook stays.
func TestDeliver_AReleaseWithNoRecordSweepsTheTrackedSettings(t *testing.T) {
	fs := afero.NewOsFs()
	eng, err := claude.Build()
	require.NoError(t, err)
	project := trackedClaudeSettings(t, fs, eng)

	releaseUnrecorded(t, fs, eng, project, nil)

	doc := claudeSettings(t, project)
	assert.NotContains(t, doc, "statusLine")
	assert.Equal(t, map[string]any{
		"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": sweepUsersHook}}}},
	}, doc["hooks"])
}

// A release of only kinds the sweep does not cover (`--release --surface
// context`) leaves the settings file as it was: a sweep is the hooks' and
// the settings' release, never another kind's.
func TestDeliver_AReleaseOfAnotherKindDoesNotSweep(t *testing.T) {
	fs := afero.NewOsFs()
	eng, err := claude.Build()
	require.NoError(t, err)
	project := trackedClaudeSettings(t, fs, eng)
	before, err := os.ReadFile(claude.ProjectSettingsPath(project))
	require.NoError(t, err)

	releaseUnrecorded(t, fs, eng, project, []present.Kind{present.Context})

	after, err := os.ReadFile(claude.ProjectSettingsPath(project))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

// The sweep is either kind's release: releasing only the settings (or only
// the hooks) sweeps the file both name, so a partial uninstall still takes
// ctxloom's callbacks out of a clone's settings.
func TestDeliver_AReleaseOfOneSweptKindSweeps(t *testing.T) {
	for _, k := range []present.Kind{present.Settings, present.Hooks} {
		t.Run(k.String(), func(t *testing.T) {
			fs := afero.NewOsFs()
			eng, err := claude.Build()
			require.NoError(t, err)
			project := trackedClaudeSettings(t, fs, eng)

			releaseUnrecorded(t, fs, eng, project, []present.Kind{k})

			doc := claudeSettings(t, project)
			assert.NotContains(t, doc, "statusLine")
			assert.NotContains(t, doc["hooks"], "SessionStart")
		})
	}
}
