package fsstatic

import (
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func sweep(target string, w delivery.Writer) func(delivery.Staging) error {
	return func(s delivery.Staging) error { return s.Sweep(target, w) }
}

func callbackEntry(verb string) map[string]any {
	return map[string]any{"type": "command", "command": "ctxloom", "args": []any{"hook", verb, "--engine", "claude-code"}}
}

var (
	usersCtxloomEntry = map[string]any{"type": "command", "command": "ctxloom profile list"}
	usersEntry        = map[string]any{"type": "command", "command": "echo mine"}
)

// trackedSettings is a settings file a repository tracks, as a fresh clone
// carries it onto a machine whose record has never seen it: ctxloom's
// callbacks, alone in some groups and beside the user's hooks in another, the
// HUD statusline, and keys of the user's.
func trackedSettings(t *testing.T) afero.Fs {
	t.Helper()
	doc := map[string]any{
		"model":       "opus",
		"permissions": map[string]any{"deny": []any{"Read(./.env)"}},
		"statusLine":  map[string]any{"type": "command", "command": "ctxloom hook hud"},
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{callbackEntry("session-start"), usersCtxloomEntry}}},
			"Stop":         []any{map[string]any{"hooks": []any{callbackEntry("next-step")}}},
			"PostToolUse": []any{
				map[string]any{"matcher": "Skill", "hooks": []any{callbackEntry("skill-mates")}},
				map[string]any{"matcher": "Bash", "hooks": []any{usersEntry}},
			},
		},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	fs := afero.NewMemMapFs()
	testsupport.WriteFile(t, fs, settingsTarget, data, 0o644)
	return fs
}

// A sweep takes out every ctxloom callback and the HUD statusline, keeps the
// user's hooks — one running ctxloom for its own purposes included — and the
// user's keys, and prunes each group and event it empties.
func TestClaimsASweepTakesOutTheCallbacksNoRecordNames(t *testing.T) {
	fs := trackedSettings(t)
	c := newRecords(t, fs)

	mustCommit(t, c, fs, sweep(settingsTarget, project))

	doc := settingsDoc(t, fs)
	assert.NotContains(t, doc, "statusLine")
	assert.Equal(t, "opus", doc["model"])
	assert.Equal(t, []any{"Read(./.env)"}, deny(t, fs))
	assert.Equal(t, map[string]any{
		"SessionStart": []any{map[string]any{"hooks": []any{usersCtxloomEntry}}},
		"PostToolUse":  []any{map[string]any{"matcher": "Bash", "hooks": []any{usersEntry}}},
	}, doc["hooks"], "the callbacks leave, the groups and events they alone filled leave with them, the user's hooks stay")
}

// A file holding nothing a sweep takes is left byte for byte.
func TestClaimsASweepWithNothingToTakeLeavesTheFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	g, err := json.Marshal(map[string]any{
		"hooks":      map[string]any{"SessionEnd": []any{map[string]any{"hooks": []any{usersCtxloomEntry}}}},
		"statusLine": map[string]any{"type": "command", "command": "ctxloom profile list"},
	})
	require.NoError(t, err)
	testsupport.WriteFile(t, fs, settingsTarget, g, 0o644)
	c := newRecords(t, fs)

	mustCommit(t, c, fs, sweep(settingsTarget, project))

	assert.Equal(t, string(g), read(t, fs, settingsTarget))
}

// A callback a claim staged in the same batch holds is that writer's: the
// sweep leaves it, and the group it sits in, whole.
func TestClaimsASweepLeavesACallbackAClaimHolds(t *testing.T) {
	fs := afero.NewMemMapFs()
	g, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{hookGroup}}})
	require.NoError(t, err)
	testsupport.WriteFile(t, fs, settingsTarget, g, 0o644)
	c := newRecords(t, fs)

	mustCommit(t, c, fs, stage(settingsTarget, session, element("/hooks/PreToolUse", hookGroup)), sweep(settingsTarget, project))

	hooks, _ := settingsDoc(t, fs)["hooks"].(map[string]any)
	assert.Equal(t, []any{hookGroup}, hooks["PreToolUse"])
}

// A file the record names anything in is the record's to release: a sweep
// there takes nothing, a callback no claim holds included.
func TestClaimsASweepLeavesAFileTheRecordNames(t *testing.T) {
	fs := trackedSettings(t)
	c := newRecords(t, fs)
	mustCommit(t, c, fs, stage(settingsTarget, session, element("/permissions/deny", "Task")))
	assert.Contains(t, settingsDoc(t, fs), "statusLine", "a claim staged with no sweep sweeps nothing")
	before := read(t, fs, settingsTarget)

	mustCommit(t, c, fs, sweep(settingsTarget, project))

	assert.Equal(t, before, read(t, fs, settingsTarget))
}

// A sweep names the file and the writer releasing it; either missing is
// refused, never read as "sweep everything" or "sweep for nobody".
func TestClaimsASweepNeedsATargetAndAWriter(t *testing.T) {
	fs := trackedSettings(t)
	c := newRecords(t, fs)
	for _, op := range []func(delivery.Staging) error{sweep("", project), sweep("  ", project), sweep(settingsTarget, "")} {
		_, err := commitOps(t, c, fs, op)
		assert.ErrorContains(t, err, "a sweep needs a target and a writer")
	}
}

// A claim held in the same batch protects only what it holds: a callback
// beside it that no claim holds is still swept.
func TestClaimsASweepTakesWhatTheClaimDoesNotHold(t *testing.T) {
	fs := afero.NewMemMapFs()
	g, err := json.Marshal(map[string]any{"hooks": map[string]any{
		"PreToolUse": []any{hookGroup},
		"Stop":       []any{map[string]any{"hooks": []any{callbackEntry("next-step")}}},
	}})
	require.NoError(t, err)
	testsupport.WriteFile(t, fs, settingsTarget, g, 0o644)
	c := newRecords(t, fs)

	mustCommit(t, c, fs, stage(settingsTarget, session, element("/hooks/PreToolUse", hookGroup)), sweep(settingsTarget, project))

	assert.Equal(t, map[string]any{"PreToolUse": []any{hookGroup}}, settingsDoc(t, fs)["hooks"])
}

// A sweep removes only what it empties: a container of the user's that was
// already empty stays, and one the sweep empties (every hook ctxloom's) goes.
func TestClaimsASweepRemovesOnlyWhatItEmpties(t *testing.T) {
	fs := afero.NewMemMapFs()
	g, err := json.Marshal(map[string]any{
		"permissions": map[string]any{"allow": []any{}},
		"env":         map[string]any{},
		"hooks":       map[string]any{"Stop": []any{map[string]any{"hooks": []any{callbackEntry("next-step")}}}},
	})
	require.NoError(t, err)
	testsupport.WriteFile(t, fs, settingsTarget, g, 0o644)
	c := newRecords(t, fs)

	mustCommit(t, c, fs, sweep(settingsTarget, project))

	assert.Equal(t, map[string]any{
		"permissions": map[string]any{"allow": []any{}},
		"env":         map[string]any{},
	}, settingsDoc(t, fs))
}
