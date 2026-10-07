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
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// fakeStep is a test step to generation to: it sets key to "yes", or edits
// nothing when key is empty (a marker generation). Its name is the key, so a
// test can predict Applied without a magic string.
type fakeStep struct {
	to  int
	key string
}

func (s fakeStep) To() int      { return s.to }
func (s fakeStep) Name() string { return "to " + itoa(s.to) + " " + s.key }
func (s fakeStep) Apply(root *yaml.Node) {
	if s.key != "" {
		yamlx.MapSet(root, s.key, yamlx.ScalarNode("yes"))
	}
}

var (
	stepA = fakeStep{to: 2, key: "added_a"}
	stepB = fakeStep{to: 3, key: "added_b"}
)

// withSteps: generations 1..3, two steps.
var withSteps = Define("widget", 3, stepA, stepB)

// zeroSteps: the steady state of a kind with no migrations registered.
var zeroSteps = Define("gadget", 2)

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

func TestDefine_CurrentIsDeclaredAndOldestFollowsTheSteps(t *testing.T) {
	assert.Equal(t, 3, withSteps.Current())
	assert.Equal(t, 1, withSteps.Oldest())
	assert.Equal(t, 2, zeroSteps.Current())
	assert.Equal(t, 2, zeroSteps.Oldest(), "zero steps: the floor IS current")
}

// Retiring the oldest step raises Oldest and leaves Current where it was, so a
// file already stamped current still reads: the reason Current is declared.
func TestDefine_DroppingTheOldestStepRaisesOldestOnly(t *testing.T) {
	dropped := Define("widget", 3, stepB)
	assert.Equal(t, withSteps.Current(), dropped.Current())
	assert.Equal(t, 2, dropped.Oldest())

	_, err := dropped.Upgrade(doc(3, ""))
	require.NoError(t, err, "a current file still reads")
	_, err = dropped.Upgrade(doc(1, ""))
	assert.Equal(t, 1, requireVersionError(t, err, ErrTooOld).Found, "the retired generation is refused")
}

func TestDefine_PanicsOnABrokenChain(t *testing.T) {
	cases := map[string]func(){
		"generation zero":         func() { Define("k", 0) },
		"a gap":                   func() { Define("k", 3, stepA, fakeStep{to: 4}) },
		"a step above current":    func() { Define("k", 2, stepB) },
		"a step below the top":    func() { Define("k", 4, stepA, stepB) },
		"out of order":            func() { Define("k", 3, stepB, stepA) },
		"more steps than history": func() { Define("k", 1, fakeStep{to: 0}, fakeStep{to: 1}) },
	}
	for name, define := range cases {
		t.Run(name, func(t *testing.T) { assert.Panics(t, define) })
	}
}

