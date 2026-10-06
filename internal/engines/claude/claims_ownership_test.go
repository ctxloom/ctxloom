package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/wire"
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
		agent.NewMailDrainHook(),
		approvalCodec{}.Hooks(time.Minute).PermissionAsk[0],
	}
}

// fullManagedHookSet is the shape apply-hooks assembles for claude-code:
// ctxloom's own callbacks, the bundle-shipped ctxloom hooks, and a companion
// binary's hook — one that runs another executable, which only the record can
// say is ctxloom's.
func fullManagedHookSet() *wire.HooksConfig {
	own := ctxloomOwnHooks()
	return &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: append([]wire.Hook{{Command: "ctxloom hook session-bind"}}, own[0], own[1], own[2]),
		PostTool:     []wire.Hook{{Command: "ctxloom hook stamp-plan"}, own[3]},
		TurnEnd:      []wire.Hook{own[4]},
		PreTool:      []wire.Hook{{Command: "ltk evaluate", Matcher: "Bash"}},
	}}
}

// TestUninstall_ThenInstall_IsIdentity is the round trip the claims record
// exists to make safe: uninstall returns the file to what the user had, and a
// re-install returns it to what the first install produced — asserted on the
// BYTES, not on counts.
//
// The user's hook and the user's unrelated key are the controls: without them
// "returned to prior content" cannot be told apart from "emptied the file".
func TestUninstall_ThenInstall_IsIdentity(t *testing.T) {
	dir := t.TempDir()
	p := atRest(t, afero.NewOsFs(), dir)
	settingsPath := ProjectSettingsPath(dir)
	const userHook = "./scripts/my-own-hook.sh"
	seed := `{
  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "` + userHook + `"}]}]},
  "worktree": {"baseRef": "head"}
}
`
	require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o755))
	require.NoError(t, os.WriteFile(settingsPath, []byte(seed), 0o644))

	install(t, p, fullManagedHookSet(), ctxloomBundleMCP())
	applied, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	require.Len(t, hookCommands(t, dir), 9, "precondition: the user's hook and the whole assembled set reached the file")

	require.NoError(t, p.Uninstall())
	assert.Equal(t, []string{userHook}, hookCommands(t, dir),
		"uninstall must leave exactly the user's hooks: every ctxloom-written hook withdrawn, the user's untouched")
	removed, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.NotContains(t, string(removed), "statusLine", "uninstall must withdraw the statusline it installed")
	assert.Contains(t, string(removed), `"baseRef": "head"`, "uninstall must not touch the user's unrelated keys")

	install(t, p, fullManagedHookSet(), ctxloomBundleMCP())
	reapplied, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Equal(t, string(applied), string(reapplied), "re-install after uninstall must reproduce the first install byte for byte")
}

// TestInstall_WithoutARecord_KeepsOneOfEachHook: with the claims record lost
// (a fresh machine over a tracked settings.json), a re-install finds each
// hook it is about to write already there and does not add a second.
func TestInstall_WithoutARecord_KeepsOneOfEachHook(t *testing.T) {
	dir := t.TempDir()
	p := atRest(t, afero.NewOsFs(), dir)
	install(t, p, fullManagedHookSet(), ctxloomBundleMCP())
	once := hookCommands(t, dir)

	require.NoError(t, os.RemoveAll(filepath.Clean(dir)+".records"), "simulate a machine with no claims record")
	install(t, atRest(t, afero.NewOsFs(), dir), fullManagedHookSet(), ctxloomBundleMCP())
	assert.ElementsMatch(t, once, hookCommands(t, dir))
}

// TestUninstall_UserStatusLineInvokingCtxloomSurvives: a statusline the user
// pointed at the ctxloom binary is theirs. Ownership is the record's, never
// inferred from the file's contents.
func TestUninstall_UserStatusLineInvokingCtxloomSurvives(t *testing.T) {
	dir := t.TempDir()
	p := atRest(t, afero.NewOsFs(), dir)
	settingsPath := ProjectSettingsPath(dir)
	const userStatus = "ctxloom hook hud --theme mine"
	require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o755))
	require.NoError(t, os.WriteFile(settingsPath,
		[]byte(`{"statusLine": {"type": "command", "command": "`+userStatus+`"}}`), 0o644))

	install(t, p, fullManagedHookSet(), nil)
	require.NoError(t, p.Uninstall())
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), userStatus,
		"a statusline ctxloom never claimed is the user's and survives install and uninstall")
}

// TestInstall_CtxloomHooksAreWrittenInExecForm: each hook ctxloom constructs
// for itself reaches settings.json as claude's exec form — the executable in
// "command", its argv in "args" — so no shell ever parses it.
func TestInstall_CtxloomHooksAreWrittenInExecForm(t *testing.T) {
	dir := t.TempDir()
	own := ctxloomOwnHooks()
	install(t, atRest(t, afero.NewOsFs(), dir), &wire.HooksConfig{Unified: wire.UnifiedHooks{TurnEnd: own}}, nil)
	settings, err := (&ClaudeCodeHookWriter{}).loadSettings(ProjectSettingsPath(dir))
	require.NoError(t, err)
	var written []claudeCodeHook
	for _, m := range settings.Hooks["Stop"] {
		written = append(written, m.Hooks...)
	}
	var want, got [][]string
	for _, h := range own {
		want = append(want, h.Args)
	}
	for _, h := range written {
		assert.Equal(t, agent.CtxloomCommand(), h.Command, "the executable alone")
		got = append(got, h.Args)
	}
	assert.ElementsMatch(t, want, got)
}

