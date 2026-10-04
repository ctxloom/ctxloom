package schemaver

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// setKey is a test step: it sets key to "yes". Its name is the key, so a
// test can predict Applied without a magic string.
type setKey string

func (s setKey) Name() string { return string(s) }
func (s setKey) Apply(root *yaml.Node) bool {
	yamlx.MapSet(root, string(s), yamlx.ScalarNode("yes"))
	return true
}

// noop is a step that changes nothing in the tree; it still advances the
// version, because a step marks a generation whether or not it edits keys.
type noop string

func (n noop) Name() string                  { return string(n) }
func (noop) Apply(*yaml.Node) (changed bool) { return false }

const (
	stepA = setKey("added_a")
	stepB = setKey("added_b")
)

// withSteps: generations 1..3, two steps.
var withSteps = Kind{Name: "widget", Oldest: 1, Steps: []upgrade.Upgrader{stepA, stepB}}

// zeroSteps: the steady state of a kind with no migrations registered.
var zeroSteps = Kind{Name: "gadget", Oldest: 2}

func doc(version int, rest string) []byte {
	return []byte(Key + ": " + itoa(version) + "\n" + rest)
}

func itoa(n int) string {
	out, err := yaml.Marshal(n)
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(out))
}

// declared parses out and returns its top-level Key, failing the test when
// it is not a readable integer.
func declared(t *testing.T, out []byte) int {
	t.Helper()
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(out, &m))
	v, ok := m[Key].(int)
	require.True(t, ok, "%s must be an integer in %q", Key, out)
	return v
}

func requireVersionError(t *testing.T, err error, sentinel error) *VersionError {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	var ve *VersionError
	require.ErrorAs(t, err, &ve)
	return ve
}

func TestCurrent_IsOldestPlusSteps(t *testing.T) {
	assert.Equal(t, 3, withSteps.Current())
	assert.Equal(t, 2, zeroSteps.Current(), "zero steps: the floor IS current")
}

func TestUpgrade_CurrentPassesThroughByteIdentical(t *testing.T) {
	for _, k := range []Kind{withSteps, zeroSteps} {
		t.Run(k.Name, func(t *testing.T) {
			in := doc(k.Current(), "# keep me\nkept:   x\n")
			r, err := k.Upgrade(in)
			require.NoError(t, err)
			assert.Empty(t, r.Applied)
			assert.Equal(t, k.Current(), r.From)
			assert.Equal(t, k.Current(), r.To)
			require.NotEmpty(t, r.Data)
			assert.Same(t, &in[0], &r.Data[0], "a current document must not be reserialized")
		})
	}
}

