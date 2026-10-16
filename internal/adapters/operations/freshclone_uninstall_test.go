package operations

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A FRESH CLONE of a repository that tracks .claude/settings.json carries
// ctxloom's hooks and statusline in the file but has no ownership record on
// this machine: the record is home-rooted (paths.HomeRecordsDir). Uninstall
// (Release at the at-rest placement, what `manage uninstall` runs) must still
// take out what ctxloom writes by construction, and leave everything else.

const (
	freshCloneDir       = "/clone"
	freshUserHook       = "echo user-hook"
	freshUserCtxloom    = "ctxloom profile list"
	freshCompanionHook  = "ltk evaluate"
	freshUnrelatedKey   = "worktree"
	freshUnrelatedValue = `{"baseRef":"head"}`
)

// byConstructionHooks is every machine callback ctxloom's own constructors
// build (managedhooks.appendManagedDynamicHooks), on the unified event
// ctxloom installs each under.
func byConstructionHooks() wire.UnifiedHooks {
	return wire.UnifiedHooks{
		SessionStart: []wire.Hook{agent.NewSessionStartHook()},
		PostTool:     []wire.Hook{agent.NewSkillMatesHook(), agent.NewMailPeekHook()},
		TurnEnd:      []wire.Hook{agent.NewNextStepHook()},
		TurnStart:    []wire.Hook{agent.NewMailDrainHook()},
	}
}

// byConstructionVerbs is the `hook <verb>` of every by-construction callback:
// the delivered entry carries engine-specific arguments after it, so the verb,
// not the constructor's whole argument list, identifies it in the file.
func byConstructionVerbs() []string {
	h := byConstructionHooks()
	var out []string
	for _, set := range [][]wire.Hook{h.SessionStart, h.PostTool, h.TurnEnd, h.TurnStart} {
		for _, hook := range set {
			out = append(out, strings.Join(hook.Args[:2], " "))
		}
	}
	return out
}

func freshClonePackage() composite.Package {
	return composite.Package{Hooks: wire.HooksConfig{Unified: byConstructionHooks()}, Statusline: true}
}

// isolateRecords points the home-rooted ownership record at a directory no
// other test shares, so "no record" is a fact of this test and not of order.
func isolateRecords(t *testing.T) {
	t.Helper()
	t.Cleanup(paths.SetHomeRecordsDirForTesting(filepath.Join(t.TempDir(), "records")))
}

func deliverFresh(t *testing.T, fs afero.Fs) {
	t.Helper()
	kind := lookupEngine(t, "claude-code")
	_, _, err := Deliver(context.Background(), safefs.NewMem(fs), kind, freshClonePackage(), delivery.Loadout{}, atRestPlacement(freshCloneDir, kind.Root().Name, delivery.AllKinds()))
	require.NoError(t, err)
}

func releaseFresh(t *testing.T, fs afero.Fs) {
	t.Helper()
	kind := lookupEngine(t, "claude-code")
	require.NoError(t, Release(context.Background(), safefs.NewMem(fs), kind, atRestPlacement(freshCloneDir, kind.Root().Name, delivery.AllKinds())))
}

// seedFreshClone writes, into a project with no record, exactly the
// settings.json an at-rest Deliver writes for the by-construction package,
// plus a user hook, a user hook that invokes the ctxloom binary for its own
// purposes, a companion-style hook and an unrelated user key.
func seedFreshClone(t *testing.T, fs afero.Fs) {
	t.Helper()
	isolateRecords(t)
	src := afero.NewMemMapFs()
	deliverFresh(t, src)
	settingsPath := claude.ProjectSettingsPath(freshCloneDir)
	data, err := afero.ReadFile(src, settingsPath)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	require.Contains(t, doc, "statusLine", "the at-rest delivery wrote the managed statusline")
	_, seeded := callbacksIn(hookEntries(doc))
	require.ElementsMatch(t, byConstructionVerbs(), seeded, "the at-rest delivery wrote every by-construction callback")
	hooks := doc["hooks"].(map[string]any)
	hooks["PreToolUse"] = []any{map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{"type": "command", "command": freshCompanionHook},
			map[string]any{"type": "command", "command": freshUserHook},
		},
	}}
	hooks["SessionEnd"] = []any{map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": freshUserCtxloom}},
	}}
	var unrelated any
	require.NoError(t, json.Unmarshal([]byte(freshUnrelatedValue), &unrelated))
	doc[freshUnrelatedKey] = unrelated
	out, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)

	// The fresh clone: the file, and a record directory that has never seen it.
	isolateRecords(t)
	testsupport.WriteFile(t, fs, settingsPath, out, 0o644)
}

