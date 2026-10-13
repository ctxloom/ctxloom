package kit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

const sampleContext = "# Rules\nthe secret color is vermilion"

func framedApproach() Approach {
	return Approach{Engine: "eng", ApproachName: "ctx", Private: true,
		T: present.Traits{Roots: []present.RootKind{present.RootSessionHome, present.RootProjectRoot}, Channel: present.ChannelArgv}}
}

func homeStart(project, home string) present.Start {
	return present.New(present.OnHost(present.Paths{ProjectRoot: present.Root{Host: project}, SessionHome: present.Root{Host: home}}))
}

var hashedFlag = FramedFile{Name: HashedName(".ctx.md"), Announce: func(r present.Rooted) present.Rooted { return r.AnnounceFlag("--ctx-file") }}

// The framed context lands beneath the session home under its content
// hash, declared and announced; the project root receives nothing.
func TestFramedFile_WritesTheFramedContextBeneathTheSessionHome(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	d, err := hashedFlag.Deliver(framedApproach(), homeStart(project, home), []byte(sampleContext), afero.NewOsFs())
	require.NoError(t, err)

	require.Len(t, d.Files, 1)
	path := d.Files[0]
	assert.Equal(t, home, filepath.Dir(path))
	assert.True(t, strings.HasSuffix(path, ".ctx.md"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, agent.FrameProjectContext(sampleContext), string(data))
	assert.Equal(t, path, d.Presented.HostPath)
	assert.Equal(t, []string{"--ctx-file", path}, d.Presented.Args)
	assert.Empty(t, dirNames(t, project))
}

// The hashed leaf is a function of the framed bytes: the same context, the
// same file; different context, a different one.
func TestFramedFile_HashedNameIsDeterministic(t *testing.T) {
	home := t.TempDir()
	deliver := func(text string) string {
		d, err := hashedFlag.Deliver(framedApproach(), homeStart(t.TempDir(), home), []byte(text), afero.NewOsFs())
		require.NoError(t, err)
		return d.Files[0]
	}
	assert.Equal(t, deliver("stable"), deliver("stable"))
	assert.NotEqual(t, deliver("stable"), deliver("changed"))
}

// A rewrite keeps the file's existing mode (safefs.WriteFileKeepMode), where
// a raw write at 0644 would reset it.
func TestFramedFile_RewriteKeepsTheExistingMode(t *testing.T) {
	home := t.TempDir()
	f := FramedFile{Name: FixedName("AGENTS.md")}
	path := filepath.Join(home, "AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte("stale"), 0o640))

	_, err := f.Deliver(framedApproach(), homeStart(t.TempDir(), home), []byte(sampleContext), afero.NewOsFs())
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	fileperm.Equal(t, 0o640, info.Mode())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, agent.FrameProjectContext(sampleContext), string(data))
}

// Empty context writes nothing and presents nothing: no channel may name a
// file that was not written.
func TestFramedFile_EmptyContextWritesAndPresentsNothing(t *testing.T) {
	home := t.TempDir()
	d, err := hashedFlag.Deliver(framedApproach(), homeStart(t.TempDir(), home), nil, afero.NewOsFs())
	require.NoError(t, err)
	assert.Equal(t, present.Delivered{}, d)
	assert.Empty(t, dirNames(t, home))
}

// A run with no session home is refused, empty context or not, and nothing
// falls back to the project root.
func TestFramedFile_RefusesWithoutASessionHome(t *testing.T) {
	project := t.TempDir()
	for _, text := range []string{sampleContext, ""} {
		d, err := hashedFlag.Deliver(framedApproach(), homeStart(project, ""), []byte(text), afero.NewOsFs())
		require.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
		assert.Equal(t, present.Delivered{}, d)
	}
	assert.Empty(t, dirNames(t, project))
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