// TestUninstall_AUserHookRunningCtxloomSurvives: in exec form every hook
// ctxloom writes for itself names the same executable, so ownership must be
// keyed on the whole value. A user's own hooks that run the ctxloom binary —
// bare, or in exec form with a verb ctxloom never installs — are theirs, and
// an install-then-uninstall leaves them exactly as found.
func TestUninstall_AUserHookRunningCtxloomSurvives(t *testing.T) {
	dir := t.TempDir()
	p := atRest(t, afero.NewOsFs(), dir)
	settingsPath := ProjectSettingsPath(dir)
	require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o755))
	require.NoError(t, os.WriteFile(settingsPath, []byte(`{"hooks": {"Stop": [{"hooks": [
		{"type": "command", "command": "ctxloom"},
		{"type": "command", "command": "ctxloom", "args": ["doctor"]}
	]}]}}`), 0o644))

	install(t, p, fullManagedHookSet(), ctxloomBundleMCP())
	require.NoError(t, p.Uninstall())
	assert.ElementsMatch(t, []string{"ctxloom", "'ctxloom' 'doctor'"}, hookCommands(t, dir))
}

// companionExecHook is an exec-form hook ctxloom writes that runs another
// executable — the case only the record, keyed on the whole value, owns.
var companionExecHook = wire.Hook{Type: "command", Command: "ltk", Args: []string{"evaluate", "--strict"}, Matcher: "Bash"}

// TestUninstall_ReclaimsAnOwnedExecHook: an exec-form hook ctxloom claimed is
// reclaimed on uninstall through the record — no name rule recognises it.
func TestUninstall_ReclaimsAnOwnedExecHook(t *testing.T) {
	dir := t.TempDir()
	p := atRest(t, afero.NewOsFs(), dir)
	install(t, p, &wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{companionExecHook}}}, nil)
	require.Equal(t, []string{companionExecHook.Line()}, hookCommands(t, dir), "precondition: written")
	require.NoError(t, p.Uninstall())
	assert.NoFileExists(t, ProjectSettingsPath(dir), "the file the install created is taken back out whole")
}

// TestInstall_WithoutARecord_TakesOutSupersededSpellingsOfItsOwnHooks: a
// tracked settings.json carries ctxloom hooks no record on this machine names
// — an older spelling of a callback ctxloom still installs (the shell form of
// next-step) and a stale set of context-injection parts (another hash, another
// part count). Each runs a ctxloom callback ctxloom is installing into that
// same group now, so each is ctxloom's own leftover: delivery leaves exactly
// one of each current hook, and none of the superseded ones, or claude runs
// every callback twice. The user's hooks — their own script, and their own
// hook running a ctxloom verb ctxloom never installs there — are untouched.
func TestInstall_WithoutARecord_TakesOutSupersededSpellingsOfItsOwnHooks(t *testing.T) {
	dir := t.TempDir()
	settingsPath := ProjectSettingsPath(dir)
	const (
		userScript      = "./scripts/my-own-hook.sh"
		staleNextStep   = "'ctxloom' hook next-step"
		staleInjectPart = "'ctxloom' hook inject-context --part 1 --of 8 0ld0ld0ld0ld0ld0"
	)
	nextStep := agent.NewNextStepHook()
	require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o755))
	require.NoError(t, os.WriteFile(settingsPath, []byte(`{"hooks": {
  "Stop": [{"hooks": [
    {"type": "command", "command": "`+staleNextStep+`", "timeout": 15},
    {"type": "command", "command": "ctxloom", "args": ["hook", "next-step"], "timeout": 15},
    {"type": "command", "command": "`+userScript+`"},
    {"type": "command", "command": "ctxloom", "args": ["doctor"]}
  ]}],
  "SessionStart": [{"hooks": [
    {"type": "command", "command": "`+staleInjectPart+`", "timeout": 60}
  ]}]
}}`), 0o644))

	install(t, atRest(t, afero.NewOsFs(), dir), &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{agent.NewContextInjectionChunkHook("abc123", 1, 2), agent.NewContextInjectionChunkHook("abc123", 2, 2)},
		TurnEnd:      []wire.Hook{nextStep},
	}}, nil)

	assert.ElementsMatch(t, []string{
		nextStep.Line(),
		agent.NewContextInjectionChunkHook("abc123", 1, 2).Line(),
		agent.NewContextInjectionChunkHook("abc123", 2, 2).Line(),
		userScript,
		"'ctxloom' 'doctor'",
	}, hookCommands(t, dir))
}