type freshHook struct {
	command string
	args    []string
}

// freshHooks is the settings file and every hook entry in it.
func freshHooks(t *testing.T, fs afero.Fs) (map[string]any, []freshHook) {
	t.Helper()
	data, err := afero.ReadFile(fs, claude.ProjectSettingsPath(freshCloneDir))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc, hookEntries(doc)
}

// hookEntries is every hook entry in a claude settings document.
func hookEntries(doc map[string]any) []freshHook {
	var out []freshHook
	hooks, _ := doc["hooks"].(map[string]any)
	for _, matchers := range hooks {
		for _, m := range matchers.([]any) {
			for _, h := range m.(map[string]any)["hooks"].([]any) {
				e := h.(map[string]any)
				fh := freshHook{command: e["command"].(string)}
				for _, a := range asSlice(e["args"]) {
					fh.args = append(fh.args, a.(string))
				}
				out = append(out, fh)
			}
		}
	}
	return out
}

// callbacksIn is every entry's command, and the `hook <verb>` of each entry
// that runs ctxloom's executable as a machine callback.
func callbacksIn(hooks []freshHook) (commands, callbacks []string) {
	for _, h := range hooks {
		commands = append(commands, h.command)
		if h.command == agent.CtxloomCommand() && len(h.args) >= 2 && h.args[0] == "hook" {
			callbacks = append(callbacks, strings.Join(h.args[:2], " "))
		}
	}
	return commands, callbacks
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func assertReclaimed(t *testing.T, fs afero.Fs) {
	t.Helper()
	doc, hooks := freshHooks(t, fs)
	commands, callbacks := callbacksIn(hooks)
	for _, verb := range byConstructionVerbs() {
		assert.NotContains(t, callbacks, verb, "a machine callback ctxloom constructs survived uninstall")
	}
	assert.NotContains(t, doc, "statusLine", "the managed statusline survived uninstall")
	assert.Contains(t, commands, freshUserHook)
	assert.Contains(t, commands, freshUserCtxloom, "a user's own ctxloom invocation is not a machine callback")
	assert.Contains(t, commands, freshCompanionHook, "a companion hook cannot be recognised without a record and stays")
	unrelated, err := json.Marshal(doc[freshUnrelatedKey])
	require.NoError(t, err)
	assert.JSONEq(t, freshUnrelatedValue, string(unrelated))
}

func TestRelease_FreshClone_ReclaimsCtxloomHooks(t *testing.T) {
	fs := afero.NewMemMapFs()
	seedFreshClone(t, fs)

	releaseFresh(t, fs)

	assertReclaimed(t, fs)
}

func TestDeliverThenRelease_FreshClone_ReclaimsCtxloomHooks(t *testing.T) {
	fs := afero.NewMemMapFs()
	seedFreshClone(t, fs)

	deliverFresh(t, fs)
	releaseFresh(t, fs)

	assertReclaimed(t, fs)
}

// check and uninstall read one rule: what `manage check` (HarnessStatus)
// reports as wired is exactly what `manage uninstall` takes out.
func TestHarnessStatus_FreshClone_AgreesWithUninstall(t *testing.T) {
	fs := afero.NewMemMapFs()
	seedFreshClone(t, fs)
	status := func() BackendWiring {
		res, err := HarnessStatus(context.Background(), engines.Registry(), &config.Config{}, HarnessStatusRequest{FS: fs, WorkDir: freshCloneDir})
		require.NoError(t, err)
		return backendWiring(t, res, "claude-code")
	}

	before := status()
	assert.True(t, before.HooksPresent)
	assert.True(t, before.StatusLine)

	releaseFresh(t, fs)

	after := status()
	assert.False(t, after.HooksPresent, "check reports hooks that uninstall left as the user's")
	assert.False(t, after.StatusLine)
}
