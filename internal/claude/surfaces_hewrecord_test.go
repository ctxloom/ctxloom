package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/confpatch"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// This file proves the hew-record settings approach: the first delivery on
// the seam whose bytes are NOT a project file. It writes beneath the
// engine's private home through the §9.7 record store, and the record — not
// the file — is what remembers what ctxloom put there.

// recordHome is a per-session engine home a ctxloom-launched agent owns.
const recordHome = "/proj/.ctxloom/state/h/home/claude"

// userSettings is what an engine home carries BEFORE ctxloom writes when
// something already put settings there: a hook for an event ctxloom does not
// write (PreToolUse), a deny entry, and a key ctxloom knows nothing about.
// All of it must survive every write byte-for-byte.
func userSettings(t *testing.T) []byte {
	t.Helper()
	return settingsJSON(t, map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "echo user"}}}},
		},
		"model":       "opus",
		"permissions": map[string]any{"deny": []any{"Bash(rm -rf *)"}},
		"statusLine":  map[string]any{"type": "command", "command": "my-status"},
	})
}

func settingsJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	return append(out, '\n')
}

// recordStart advises a run whose project root and engine home are both
// resolved — what setupViaCells advises for a binding with config_home:
// project.
func recordStart() present.Start {
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: "/proj"},
		EngineHome:  present.Root{Host: recordHome},
	}))
}

// recordFixture seeds an in-memory engine home with userSettings and
// redirects the record store into the same in-memory fs, returning the fs,
// the target path and the store directory.
func recordFixture(t *testing.T) (afero.Fs, string, string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	const recordsDir = "/home/.ctxloom/records"
	t.Cleanup(paths.SetHomeRecordsDirForTesting(recordsDir))
	target := filepath.Join(recordHome, SettingsFileName)
	testsupport.WriteFile(t, fs, target, userSettings(t), 0o644)
	return fs, target, recordsDir
}

// recordFiles lists the *.hew-record.yaml files in dir.
func recordFiles(t *testing.T, fs afero.Fs, dir string) []string {
	t.Helper()
	entries, err := afero.ReadDir(fs, dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".hew-record.yaml") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// eventHookCommands flattens settings.json's hooks[event] into the command
// strings it carries, in order.
func eventHookCommands(t *testing.T, raw []byte, event string) []string {
	t.Helper()
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(raw, &s))
	var out []string
	for _, m := range s.Hooks[event] {
		for _, h := range m.Hooks {
			out = append(out, h.Command)
		}
	}
	return out
}