func TestUpgrade_OlderIsMigratedAndStamped(t *testing.T) {
	r, err := withSteps.Upgrade(doc(1, "kept: x\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, r.From)
	assert.Equal(t, 3, r.To)
	assert.Equal(t, []string{stepA.Name(), stepB.Name()}, r.Applied)
	assert.Equal(t, 3, declared(t, r.Data))
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(r.Data, &m))
	assert.Equal(t, "x", m["kept"])
	assert.Equal(t, "yes", m[string(stepA)])
	assert.Equal(t, "yes", m[string(stepB)])
}

// Steps run FROM the declared version: a generation-2 document must not
// re-run the 1→2 step.
func TestUpgrade_RunsOnlyTheStepsAboveFound(t *testing.T) {
	r, err := withSteps.Upgrade(doc(2, "kept: x\n"))
	require.NoError(t, err)
	assert.Equal(t, 2, r.From)
	assert.Equal(t, []string{stepB.Name()}, r.Applied)
	assert.NotContains(t, string(r.Data), string(stepA))
}

// A step that edits nothing still marks a generation: it is reported and the
// document is stamped.
func TestUpgrade_NoopStepStillAdvancesAndStamps(t *testing.T) {
	k := Kind{Name: "marker", Oldest: 0, Steps: []upgrade.Upgrader{noop("introduce")}}
	r, err := k.Upgrade(doc(0, "kept: x\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"introduce"}, r.Applied)
	assert.Equal(t, 1, declared(t, r.Data))
}

func TestUpgrade_NewerIsRefused(t *testing.T) {
	for _, k := range []Kind{withSteps, zeroSteps} {
		t.Run(k.Name, func(t *testing.T) {
			_, err := k.Upgrade(doc(k.Current()+4, ""))
			ve := requireVersionError(t, err, ErrNewer)
			assert.Equal(t, VersionError{Kind: k.Name, Found: k.Current() + 4, Current: k.Current(), Oldest: k.Oldest, Err: ErrNewer}, *ve)
			assert.Contains(t, err.Error(), itoa(k.Current()+4), "the message names the found version")
			assert.Contains(t, err.Error(), itoa(k.Current()), "the message names the version this binary reads")
		})
	}
}

func TestUpgrade_BelowFloorIsRefused(t *testing.T) {
	for _, k := range []Kind{withSteps, zeroSteps} {
		t.Run(k.Name, func(t *testing.T) {
			_, err := k.Upgrade(doc(k.Oldest-1, ""))
			ve := requireVersionError(t, err, ErrTooOld)
			assert.Equal(t, VersionError{Kind: k.Name, Found: k.Oldest - 1, Current: k.Current(), Oldest: k.Oldest, Err: ErrTooOld}, *ve)
			assert.Contains(t, err.Error(), itoa(k.Oldest), "the message names the floor")
		})
	}
}

// Keyless is generation 0, which is below a floor of 1.
func TestUpgrade_KeylessBelowFloorIsRefused(t *testing.T) {
	_, err := withSteps.Upgrade([]byte("kept: x\n"))
	ve := requireVersionError(t, err, ErrTooOld)
	assert.Equal(t, 0, ve.Found)
}

func TestUpgrade_Unreadable(t *testing.T) {
	cases := map[string]string{
		"not an integer":      Key + ": banana\n",
		"a float":             Key + ": 1.5\n",
		"a mapping":           Key + ": {a: 1}\n",
		"a sequence document": "- a\n- b\n",
		"a scalar document":   "hello\n",
		"malformed":           "a: [unterminated\n",
		"multi-document":      Key + ": 1\n---\nb: 2\n",
		"trailing separator":  Key + ": 1\n---\n",
	}
	for _, k := range []Kind{withSteps, zeroSteps} {
		for name, in := range cases {
			t.Run(k.Name+"/"+name, func(t *testing.T) {
				_, err := k.Upgrade([]byte(in))
				ve := requireVersionError(t, err, ErrUnreadable)
				assert.Equal(t, k.Name, ve.Kind)
				assert.Equal(t, k.Current(), ve.Current)
			})
		}
	}
}

// A duplicate key would be resolved by whichever entry the node helpers
// reach first if the document were re-encoded; refuse rather than guess.
func TestUpgrade_DuplicateKeyNeedingMigrationIsUnreadable(t *testing.T) {
	_, err := withSteps.Upgrade(doc(1, "kept: x\nkept: y\n"))
	assert.Equal(t, withSteps.Name, requireVersionError(t, err, ErrUnreadable).Kind)
}

func TestUpgrade_EmptyAndCommentOnlyAreGenerationZero(t *testing.T) {
	gen0 := Kind{Name: "fresh", Oldest: 0, Steps: []upgrade.Upgrader{stepA}}
	for name, in := range map[string]string{
		"empty":         "",
		"comment-only":  "# just a comment\n",
		"bare document": "---\n",
		"null document": "~\n",
	} {
		t.Run(name+"/zero steps passes through", func(t *testing.T) {
			k := Kind{Name: "fresh", Oldest: 0}
			data := []byte(in)
			r, err := k.Upgrade(data)
			require.NoError(t, err)
			assert.Equal(t, 0, r.From)
			assert.Equal(t, 0, r.To)
			assert.Empty(t, r.Applied)
			assert.Equal(t, in, string(r.Data))
		})
		t.Run(name+"/with steps migrates", func(t *testing.T) {
			r, err := gen0.Upgrade([]byte(in))
			require.NoError(t, err)
			assert.Equal(t, 0, r.From)
			assert.Equal(t, 1, r.To)
			assert.Equal(t, []string{stepA.Name()}, r.Applied)
			assert.Equal(t, 1, declared(t, r.Data))
		})
	}
}

// A comment-only file's comments are its whole content; a migration must not
// throw them away.
func TestUpgrade_CommentOnlyKeepsItsComments(t *testing.T) {
	k := Kind{Name: "fresh", Oldest: 0, Steps: []upgrade.Upgrader{noop("introduce")}}
	r, err := k.Upgrade([]byte("# keep me\n"))
	require.NoError(t, err)
	assert.Contains(t, string(r.Data), "# keep me")
	assert.Equal(t, 1, declared(t, r.Data))
}

// The rename runs BEFORE the version read: with a floor of 1, a `version: 1`
// file read before the rename would be keyless — generation 0, below the
// floor — and be refused for merely spelling its version the old way.
func TestUpgrade_LegacyKeyRenamedBeforeTheVersionCheck(t *testing.T) {
	k := Kind{Name: "legacy", LegacyKey: "version", Oldest: 1}
	r, err := k.Upgrade([]byte("# head\nversion: 1 # why\nkept: x\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, r.From)
	assert.Equal(t, 1, r.To)
	assert.Equal(t, []string{renameStepName(k.LegacyKey)}, r.Applied, "the rename is a change and is reported")
	assert.Equal(t, 1, declared(t, r.Data))
	assert.NotRegexp(t, `(?m)^version:`, string(r.Data))
	assert.True(t, strings.HasPrefix(string(r.Data), "# head\n"+Key+": 1"), "renamed in place: %q", r.Data)
	assert.Contains(t, string(r.Data), "# why")
}

// The rename is per-kind opt-in: without LegacyKey, `version:` is just some
// other key and the document is keyless.
func TestUpgrade_NoLegacyKeyMeansNoRename(t *testing.T) {
	k := Kind{Name: "bundle-like", Oldest: 1}
	_, err := k.Upgrade([]byte("version: 1\n"))
	ve := requireVersionError(t, err, ErrTooOld)
	assert.Equal(t, 0, ve.Found)
}

func TestUpgrade_LegacyRenameThenMigrates(t *testing.T) {
	k := Kind{Name: "legacy", LegacyKey: "version", Oldest: 1, Steps: []upgrade.Upgrader{stepA}}
	r, err := k.Upgrade([]byte("version: 1\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{renameStepName("version"), stepA.Name()}, r.Applied)
	assert.Equal(t, 2, declared(t, r.Data))
}

// Both spellings present: no safe way to pick one.
func TestUpgrade_LegacyAndKeyBothPresentIsUnreadable(t *testing.T) {
	k := Kind{Name: "legacy", LegacyKey: "version", Oldest: 1}
	_, err := k.Upgrade([]byte("version: 1\n" + Key + ": 1\n"))
	assert.Equal(t, k.Name, requireVersionError(t, err, ErrUnreadable).Kind)
}

func TestStamp(t *testing.T) {
	t.Run("absent goes first", func(t *testing.T) {
		var d yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte("a: 1\n"), &d))
		withSteps.Stamp(d.Content[0])
		out, err := yaml.Marshal(&d)
		require.NoError(t, err)
		assert.Equal(t, Key+": 3\na: 1\n", string(out))
	})
	t.Run("present is replaced in place", func(t *testing.T) {
		var d yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte("a: 1\n"+Key+": 1\n"), &d))
		withSteps.Stamp(d.Content[0])
		out, err := yaml.Marshal(&d)
		require.NoError(t, err)
		assert.Equal(t, "a: 1\n"+Key+": 3\n", string(out))
	})
}

func TestWriteBack_BacksUpThenReplaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/cfg/file.yaml"
	old := []byte("old: 1\n")
	taskstest.WriteFile(t, fs, path, old, 0o600)
	r := Result{Data: []byte(Key + ": 3\n"), From: 1, To: 3, Applied: []string{"x"}}

	require.NoError(t, WriteBack(fs, path, r, KeepBackup))

	got, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, r.Data, got)
	bak, err := afero.ReadFile(fs, path+BackupSuffix)
	require.NoError(t, err)
	assert.Equal(t, old, bak)
	for _, p := range []string{path, path + BackupSuffix} {
		info, err := fs.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "%s keeps the user's mode", p)
	}
}

