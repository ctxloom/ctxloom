package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/exectoken"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// ctxloomOwnHooks is every hook ctxloom constructs for ITSELF — the ones that
// invoke its own `hook` verbs rather than being read out of a bundle. They are
// built through the real constructors so a verb that moves, or a new one that
// is added, changes what these tests exercise without anyone editing a list
// here.
func ctxloomOwnHooks() []wire.Hook {
	return []wire.Hook{
		agent.NewContextInjectionHook("abc123"),
		agent.NewContextInjectionChunkHook("abc123", 1, 2),
		agent.NewContextInjectionChunkHook("abc123", 2, 2),
		agent.NewToolReflectHook(agent.DefaultToolReflectBytes),
		agent.NewNextStepHook(),
		agent.NewSkillMatesHook(),
	}
}

// fullManagedHookSet is the shape apply-hooks assembles for claude-code: ctxloom's
// own callbacks, the bundle-shipped ctxloom hooks, and a companion binary's hook.
// The companion is the case only the ledger can own (no name rule ever matches
// it); the bundle hooks are the case the name fallback was written for.
func fullManagedHookSet() *wire.HooksConfig {
	own := ctxloomOwnHooks()
	return &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: append([]wire.Hook{{Command: "ctxloom hook session-bind"}}, own[0], own[1], own[2]),
		PostTool:     []wire.Hook{{Command: "ctxloom hook stamp-plan"}, own[3]},
		TurnEnd:      []wire.Hook{own[4]},
		PreTool:      []wire.Hook{{Command: "ltk evaluate", Matcher: "Bash"}},
	}}
}

func hooksLedger(projectDir string) ledger.Ledger {
	return ledger.Ledger{FS: (&ClaudeCodeHookWriter{}).getFS(), Dir: filepath.Join(projectDir, ".claude")}
}

func digestsOf(commands []string) []string {
	out := make([]string, 0, len(commands))
	for _, c := range commands {
		out = append(out, agent.ComputeCommandDigest(c))
	}
	return out
}

// TestWriteSettings_EveryHookWrittenIsInTheLedger pins the ledger's one job:
// ownership is RECORDED at write time for every entry, not inferred later from
// the file's contents. The assertion is over the ledger's CONTENT against what
// is actually in settings.json — a write that reports success and records a
// subset is the silent no-op that let two of ctxloom's own hooks survive an
// uninstall as if a user had written them.
func TestWriteSettings_EveryHookWrittenIsInTheLedger(t *testing.T) {
	dir := t.TempDir()
	w := &ClaudeCodeHookWriter{}
	require.NoError(t, w.WriteSettings(fullManagedHookSet(), ctxloomBundleMCP(), dir))

	written := hookCommands(t, dir)
	require.Len(t, written, 8, "precondition: the whole assembled set reached the file")

	recorded, err := hooksLedger(dir).Read(ledger.SurfaceHooks)
	require.NoError(t, err)
	assert.ElementsMatch(t, digestsOf(written), recorded,
		"the hooks surface must name exactly the hooks in the file — every one ctxloom wrote, nothing it did not")

	status, err := hooksLedger(dir).Read(ledger.SurfaceStatusLine)
	require.NoError(t, err)
	assert.Equal(t, []string{agent.ComputeCommandDigest(ctxloomStatusLineCommand())}, status,
		"the statusline ctxloom installed must be claimed by digest")
}

// TestRemoveSettings_ThenWriteSettings_IsIdentity is the round trip the ledger
// exists to make safe: uninstall returns the file to what the user had, and a
// re-apply returns it to what the first apply produced — asserted on the BYTES
// of both settings.json and the ledger, not on counts.
//
// The user's hook and the user's unrelated key are the controls: without them
// "returned to prior content" cannot be told apart from "emptied the file".
func TestRemoveSettings_ThenWriteSettings_IsIdentity(t *testing.T) {
	dir := t.TempDir()
	w := &ClaudeCodeHookWriter{}
	settingsPath := w.SettingsPath(dir)
	const userHook = "./scripts/my-own-hook.sh"
	seed := `{
  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "` + userHook + `"}]}]},
  "worktree": {"baseRef": "head"}
}
`
	require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o755))
	require.NoError(t, os.WriteFile(settingsPath, []byte(seed), 0o644))

	require.NoError(t, w.WriteSettings(fullManagedHookSet(), ctxloomBundleMCP(), dir))
	applied, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	appliedLedger, err := os.ReadFile(hooksLedger(dir).Path())
	require.NoError(t, err)

	require.NoError(t, w.RemoveSettings(dir))
	assert.Equal(t, []string{userHook}, hookCommands(t, dir),
		"uninstall must leave exactly the user's hooks: every ctxloom-written hook withdrawn, the user's untouched")
	removed, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.NotContains(t, string(removed), "statusLine", "uninstall must withdraw the statusline it installed")
	assert.Contains(t, string(removed), `"baseRef": "head"`, "uninstall must not touch the user's unrelated keys")

	owned, err := hooksLedger(dir).Read(ledger.SurfaceHooks)
	require.NoError(t, err)
	assert.Empty(t, owned, "after uninstall the ledger must claim no hooks: a claim on entries that are gone would delete whatever a user later writes under those commands")
	status, err := hooksLedger(dir).Read(ledger.SurfaceStatusLine)
	require.NoError(t, err)
	assert.Empty(t, status, "after uninstall the ledger must claim no statusline, for the same reason")

	require.NoError(t, w.WriteSettings(fullManagedHookSet(), ctxloomBundleMCP(), dir))
	reapplied, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Equal(t, string(applied), string(reapplied), "re-apply after uninstall must reproduce the first apply byte for byte")
	reappliedLedger, err := os.ReadFile(hooksLedger(dir).Path())
	require.NoError(t, err)
	assert.Equal(t, string(appliedLedger), string(reappliedLedger), "and so must the ledger")
}

