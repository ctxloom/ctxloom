package operations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// staleHookPath is the fake engine's settings file under root.
func staleHookPath(root string) string { return filepath.Join(root, ".fake", "settings.json") }

// An unreadable settings file does not hide the stale entries the next file
// holds, and the row names both: the stale entries and the file it could not
// read.
func TestDoctorCheckStaleHooks_UnreadableFileBesideAStaleOne(t *testing.T) {
	reg, project, home := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json", `{not json`)
	writeSurface(t, home, ".fake/settings.json", staleHookFixture)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.Equal(t, doctorFixRemedy, check.Remedy)
	assert.Contains(t, check.Detail, staleHookPath(home)+": SessionStart runs `ctxloom hook inject-context`",
		"the file read after the unreadable one is still scanned")
	assert.Contains(t, check.Detail, "; could not read: "+staleHookPath(project)+" (")
}

// Stale entries and nothing unreadable: the row does not claim a file it
// failed to read.
func TestDoctorCheckStaleHooks_StaleOnlyClaimsNoUnreadableFile(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json", staleHookFixture)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.NotContains(t, check.Detail, "could not read")
}

// Nothing stale but a file that could not be read: a warning that the
// entries are unverified, with no fix to offer (--fix cannot read it either).
func TestDoctorCheckStaleHooks_UnreadableOnlyIsUnverifiedNotClean(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json", `{not json`)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.Empty(t, check.Remedy)
	assert.Contains(t, check.Detail, "1 settings file(s) could not be read")
	assert.Contains(t, check.Detail, staleHookPath(project))
	assert.NotContains(t, check.Detail, "invoke a `ctxloom hook` subcommand")
}

// When the project IS the home, the engine's two settings paths are one
// file: it is scanned once, so each stale entry is counted once.
func TestDoctorCheckStaleHooks_ProjectThatIsTheHomeIsScannedOnce(t *testing.T) {
	_, project, _ := staleHookEngine(t)
	reg := enginefixture.RegistryOf(enginefixture.Kind("stale-hook-fake", mock.WithHookGlobalScope(agent.HookGlobalScope{
		Paths: func(workDir string) (string, string, error) {
			return staleHookPath(workDir), staleHookPath(project), nil
		},
	})))
	writeSurface(t, project, ".fake/settings.json", staleHookFixture)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "2 settings hook entr(y/ies)", check.Detail)
}

// unhostedEngine is an engine with no agent.Hosted surface at all.
type unhostedEngine struct{ engine.Engine }

// An engine that is not hosted, or hosted with no settings scope, or with a
// scope that resolves no paths, has no settings file for the check to read —
// and the engines registered after it are still read.
func TestStaleHooks_EnginesWithoutSettingsAreSkippedNotFatal(t *testing.T) {
	reg0, project, home := staleHookEngine(t)
	fake, ok := reg0.Lookup("stale-hook-fake")
	require.True(t, ok)
	reg := enginefixture.RegistryOf(
		unhostedEngine{enginefixture.Kind("a-unhosted")},
		enginefixture.Kind("b-no-scope"),
		enginefixture.Kind("c-nil-paths", mock.WithHookGlobalScope(agent.HookGlobalScope{Label: "no paths"})),
		fake,
	)
	writeSurface(t, home, ".fake/settings.json", staleHookFixture)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, staleHookPath(home))

	removed, err := removeStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)
	require.NoError(t, err)
	assert.Len(t, removed, 2)
	assert.Equal(t, staleHookFixtureFixed, readString(t, staleHookPath(home)))
}

// A real settings file opens with a "$schema" scalar, which sorts before
// "hooks": a scalar sibling neither ends the walk nor hides the table after
// it.
func TestStaleHooks_ScalarKeyBeforeTheHookTableIsWalkedPast(t *testing.T) {
	reg, project, _ := staleHookEngine(t)
	writeSurface(t, project, ".fake/settings.json",
		`{"$schema": "https://example.invalid/s.json", "hooks": {"Stop": [{"hooks": [{"command": "ctxloom hook gone"}]}]}}`)

	check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "Stop runs `ctxloom hook gone`")
}