func TestWriteBack_NoBackupReplacesAndLeavesNothingBeside(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/proj/file.yaml"
	taskstest.WriteFile(t, fs, path, []byte("old: 1\n"), 0o640)
	r := Result{Data: []byte(Key + ": 3\n"), From: 1, To: 3, Applied: []string{"x"}}

	require.NoError(t, WriteBack(fs, path, r, NoBackup))

	got, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, r.Data, got)
	info, err := fs.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "the file keeps the user's mode")
	_, err = fs.Stat(path + BackupSuffix)
	assert.True(t, errors.Is(err, os.ErrNotExist), "NoBackup writes no backup")
}

// The backup comes first: when it cannot be written the file is not touched.
func TestWriteBack_NoBackupNoWrite(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/cfg/file.yaml"
	old := []byte("old: 1\n")
	taskstest.WriteFile(t, base, path, old, 0o644)
	fs := afero.NewReadOnlyFs(base)

	err := WriteBack(fs, path, Result{Data: []byte(Key + ": 3\n")}, KeepBackup)
	require.Error(t, err)
	got, rerr := afero.ReadFile(base, path)
	require.NoError(t, rerr)
	assert.Equal(t, old, got)
}

// --- the --write-upgrades switch ------------------------------------------

func parse(t *testing.T, args ...string) {
	t.Helper()
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	BindWriteUpgrades(fs)
	require.NoError(t, fs.Parse(args))
}

