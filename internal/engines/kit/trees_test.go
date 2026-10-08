package kit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

func identity(c agent.CommandExport) (string, []byte, error) {
	return c.Name + ".md", []byte(c.Content), nil
}

// TestDeliverCommands_WritesEnabledRenderedSkipsUnsafe: every enabled
// command rendered under the selected root's dir and declared; a disabled
// one and a traversal name are not written; the dir is announced.
func TestDeliverCommands_WritesEnabledRenderedSkipsUnsafe(t *testing.T) {
	start, home, _ := bothStart(t)
	a := Approach{ApproachName: "cmds", T: bothRoots, Private: true}
	in := engine.CommandsInputs{Commands: []engine.CommandExport{
		{Name: "go", Body: []byte("GO"), Enabled: true},
		{Name: "off", Body: []byte("OFF")},
		{Name: "../escape", Body: []byte("X"), Enabled: true},
	}}
	d, err := DeliverCommands(a, start, present.RootSessionHome, "commands", "p/commands", safefs.New(), in, identity,
		func(r present.Rooted) present.Rooted { return r.AnnounceFlag("--cmds") })
	require.NoError(t, err)
	dir := filepath.Join(home, "commands")
	assert.Equal(t, dir, d.Presented.HostPath)
	assert.Equal(t, []string{"--cmds", dir}, d.Presented.Args)
	assert.Equal(t, []string{filepath.Join(dir, "go.md")}, d.Files)
	b, err := os.ReadFile(filepath.Join(dir, "go.md"))
	require.NoError(t, err)
	assert.Equal(t, "GO", string(b))
	assert.NoFileExists(t, filepath.Join(dir, "off.md"))
	assert.NoFileExists(t, filepath.Join(home, "escape.md"))
}

// TestDeliverCommands_RefusesThroughTheApproach: the root rules are
// Approach.Rooted's (an unrooted private home writes nothing).
func TestDeliverCommands_RefusesThroughTheApproach(t *testing.T) {
	fs := afero.NewMemMapFs()
	a := Approach{ApproachName: "cmds", T: bothRoots, Private: true}
	_, err := DeliverCommands(a, present.ProjectOnHost("/p"), present.RootSessionHome, "c", "c", safefs.NewMem(fs),
		engine.CommandsInputs{Commands: []engine.CommandExport{{Name: "go", Enabled: true}}}, identity, nil)
	require.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
}

// TestDeliverSkills_AcceptedPackagesAtTheirDeclaredModes: each accepted,
// enabled package under <dir>/<name>/, an undeclared mode at 0644, a
// declared exec bit kept; accept filters before anything is written.
func TestDeliverSkills_AcceptedPackagesAtTheirDeclaredModes(t *testing.T) {
	start, _, project := bothStart(t)
	a := Approach{ApproachName: "skills", T: bothRoots}
	in := engine.SkillsInputs{Skills: []engine.SkillExport{
		{Name: "keep", Enabled: true, Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: []byte("K")}, {Path: "run.sh", Bytes: []byte("#!"), Mode: 0o755}}},
		{Name: "drop", Enabled: true, Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: []byte("D")}}},
	}}
	var seen []string
	accept := func(s []agent.SkillExport, _ report.Sink) []agent.SkillExport {
		var out []agent.SkillExport
		for _, e := range s {
			seen = append(seen, e.Name)
			if e.Name == "keep" {
				out = append(out, e)
			}
		}
		return out
	}
	d, err := DeliverSkills(a, start, present.RootProjectRoot, "skills", "p/skills", safefs.New(), in, accept, nil)
	require.NoError(t, err)
	dir := filepath.Join(project, "p", "skills")
	assert.Equal(t, dir, d.Presented.HostPath)
	assert.Empty(t, d.Presented.Args, "no announce")
	assert.Equal(t, []string{"keep", "drop"}, seen)
	assert.ElementsMatch(t, []string{filepath.Join(dir, "keep", "SKILL.md"), filepath.Join(dir, "keep", "run.sh")}, d.Files)
	for rel, want := range map[string]os.FileMode{"keep/SKILL.md": 0o644, "keep/run.sh": 0o755} {
		info, err := os.Stat(filepath.Join(dir, rel))
		require.NoError(t, err)
		assert.Equal(t, want, info.Mode().Perm(), rel)
	}
	assert.NoDirExists(t, filepath.Join(dir, "drop"))
}

// TestDeliverSkills_ASkippedPackageIsReportedOnTheInputsSink: a package the
// writer refuses (a traversal name from bundle content) is not written, and
// the refusal reaches the sink the inputs carry — the operator learns a skill
// they enabled never arrived.
func TestDeliverSkills_ASkippedPackageIsReportedOnTheInputsSink(t *testing.T) {
	start, _, project := bothStart(t)
	a := Approach{ApproachName: "skills", T: bothRoots}
	var got []string
	in := engine.SkillsInputs{
		Skills: []engine.SkillExport{
			{Name: "../escape", Enabled: true, Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: []byte("X")}}},
		},
		Report: report.SinkFunc(func(f report.Finding) { got = append(got, f.Text) }),
	}

	d, err := DeliverSkills(a, start, present.RootProjectRoot, "skills", "p/skills", safefs.New(), in, nil, nil)

	require.NoError(t, err)
	assert.Empty(t, d.Files)
	assert.NoFileExists(t, filepath.Join(project, "p", "escape", "SKILL.md"))
	require.Len(t, got, 1)
	assert.Contains(t, got[0], `skipping package "../escape"`)
}