// The event named is the key below the first "hooks" segment wherever the
// table sits; an entry that IS the "hooks" value is named by its first
// segment.
func TestDoctorCheckStaleHooks_EventIsTheKeyBelowHooks(t *testing.T) {
	cases := map[string]struct{ body, event string }{
		"nested table":             {`{"wrapper": {"hooks": {"Stop": [{"command": "ctxloom hook gone"}]}}}`, "Stop"},
		"entry is the hooks value": {`{"hooks": {"command": "ctxloom hook gone"}}`, "hooks"},
		"escaped key":              {`{"hooks": {"a/b~c": [{"command": "ctxloom hook gone"}]}}`, "a/b~c"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reg, project, _ := staleHookEngine(t)
			writeSurface(t, project, ".fake/settings.json", tc.body)

			check := doctorCheckStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

			assert.Contains(t, check.Detail, staleHookPath(project)+": "+tc.event+" runs `ctxloom hook gone`")
		})
	}
}

// failWritesUnder is an OS filesystem that refuses to open any file under dir
// for writing — which is how the atomic writer creates its temp file.
type failWritesUnder struct {
	afero.Fs
	dir string
}

func (f failWritesUnder) refused(name string) bool {
	rel, err := filepath.Rel(f.dir, name)
	return err == nil && !strings.HasPrefix(rel, "..")
}

func (f failWritesUnder) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if f.refused(name) && flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		return nil, os.ErrPermission
	}
	return f.Fs.OpenFile(name, flag, perm)
}

// --fix reports a file it could not read or could not write, and goes on to
// fix the next one: one bad file never leaves the others' stale entries in
// place.
func TestRemoveStaleHooks_AFailedFileDoesNotStopTheNext(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		reg, project, home := staleHookEngine(t)
		writeSurface(t, project, ".fake/settings.json", `{not json`)
		writeSurface(t, home, ".fake/settings.json", staleHookFixture)

		removed, err := removeStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

		require.Error(t, err)
		assert.Contains(t, err.Error(), staleHookPath(project))
		assert.Len(t, removed, 2)
		assert.Equal(t, staleHookFixtureFixed, readString(t, staleHookPath(home)))
	})
	t.Run("unwritable", func(t *testing.T) {
		reg, project, home := staleHookEngine(t)
		writeSurface(t, project, ".fake/settings.json", staleHookFixture)
		writeSurface(t, home, ".fake/settings.json", staleHookFixture)

		removed, err := removeStaleHooks(reg, project, failWritesUnder{afero.NewOsFs(), project}, liveVerbs)

		require.Error(t, err)
		require.Len(t, removed, 2, "only the file actually rewritten counts as removed")
		assert.Equal(t, staleHookPath(home), removed[0].File)
		assert.Equal(t, staleHookFixture, readString(t, staleHookPath(project)))
		assert.Equal(t, staleHookFixtureFixed, readString(t, staleHookPath(home)))
	})
	t.Run("clean first file", func(t *testing.T) {
		reg, project, home := staleHookEngine(t)
		writeSurface(t, project, ".fake/settings.json", staleHookFixtureFixed)
		writeSurface(t, home, ".fake/settings.json", staleHookFixture)

		removed, err := removeStaleHooks(reg, project, afero.NewOsFs(), liveVerbs)

		require.NoError(t, err)
		assert.Len(t, removed, 2, "a file with nothing to remove does not end the pass")
		assert.Equal(t, staleHookFixtureFixed, readString(t, staleHookPath(home)))
	})
}

// A path the engine fails to resolve is an error from --fix, never a silent
// skip.
func TestRemoveStaleHooks_UnresolvedPathIsAnError(t *testing.T) {
	reg := enginefixture.RegistryOf(enginefixture.Kind("broken", mock.WithHookGlobalScope(agent.HookGlobalScope{
		Paths: func(string) (string, string, error) { return "", "", os.ErrNotExist },
	})))

	_, err := removeStaleHooks(reg, t.TempDir(), afero.NewOsFs(), liveVerbs)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken settings")
}