func TestWriteUpgrades_FlagTurnsItOn(t *testing.T) {
	t.Cleanup(func() { parse(t) })
	parse(t, "--"+WriteUpgradesFlag)
	assert.True(t, WriteUpgrades())
}

func TestWriteUpgrades_ExplicitFalse(t *testing.T) {
	t.Cleanup(func() { parse(t) })
	parse(t, "--"+WriteUpgradesFlag+"=false")
	assert.False(t, WriteUpgrades())
}

// Binding resets: a command tree built after one that set the switch must
// not inherit it — a test driving the root twice would otherwise write files
// on a run that never asked to.
func TestWriteUpgrades_BindResets(t *testing.T) {
	t.Cleanup(func() { parse(t) })
	parse(t, "--"+WriteUpgradesFlag)
	require.True(t, WriteUpgrades())
	parse(t)
	assert.False(t, WriteUpgrades())
}

// Load sites read the switch from goroutines the flag parse never sees.
// The interleaving under test is a read concurrent with a set; the race
// detector is the assertion. Both sides start together behind one barrier so
// they genuinely overlap.
func TestWriteUpgrades_ConcurrentReadAndSet(t *testing.T) {
	t.Cleanup(func() { parse(t) })
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	BindWriteUpgrades(fs)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range 100 {
			_ = WriteUpgrades()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 100 {
			_ = fs.Set(WriteUpgradesFlag, "true")
		}
	}()
	close(start)
	wg.Wait()
	assert.True(t, WriteUpgrades())
}

// IntroduceKey is the first step of a kind that was unversioned: a keyless
// file means what a generation-1 file means, so the step edits nothing and
// only the stamp changes the document.
func TestIntroduceKey_KeylessBecomesGenerationOneUnchangedOtherwise(t *testing.T) {
	k := Kind{Name: "was unversioned", Oldest: 0, Steps: []upgrade.Upgrader{IntroduceKey}}
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("kept: x\n"), &root))
	assert.False(t, IntroduceKey.Apply(root.Content[0]), "the step edits nothing")

	r, err := k.Upgrade([]byte("kept: x\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{IntroduceKey.Name()}, r.Applied)
	assert.Equal(t, 1, declared(t, r.Data))
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(r.Data, &m))
	assert.Equal(t, map[string]any{Key: 1, "kept": "x"}, m)
}