// The record write is the second implementation of the writer seam: it
// PATCHES the engine-home settings through hew, leaving the user's own
// entries byte-for-byte, and it writes ONE §9.7 record naming the engine-home
// target, the resolved ops it applied, and a reversal hew can parse. Cleanup
// applies that reversal, restoring the seeded bytes exactly, and leaves a
// record whose applied set is empty — how Status tells "took its entries
// back out" from "never wrote".
func TestSettingsRecord_Deliver_PatchesEngineHomeSettingsAndWritesTheRecord(t *testing.T) {
	fs, target, recordsDir := recordFixture(t)
	seeded, err := afero.ReadFile(fs, target)
	require.NoError(t, err)

	in := sampleInputs()
	in.DenyTools = []string{"Task"}
	a, ok := Surfaces.Construct(agent.SurfaceSettings, ApproachHewRecord, in, fs)
	require.True(t, ok, "claude declares settings=hew-record")

	p := a.Present(recordStart())
	assert.Equal(t, target, p.HostPath, "the presenter lands the bytes beneath the ENGINE HOME, not the project root")
	assert.Empty(t, p.Args, "no launch flag: claude reads its config home's settings.json natively")

	d, err := a.Deliver(recordStart())
	require.NoError(t, err)
	require.NotNil(t, d, "something was written, so a cleanup handle is owed")

	// (1) the TARGET: the user's entries survive, ctxloom's are added.
	after, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	assert.Equal(t, []string{"echo user"}, eventHookCommands(t, after, "PreToolUse"), "the user's own hook is preserved")
	assert.Equal(t, []string{"ctxloom hook inject-context"}, eventHookCommands(t, after, "SessionStart"), "ctxloom's hook is added")
	var parsed struct {
		Model       string                    `json:"model"`
		StatusLine  *struct{ Command string } `json:"statusLine"`
		Permissions struct{ Deny []string }   `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(after, &parsed))
	assert.Equal(t, "opus", parsed.Model, "a key ctxloom knows nothing about is untouched")
	require.NotNil(t, parsed.StatusLine)
	assert.Equal(t, "my-status", parsed.StatusLine.Command, "the user's own statusline wins, as it does for the plain writer")
	assert.ElementsMatch(t, []string{"Bash(rm -rf *)", "Task"}, parsed.Permissions.Deny, "the user's deny entry survives beside ctxloom's")

	// (2) the RECORD's bytes.
	files := recordFiles(t, fs, recordsDir)
	require.Len(t, files, 1, "exactly one record for one delivery")
	raw, err := afero.ReadFile(fs, files[0])
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(raw), "hew-record: 1\n"), "the version key is first: %q", firstLine(raw))
	var rec confpatch.Record
	require.NoError(t, yamlv3.Unmarshal(raw, &rec))
	require.Len(t, rec.Targets, 1)
	assert.Equal(t, target, rec.Targets[0].Target, "the record is keyed by the engine-home target")
	assert.Equal(t, "json", rec.Targets[0].Format)
	assert.True(t, rec.Targets[0].Committed)
	assert.NotEqual(t, rec.Targets[0].Before, rec.Targets[0].After, "before/after digests differ for a write that changed the file")
	assert.NotEmpty(t, rec.Targets[0].Transforms, "the resolved ops ctxloom applied are recorded")
	paths := make([]string, 0, len(rec.Targets[0].Transforms))
	for _, op := range rec.Targets[0].Transforms {
		paths = append(paths, op.Path)
	}
	assert.NotContains(t, paths, "/statusLine", "nothing was recorded against the user's statusline")
	assert.Contains(t, paths, "/hooks/SessionStart", "ctxloom's event is created whole beside the user's")
	assert.Condition(t, func() bool {
		for _, p := range paths {
			if strings.HasPrefix(p, "/permissions/deny/") {
				return true
			}
		}
		return false
	}, "an op landed beneath /permissions/deny: %v", paths)
	_, err = hew.ParseSingle([]byte(rec.Reversal))
	require.NoError(t, err, "the stored reversal is patch text hew can parse")

	// (3) CLEANUP applies the reversal: the seeded bytes come back exactly,
	// and the newest record's applied set is empty.
	require.NoError(t, d.Cleanup())
	restored, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	require.NotEmpty(t, seeded, "comparing two empty reads would be trivially identical")
	assert.Equal(t, string(seeded), string(restored), "cleanup restores the user's bytes exactly")
	store, err := confpatch.NewStore(fs, recordsDir)
	require.NoError(t, err)
	last, found, err := store.Last(target)
	require.NoError(t, err)
	require.True(t, found, "the reversal-only apply leaves a record behind")
	require.Len(t, last.Targets, 1)
	assert.Empty(t, last.Targets[0].Transforms, "after cleanup the record says ctxloom applied nothing")
}

func firstLine(b []byte) string {
	if i := strings.IndexByte(string(b), '\n'); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// A record delivery on a run whose engine home was never advised — no
// binding, or one that keeps the real host home — is REFUSED with
// ErrUnrootedEngineHome. Nothing is written anywhere: not a relative
// "settings.json", not the user's real ~/.claude, and no record.
func TestSettingsRecord_Deliver_RefusesAnUnresolvedEngineHome(t *testing.T) {
	fs := afero.NewMemMapFs()
	const recordsDir = "/home/.ctxloom/records"
	t.Cleanup(paths.SetHomeRecordsDirForTesting(recordsDir))
	a, ok := Surfaces.Construct(agent.SurfaceSettings, ApproachHewRecord, sampleInputs(), fs)
	require.True(t, ok)

	d, err := a.Deliver(present.ProjectOnHost("/proj"))
	require.ErrorIs(t, err, agent.ErrUnrootedEngineHome)
	assert.Nil(t, d)

	var written []string
	require.NoError(t, afero.Walk(fs, "/", func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			written = append(written, path)
		}
		return nil
	}))
	assert.Empty(t, written, "a refused delivery writes nothing")
}

// The declaration: unsafe-file stays the settings DEFAULT (every existing
// launch and every at-rest install is byte-identical), hew-record is a NAMED
// second approach a binding selects, and the two are told apart by where
// their presenters land the bytes — the plain writer under the project root,
// the record writer not.
func TestSurfaces_SettingsDefaultStaysUnsafeFile_HewRecordIsNamed(t *testing.T) {
	def, ok := Surfaces.Default(agent.SurfaceSettings)
	require.True(t, ok)
	assert.Equal(t, agent.ApproachUnsafeFile, def)
	assert.Contains(t, Surfaces.Names(agent.SurfaceSettings), ApproachHewRecord)

	fs := afero.NewMemMapFs()
	record, ok := Surfaces.Construct(agent.SurfaceSettings, ApproachHewRecord, sampleInputs(), fs)
	require.True(t, ok)
	_, isRecord := record.(*settingsRecord)
	assert.True(t, isRecord, "hew-record constructs the record writer, not the plain one with a flag")
	assert.False(t, agent.PresentsUnderProjectRoot(record), "the record writer is not a project file")

	plain, ok := Surfaces.Construct(agent.SurfaceSettings, agent.ApproachUnsafeFile, sampleInputs(), fs)
	require.True(t, ok)
	assert.True(t, agent.PresentsUnderProjectRoot(plain), "the plain writer is")
}

// Two deliveries in a row reconcile through the record: the second reverses
// the first before applying, so ctxloom's entries appear ONCE, not twice —
// the property that lets a coordinator and its in-tree child share one
// engine home.
func TestSettingsRecord_Deliver_TwiceDoesNotDuplicate(t *testing.T) {
	fs, target, recordsDir := recordFixture(t)
	a, ok := Surfaces.Construct(agent.SurfaceSettings, ApproachHewRecord, sampleInputs(), fs)
	require.True(t, ok)

	for i := 0; i < 2; i++ {
		_, err := a.Deliver(recordStart())
		require.NoError(t, err, "delivery %d", i+1)
	}
	after, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	assert.Equal(t, []string{"ctxloom hook inject-context"}, eventHookCommands(t, after, "SessionStart"), "ctxloom's hook is present exactly once")
	assert.Equal(t, []string{"echo user"}, eventHookCommands(t, after, "PreToolUse"), "the user's hook is present exactly once")
	assert.Len(t, recordFiles(t, fs, recordsDir), 1, "only the newest record is live")
}

// The ordinary first write: the instance home is seeded with credentials
// only, so there is NO settings.json yet. The record writer creates it, and
// cleanup takes every ctxloom entry back out of it.
func TestSettingsRecord_Deliver_CreatesTheFileWhenTheHomeHasNone(t *testing.T) {
	fs := afero.NewMemMapFs()
	const recordsDir = "/home/.ctxloom/records"
	t.Cleanup(paths.SetHomeRecordsDirForTesting(recordsDir))
	target := filepath.Join(recordHome, SettingsFileName)
	a, ok := Surfaces.Construct(agent.SurfaceSettings, ApproachHewRecord, sampleInputs(), fs)
	require.True(t, ok)

	d, err := a.Deliver(recordStart())
	require.NoError(t, err)
	require.NotNil(t, d)
	after, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	assert.Equal(t, []string{"ctxloom hook inject-context"}, eventHookCommands(t, after, "SessionStart"))
	var parsed struct {
		StatusLine *struct{ Command string } `json:"statusLine"`
	}
	require.NoError(t, json.Unmarshal(after, &parsed))
	require.NotNil(t, parsed.StatusLine, "the managed statusline is set where none was")
	assert.True(t, agent.IsManaged(parsed.StatusLine.Command, "ctxloom"), "the statusline is ctxloom's: %q", parsed.StatusLine.Command)
	assert.Len(t, recordFiles(t, fs, recordsDir), 1)

	require.NoError(t, d.Cleanup())
	restored, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(restored, &doc))
	assert.Empty(t, doc, "cleanup leaves an empty document: nothing of ctxloom's remains, and nothing was there before")
}

// hew cannot reversibly record a hook group inserted beside an EXISTING
// keyless group for the same event (its index-addressed removal is a form
// its own parser refuses). The writer refuses that case up front with the
// remedy, writes nothing, and records nothing — never a write with an undo
// that does not undo.
func TestSettingsRecord_Deliver_RefusesToInsertBesideAnExistingHookEvent(t *testing.T) {
	fs := afero.NewMemMapFs()
	const recordsDir = "/home/.ctxloom/records"
	t.Cleanup(paths.SetHomeRecordsDirForTesting(recordsDir))
	target := filepath.Join(recordHome, SettingsFileName)
	seeded := settingsJSON(t, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo user"}}}},
		},
	})
	testsupport.WriteFile(t, fs, target, seeded, 0o644)
	a, ok := Surfaces.Construct(agent.SurfaceSettings, ApproachHewRecord, sampleInputs(), fs)
	require.True(t, ok)

	d, err := a.Deliver(recordStart())
	require.ErrorIs(t, err, errHooksBesideExisting)
	assert.Contains(t, err.Error(), "SessionStart", "the refusal names the event")
	assert.Contains(t, err.Error(), "settings=unsafe-file", "the refusal names the remedy")
	assert.Nil(t, d)
	after, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	assert.Equal(t, string(seeded), string(after), "a refused write leaves the file exactly as found")
	assert.Empty(t, recordFiles(t, fs, recordsDir), "a refused write leaves no record")
}

// End to end through the real backend: a launch whose binding selects
// settings=hew-record and whose run env carries CLAUDE_CONFIG_DIR (the
// controlled home operations.InTreeAgentHomeEnv contributes) lands
// ctxloom's settings beneath THAT directory, and nothing in the project
// tree — the launch backend advised the engine home from the var claude
// declared. Without the var, the same selection refuses the launch.
func TestSetup_SettingsHewRecord_LandsUnderClaudeConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(paths.SetHomeRecordsDirForTesting(t.TempDir()))
	work := t.TempDir()
	configDir := filepath.Join(t.TempDir(), "state", "h", "home", "claude")
	managed := &agent.ManagedConfig{
		Surfaces: map[agent.SurfaceKind]string{agent.SurfaceSettings: ApproachHewRecord},
		Hooks:    &wire.HooksConfig{Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}}}},
	}

	backend := NewClaudeCode()
	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       map[string]string{ConfigDirEnv: configDir},
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		Managed:   managed,
		CellKind:  agent.CellKindDirectoryIsolated,
	}))
	t.Cleanup(func() { _ = backend.Cleanup(context.Background()) })

	raw, err := os.ReadFile(filepath.Join(configDir, SettingsFileName))
	require.NoError(t, err, "the settings landed beneath CLAUDE_CONFIG_DIR")
	assert.Equal(t, []string{"ctxloom hook inject-context"}, eventHookCommands(t, raw, "SessionStart"))
	assert.NoFileExists(t, ProjectSettingsPath(work), "nothing was written to the project's own settings")

	refused := NewClaudeCode()
	err = refused.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       map[string]string{},
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		Managed:   managed,
		CellKind:  agent.CellKindDirectoryIsolated,
	})
	require.ErrorIs(t, err, agent.ErrUnrootedEngineHome, "no controlled home: the record write refuses rather than guessing")
}
