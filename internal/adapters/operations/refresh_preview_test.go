package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// The post-sync refresh note names only what a --yes would change
// (wieldable-squander, decision #3): the dry run previews the delivery
// against the project as it stands instead of listing every routed kind.

// refreshProject is a hooked project at CTXLOOM_ROOT with the real
// Materialize behind the post-sync step.
type refreshProject struct {
	cfg  *config.Config
	root string
}

func newRefreshProject(t *testing.T) refreshProject {
	t.Helper()
	cfg := hookedMaterializeCfg(t, "REFRESH-MARK", "")
	root := t.TempDir()
	t.Setenv("CTXLOOM_ROOT", root)
	orig := syncHooksStep
	t.Cleanup(func() { syncHooksStep = orig })
	syncHooksStep = Materialize
	return refreshProject{cfg: cfg, root: root}
}

// pull runs the post-sync steps of a pull, with --yes when write.
func (p refreshProject) pull(t *testing.T, write bool) *SyncDependenciesResult {
	t.Helper()
	res := &SyncDependenciesResult{Total: 1}
	runSyncPostSteps(context.Background(), engines.Registry(), p.cfg, SyncDependenciesRequest{ApplyHooks: true, WriteProject: write}, res, afero.NewOsFs())
	return res
}

// setHook changes the team hook the project's profile carries: a dependency
// that changed one kind.
func (p refreshProject) setHook(t *testing.T, command string) {
	t.Helper()
	appPaths := p.cfg.ToFixture().AppPaths
	testsupport.WriteFileString(t, afero.NewOsFs(), filepath.Join(bundletree.ProjectProfilesDir(t, appPaths[0]), "reviewer.yaml"),
		"select_tags:\n  - security\nhooks:\n  unified:\n    session_start:\n      - type: command\n        command: "+command+"\n", 0o644)
}

func (p refreshProject) settingsPath() string {
	return filepath.Join(p.root, ".claude", "settings.json")
}

// A pull right after a --yes pull prints no refresh note: nothing would
// change. MUTATION: the dry run lists every routed kind again → red.
func TestRefresh_APullRightAfterAYesPullSkipsNothing(t *testing.T) {
	p := newRefreshProject(t)
	require.Nil(t, p.pull(t, true).RefreshSkipped)

	again := p.pull(t, false)

	assert.Nil(t, again.RefreshSkipped, "%+v", again.RefreshSkipped)
	var out bytes.Buffer
	WriteRefreshSkipped(&out, "  ", again.RefreshSkipped, "ctxloom deps pull --yes")
	assert.Empty(t, out.String())
}

// A pull after a dependency changed one kind names exactly that kind.
func TestRefresh_APullAfterADepChangedOneKindNamesExactlyIt(t *testing.T) {
	p := newRefreshProject(t)
	p.pull(t, true)
	p.setHook(t, "echo team-guardrail-v2")

	res := p.pull(t, false)

	require.NotNil(t, res.RefreshSkipped)
	require.Len(t, res.RefreshSkipped.Engines, 1)
	e := res.RefreshSkipped.Engines[0]
	assert.Equal(t, "claude-code", e.Engine)
	assert.Equal(t, []string{"hooks"}, e.Wrote)
	assert.Empty(t, e.Released)
	assert.Empty(t, e.Refused)
}

// A hand edit a --yes would undo is reported: the user took ctxloom's hook
// out of settings.json, and the refresh would put it back.
func TestRefresh_AHandEditTheRefreshWouldUndoIsReported(t *testing.T) {
	p := newRefreshProject(t)
	p.pull(t, true)
	raw, err := os.ReadFile(p.settingsPath())
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(raw, &settings))
	require.Contains(t, settings, "hooks")
	delete(settings, "hooks")
	edited, err := json.Marshal(settings)
	require.NoError(t, err)
	testsupport.WriteFileString(t, afero.NewOsFs(), p.settingsPath(), string(edited), 0o644)

	res := p.pull(t, false)

	require.NotNil(t, res.RefreshSkipped)
	require.Len(t, res.RefreshSkipped.Engines, 1)
	assert.Equal(t, []string{"hooks"}, res.RefreshSkipped.Engines[0].Wrote)
}

