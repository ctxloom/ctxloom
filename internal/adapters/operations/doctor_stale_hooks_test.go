package operations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// staleHookFixture is a settings file in the shape an engine's hook table
// takes: a stale `ctxloom hook` verb beside a live one in the same group, a
// group holding nothing but a stale entry, a FOREIGN command that merely
// spells the same words, and a user setting outside the hook table.
const staleHookFixture = `{
  "permissions": {
    "allow": ["Bash(ls)"]
  },
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "ctxloom hook inject-context abc123"},
          {"type": "command", "command": "/usr/local/bin/ctxloom hook session-start"}
        ]
      },
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "\"/opt/my tools/ctxloom\" hook inject-context def456"}
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "my-tool hook inject-context"}
        ]
      }
    ]
  }
}
`

// staleHookFixtureFixed is staleHookFixture with exactly the two stale
// entries taken out (the second group held nothing else, so it goes too) and
// every other byte where it was.
const staleHookFixtureFixed = `{
  "permissions": {
    "allow": ["Bash(ls)"]
  },
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "/usr/local/bin/ctxloom hook session-start"}
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "my-tool hook inject-context"}
        ]
      }
    ]
  }
}
`

// liveVerbs stands in for the hook subcommands a build has. The check takes
// the set as an argument: it carries no list of its own.
var liveVerbs = []string{"session-start", "hud", "permission"}

// staleHookEngine is a test-double engine whose hook settings live at
// <project>/.fake/settings.json and <home>/.fake/settings.json, reached the
// way doctor reaches every engine's: through its declared HookGlobalScope.
func staleHookEngine(t *testing.T) (reg engine.Registry, project, home string) {
	t.Helper()
	project, home = t.TempDir(), t.TempDir()
	reg = enginefixture.RegistryOf(enginefixture.Kind("stale-hook-fake", mock.WithHookGlobalScope(agent.HookGlobalScope{
		Paths: func(workDir string) (string, string, error) {
			return filepath.Join(workDir, ".fake", "settings.json"), filepath.Join(home, ".fake", "settings.json"), nil
		},
		Label: "the fake engine's user settings",
	})))
	return reg, project, home
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestDoctorCheckStaleHooks_StaleVerbIsReportedWithFileEventAndVerb(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json", staleHookFixture)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.Equal(t, doctorFixRemedy, check.Remedy)
	assert.Contains(t, check.Detail, filepath.Join(project, ".fake", "settings.json"), "names the file")
	assert.Contains(t, check.Detail, "SessionStart", "names the event")
	assert.Contains(t, check.Detail, "inject-context", "names the verb")
	assert.Contains(t, check.Detail, "2 ", "both stale entries are counted, the quoted-path one included")
}

func TestDoctorCheckStaleHooks_ValidVerbAndForeignCommandAreQuiet(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json", staleHookFixtureFixed)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

	assert.Equal(t, DoctorOK, check.Status,
		"a live verb and a non-ctxloom command spelling `hook inject-context` are not ctxloom's leftovers: %s", check.Detail)
}

func TestDoctorCheckStaleHooks_UserScopeFileIsInspected(t *testing.T) {
	reg, project, home := staleHookEngine(t)
	writeSurface(t, home, ".fake/settings.json", staleHookFixture)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, filepath.Join(home, ".fake", "settings.json"))
}

// THE NO-HAND-LIST PROPERTY: rename a verb in the set the caller supplies and
// the check follows — the entry naming the old spelling becomes stale and the
// new spelling is live. Nothing inside the check knows any verb's name.
func TestDoctorCheckStaleHooks_FollowsARenamedVerb(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json",
		`{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "ctxloom hook session-start"}]}]}}`)

	before := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), []string{"session-start"})
	renamed := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), []string{"session-begin"})

	assert.Equal(t, DoctorOK, before.Status)
	assert.Equal(t, DoctorWarn, renamed.Status)
	assert.Contains(t, renamed.Detail, "session-start")
}

// An empty verb set would read every ctxloom hook as stale, and the fix would
// then delete the live ones. It is a caller bug, refused rather than obeyed.
func TestStaleHooks_EmptyVerbSetIsRefused(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json", staleHookFixtureFixed)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), nil)
	assert.NotEqual(t, DoctorWarn, check.Status)

	_, err := removeStaleHooks(reg, project, afero.NewOsFs(), nil)
	require.Error(t, err)
	assert.Equal(t, staleHookFixtureFixed, readString(t, filepath.Join(project, ".fake", "settings.json")))
}

func TestRemoveStaleHooks_RemovesOnlyTheStaleEntries(t *testing.T) {
	reg, project, home := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json", staleHookFixture)
	writeSurface(t, home, ".fake/settings.json", staleHookFixture)

	removed, err := removeStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)
	require.NoError(t, err)

	assert.Len(t, removed, 4, "two stale entries in each of the two scopes")
	for _, r := range removed {
		assert.Equal(t, "SessionStart", r.Event)
		assert.Equal(t, "inject-context", r.Verb)
	}
	for _, dir := range []string{project, home} {
		assert.Equal(t, staleHookFixtureFixed, readString(t, filepath.Join(dir, ".fake", "settings.json")),
			"only the stale entries leave; every other byte stays")
	}

	again := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)
	assert.Equal(t, DoctorOK, again.Status, "after the fix the check is quiet: %s", again.Detail)
}

// A file whose only hook was stale keeps the user's own settings; the hook
// table that held nothing else goes with its entry.
func TestRemoveStaleHooks_EmptiedTableLeavesTheRestOfTheFile(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json",
		`{"model": "x", "hooks": {"SessionStart": [{"matcher": "", "hooks": [{"type": "command", "command": "ctxloom hook inject-context h"}]}]}}`)

	_, err := removeStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(readString(t, filepath.Join(project, ".fake", "settings.json"))), &got))
	assert.Equal(t, map[string]any{"model": "x"}, got)
}

func TestRemoveStaleHooks_NothingStaleWritesNothing(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	path := filepath.Join(project, ".fake", "settings.json")
	writeSurface(t, project, ".fake/settings.json", staleHookFixtureFixed)
	stamp := mustModTime(t, path)

	removed, err := removeStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)
	require.NoError(t, err)
	assert.Empty(t, removed)
	assert.Equal(t, stamp, mustModTime(t, path))
}

func mustModTime(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi.ModTime().UnixNano()
}

// Two stale entries in ONE array, a live one between them: removing the first
// before the second would shift the second's index onto the live entry. The
// removals run highest index first.
func TestRemoveStaleHooks_SeveralInOneArrayKeepTheLiveOne(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json", `{"hooks": {"SessionStart": [{"hooks": [
  {"command": "ctxloom hook gone-a"},
  {"command": "ctxloom hook session-start"},
  {"command": "ctxloom hook gone-b"},
  {"command": "ctxloom hook hud"}
]}]}}
`)

	removed, err := removeStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)
	require.NoError(t, err)
	require.Len(t, removed, 2)

	assert.Equal(t, `{"hooks": {"SessionStart": [{"hooks": [
  {"command": "ctxloom hook session-start"},
  {"command": "ctxloom hook hud"}
]}]}}
`, readString(t, filepath.Join(project, ".fake", "settings.json")))
}

// Only the `hook` namespace is judged: a ctxloom entry running any other
// subcommand is not a hook verb and never stale here.
func TestDoctorCheckStaleHooks_NonHookCtxloomCommandIsQuiet(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json",
		`{"hooks": {"SessionStart": [{"hooks": [{"command": "ctxloom session bind"}]}]}}`)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

	assert.Equal(t, DoctorOK, check.Status, check.Detail)
}