func TestUpgrade_CurrentPassesThroughByteIdentical(t *testing.T) {
	for _, k := range []Kind{withSteps, zeroSteps} {
		t.Run(k.Name(), func(t *testing.T) {
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
	assert.Equal(t, "yes", m[stepA.key])
	assert.Equal(t, "yes", m[stepB.key])
}

// Steps run FROM the declared version: a generation-2 document must not
// re-run the 1→2 step.
func TestUpgrade_RunsOnlyTheStepsAboveFound(t *testing.T) {
	r, err := withSteps.Upgrade(doc(2, "kept: x\n"))
	require.NoError(t, err)
	assert.Equal(t, 2, r.From)
	assert.Equal(t, []string{stepB.Name()}, r.Applied)
	assert.NotContains(t, string(r.Data), stepA.key)
}

// A step that edits nothing still marks a generation: it is reported and the
// document is stamped.
func TestUpgrade_NoopStepStillAdvancesAndStamps(t *testing.T) {
	marker := fakeStep{to: 1}
	k := Define("marker", 1, marker)
	r, err := k.Upgrade(doc(0, "kept: x\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{marker.Name()}, r.Applied)
	assert.Equal(t, 1, declared(t, r.Data))
}

func TestUpgrade_NewerIsRefused(t *testing.T) {
	for _, k := range []Kind{withSteps, zeroSteps} {
		t.Run(k.Name(), func(t *testing.T) {
			_, err := k.Upgrade(doc(k.Current()+4, ""))
			ve := requireVersionError(t, err, ErrNewer)
			assert.Equal(t, VersionError{Kind: k.Name(), Found: k.Current() + 4, Current: k.Current(), Oldest: k.Oldest(), Err: ErrNewer}, *ve)
			assert.Contains(t, err.Error(), itoa(k.Current()+4), "the message names the found version")
			assert.Contains(t, err.Error(), itoa(k.Current()), "the message names the version this binary reads")
		})
	}
}

func TestUpgrade_BelowFloorIsRefused(t *testing.T) {
	for _, k := range []Kind{withSteps, zeroSteps} {
		t.Run(k.Name(), func(t *testing.T) {
			_, err := k.Upgrade(doc(k.Oldest()-1, ""))
			ve := requireVersionError(t, err, ErrTooOld)
			assert.Equal(t, VersionError{Kind: k.Name(), Found: k.Oldest() - 1, Current: k.Current(), Oldest: k.Oldest(), Err: ErrTooOld}, *ve)
			assert.Contains(t, err.Error(), itoa(k.Oldest()), "the message names the floor")
		})
	}
}

// Keyless is generation 0, which is below every floor: a kind whose chain
// does not reach back to 0 refuses a file that never declared a generation,
// and so does a kind with no steps at all. `version:` is just some other key.
func TestUpgrade_KeylessBelowFloorIsRefused(t *testing.T) {
	for _, in := range []string{"kept: x\n", "version: 1\n"} {
		for _, k := range []Kind{withSteps, zeroSteps, Define("one", 1)} {
			t.Run(k.Name()+"/"+in, func(t *testing.T) {
				_, err := k.Upgrade([]byte(in))
				assert.Equal(t, 0, requireVersionError(t, err, ErrTooOld).Found)
			})
		}
	}
}

func TestUpgrade_Unreadable(t *testing.T) {
	cases := map[string]string{
		"not an integer":     Key + ": banana\n",
		"a float":            Key + ": 1.5\n",
		"a mapping":          Key + ": {a: 1}\n",
		"multi-document":     Key + ": 1\n---\nb: 2\n",
		"trailing separator": Key + ": 1\n---\n",
		// The first document parsed, so there is more than one: which one
		// declares the generation is the ambiguity, not the syntax.
		"malformed second document": Key + ": 1\n---\na: [unterminated\n",
	}
	for _, k := range []Kind{withSteps, zeroSteps} {
		for name, in := range cases {
			t.Run(k.Name()+"/"+name, func(t *testing.T) {
				_, err := k.Upgrade([]byte(in))
				ve := requireVersionError(t, err, ErrUnreadable)
				assert.Equal(t, k.Name(), ve.Kind)
				assert.Equal(t, k.Current(), ve.Current)
			})
		}
	}
}

// A document that is not a well-formed YAML mapping has no generation to
// judge: it is the kind's parse failure, not a version fault. It passes through
// untouched so the kind's own decode reports it — as that decode does for any
// malformed file — instead of being mislabelled ErrUnreadable. A duplicate key
// belongs here too: every struct/map decode refuses it, and re-encoding it
// would silently keep whichever entry the node helpers reached first.
func TestUpgrade_NotAWellFormedMappingPassesThroughToTheKindsParse(t *testing.T) {
	cases := map[string]string{
		"malformed":                       "a: [unterminated\n",
		"malformed after a version":       Key + ": 1\nkept: [unterminated\n",
		"a sequence document":             "- a\n- b\n",
		"a scalar document":               "hello\n",
		"duplicate key needing migration": string(doc(1, "kept: x\nkept: y\n")),
		"duplicate version key":           Key + ": 1\n" + Key + ": 2\n",
	}
	for _, k := range []Kind{withSteps, zeroSteps} {
		for name, in := range cases {
			t.Run(k.Name()+"/"+name, func(t *testing.T) {
				data := []byte(in)
				r, err := k.Upgrade(data)
				require.NoError(t, err)
				assert.Empty(t, r.Applied)
				assert.Equal(t, in, string(r.Data))
				var probe map[string]any
				assert.Error(t, yaml.Unmarshal(r.Data, &probe), "the kind's decode must still refuse it")
			})
		}
	}
}

func TestUpgrade_EmptyAndCommentOnlyAreGenerationZero(t *testing.T) {
	gen0 := Define("fresh", 1, fakeStep{to: 1, key: "added_a"})
	for name, in := range map[string]string{
		"empty":         "",
		"comment-only":  "# just a comment\n",
		"bare document": "---\n",
		"null document": "~\n",
	} {
		t.Run(name+"/no step from zero refuses", func(t *testing.T) {
			_, err := zeroSteps.Upgrade([]byte(in))
			assert.Equal(t, 0, requireVersionError(t, err, ErrTooOld).Found)
		})
		t.Run(name+"/with a step from zero migrates", func(t *testing.T) {
			r, err := gen0.Upgrade([]byte(in))
			require.NoError(t, err)
			assert.Equal(t, 0, r.From)
			assert.Equal(t, 1, r.To)
			assert.Len(t, r.Applied, 1)
			assert.Equal(t, 1, declared(t, r.Data))
		})
	}
}

// A comment-only file's comments are its whole content; a migration must not
// throw them away.
func TestUpgrade_CommentOnlyKeepsItsComments(t *testing.T) {
	k := Define("fresh", 1, fakeStep{to: 1})
	r, err := k.Upgrade([]byte("# keep me\n"))
	require.NoError(t, err)
	assert.Contains(t, string(r.Data), "# keep me")
	assert.Equal(t, 1, declared(t, r.Data))
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

// widget is what Decode fills in the tests below.
type widget struct {
	Version int    `yaml:"schema_version"`
	Kept    string `yaml:"kept"`
	AddedA  string `yaml:"added_a"`
	AddedB  string `yaml:"added_b"`
}

// Decode is Upgrade and the kind's decode from ONE parse: the value comes out
// of the migrated tree, and the Result says what Upgrade would.
func TestDecode_MigratesThenDecodesTheTree(t *testing.T) {
	var w widget
	r, err := withSteps.Decode(doc(2, "kept: x\n"), &w)
	require.NoError(t, err)
	assert.Equal(t, widget{Version: 3, Kept: "x", AddedB: "yes"}, w)
	assert.Equal(t, []string{stepB.Name()}, r.Applied)
	assert.Equal(t, 3, declared(t, r.Data), "a migrated Result still carries what WriteBack persists")

	in := doc(3, "kept: y\n")
	w = widget{}
	r, err = withSteps.Decode(in, &w)
	require.NoError(t, err)
	assert.Equal(t, widget{Version: 3, Kept: "y"}, w)
	assert.Empty(t, r.Applied)
	assert.Same(t, &in[0], &r.Data[0])
}

func TestDecode_RefusesAsUpgradeDoes(t *testing.T) {
	var w widget
	_, err := withSteps.Decode(doc(4, ""), &w)
	assert.Equal(t, 4, requireVersionError(t, err, ErrNewer).Found)
	_, err = withSteps.Decode(doc(0, ""), &w)
	assert.Equal(t, 0, requireVersionError(t, err, ErrTooOld).Found)
}

// A document that is not a well-formed mapping is the decode's own failure,
// never a version fault.
func TestDecode_MalformedIsTheParseFailure(t *testing.T) {
	var w widget
	_, err := withSteps.Decode([]byte("kept: [unterminated\n"), &w)
	require.Error(t, err)
	var ve *VersionError
	assert.NotErrorAs(t, err, &ve)
}