// A refusal the refresh would meet is a line in the note, with its fix, and
// the pull still succeeds: no failure is recorded.
func TestRefresh_ARefusalIsANoteLineAndThePullSucceeds(t *testing.T) {
	p := newRefreshProject(t)
	p.pull(t, true)
	testsupport.WriteFileString(t, afero.NewOsFs(), p.settingsPath(), "{ not json", 0o644)

	mark := strictness.Checkpoint()
	res := p.pull(t, false)

	require.Empty(t, strictness.Since(mark), "a refusal the preview finds is no failure of the pull")
	require.NotNil(t, res.RefreshSkipped)
	require.Len(t, res.RefreshSkipped.Engines, 1)
	refused := res.RefreshSkipped.Engines[0].Refused
	require.NotEmpty(t, refused)
	kinds := []string{}
	for _, r := range refused {
		kinds = append(kinds, r.Kind)
		assert.NotEmpty(t, r.Reason)
		assert.NotEmpty(t, r.Fix)
	}
	assert.Contains(t, kinds, "hooks")
	var out bytes.Buffer
	WriteRefreshSkipped(&out, "  ", res.RefreshSkipped, "ctxloom deps pull --yes")
	assert.Contains(t, out.String(), "claude-code: hooks refused: ")
	assert.Contains(t, out.String(), "fix: ")
}

// The preview writes nothing: after a --yes pull and a changed dependency,
// a pull without --yes leaves every file under the project and the home —
// engine files, ownership records, claimant markers — byte for byte as it
// found them, and creates none.
func TestRefresh_APullWithoutYesWritesNothing(t *testing.T) {
	p := newRefreshProject(t)
	p.pull(t, true)
	p.setHook(t, "echo team-guardrail-v2")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	before := snapshot(t, p.root, home)

	res := p.pull(t, false)

	require.NotNil(t, res.RefreshSkipped, "the preview found the change")
	assert.Equal(t, before, snapshot(t, p.root, home))
}

// snapshot is every regular file under dirs with its bytes.
func snapshot(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range dirs {
		require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				out[path] = string(b)
			} else if d.IsDir() {
				out[path+"/"] = ""
			}
			return nil
		}))
	}
	return out
}

// The note's refusal lines: each refused kind with why and its fix, under
// its engine; a skip with only refusals offers no apply line, since applying
// would meet the same refusal.
func TestWriteRefreshSkipped_NamesEachRefusalWithItsFix(t *testing.T) {
	var buf bytes.Buffer
	WriteRefreshSkipped(&buf, "  ", &MaterializeResult{Target: "/proj", Engines: []EngineOutcome{
		{Engine: "claude-code", Wrote: []string{"settings"}, Refused: []KindRefusal{{Kind: "hooks", Reason: "settings.json: not json", Fix: "repair the file"}}},
	}}, "ctxloom deps pull --yes")
	assert.Equal(t, "  Project refresh skipped: without --yes nothing was written into /proj.\n"+
		"    claude-code: would write settings\n"+
		"    claude-code: hooks refused: settings.json: not json\n"+
		"      fix: repair the file\n"+
		"    Apply it with 'ctxloom deps pull --yes', or 'ctxloom materialize --yes --surface settings'.\n",
		buf.String())

	var only bytes.Buffer
	WriteRefreshSkipped(&only, "  ", &MaterializeResult{Target: "/proj", Engines: []EngineOutcome{
		{Engine: "claude-code", Refused: []KindRefusal{{Kind: "hooks", Reason: "not json", Fix: "repair the file"}}},
	}}, "ctxloom deps pull --yes")
	assert.Equal(t, "  Project refresh skipped: without --yes nothing was written into /proj.\n"+
		"    claude-code: hooks refused: not json\n"+
		"      fix: repair the file\n",
		only.String())
}

// refreshPlansAnything is true for a refusal alone: the note must say it.
func TestRefreshPlansAnything_ARefusalAloneIsWorthANote(t *testing.T) {
	assert.False(t, refreshPlansAnything(&MaterializeResult{Engines: []EngineOutcome{{Engine: "claude-code"}}}))
	assert.True(t, refreshPlansAnything(&MaterializeResult{Engines: []EngineOutcome{{Engine: "claude-code", Refused: []KindRefusal{{Kind: "hooks"}}}}}))
}