// TestRemoveSettings_WithoutALedger_ReclaimsEveryHookCtxloomConstructs pins the
// fallback for a settings.json whose ledger is absent or stale — a fresh clone
// of a tracked file, or a ledger committed from before a regeneration. The name
// rule (ctxloomMachineCallbacks) must recognise EVERY hook ctxloom constructs
// for itself; it recognised four of six, and the other two survived an
// uninstall as user content. The table makes the list a checked binding: a
// constructor added without extending the list fails here.
func TestRemoveSettings_WithoutALedger_ReclaimsEveryHookCtxloomConstructs(t *testing.T) {
	for _, h := range ctxloomOwnHooks() {
		assert.True(t, isCtxloomMachineCallback(h.Command),
			"%q is a hook ctxloom constructs for itself and must be reclaimable by name", h.Command)
	}

	dir := t.TempDir()
	w := &ClaudeCodeHookWriter{}
	require.NoError(t, w.WriteSettings(fullManagedHookSet(), ctxloomBundleMCP(), dir))
	require.NoError(t, os.Remove(hooksLedger(dir).Path()), "simulate a checkout with no ownership record")

	require.NoError(t, w.RemoveSettings(dir))
	for _, cmd := range hookCommands(t, dir) {
		assert.False(t, exectoken.IsManaged(cmd, "ctxloom"),
			"without a ledger, uninstall must still reclaim ctxloom's own callback %q rather than leave it as the user's", cmd)
	}
}

// TestRemoveSettings_UserStatusLineInvokingCtxloomSurvives is the uninstall
// twin of the write-side rule: a statusline the user pointed at the ctxloom
// binary is theirs. Uninstall used to key on the executable token alone and
// removed it — ownership inferred from the file's contents, the exact mistake
// the ledger replaces.
func TestRemoveSettings_UserStatusLineInvokingCtxloomSurvives(t *testing.T) {
	dir := t.TempDir()
	w := &ClaudeCodeHookWriter{}
	settingsPath := w.SettingsPath(dir)
	const userStatus = "ctxloom hook hud --theme mine"
	require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o755))
	require.NoError(t, os.WriteFile(settingsPath,
		[]byte(`{"statusLine": {"type": "command", "command": "`+userStatus+`"}}`), 0o644))

	require.NoError(t, w.RemoveSettings(dir))
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), userStatus,
		"a statusline ctxloom never recorded writing is the user's and survives uninstall")
}

// TestTrackedLedger_NamesExactlyWhatTrackedSettingsCarry checks THIS REPO's own
// .claude/.ctxloom-managed against its own .claude/settings.json. Both are
// tracked, and the ledger was once committed from before a regeneration of the
// settings: it named three of the thirteen hooks in the file and ten that were
// no longer there, so a fresh clone's uninstall left ctxloom's own hooks behind
// as user content. Nothing checked the pair; this does.
//
// It reads the files where they are rather than the committed blobs: the pair
// is written together by one writer, so a rematerialized worktree still holds
// a consistent pair, and a drifted pair on disk is what would be committed.
func TestTrackedLedger_NamesExactlyWhatTrackedSettingsCarry(t *testing.T) {
	root := sourcedir.MustRepoRoot()
	w := &ClaudeCodeHookWriter{}
	settings, err := w.loadSettings(w.SettingsPath(root))
	require.NoError(t, err, "the repo's tracked .claude/settings.json must load")

	recorded, err := hooksLedger(root).Read(ledger.SurfaceHooks)
	require.NoError(t, err)
	assert.ElementsMatch(t, digestsOf(hookCommands(t, root)), recorded,
		"the tracked ledger must name exactly the hooks the tracked settings.json carries; regenerate the pair together")

	var wantStatus []string
	if settings.StatusLine != nil {
		wantStatus = []string{agent.ComputeCommandDigest(settings.StatusLine.Command)}
	}
	status, err := hooksLedger(root).Read(ledger.SurfaceStatusLine)
	require.NoError(t, err)
	assert.Equal(t, wantStatus, status,
		"the tracked ledger must claim the statusline the tracked settings.json carries, and no other")
}
