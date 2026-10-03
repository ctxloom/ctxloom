package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUserDirsDocuments(t *testing.T) {
	home := "/home/u"
	cases := []struct {
		name, content, want string
		ok                  bool
	}{
		{"home-relative", "# comment\nXDG_DESKTOP_DIR=\"$HOME/Desktop\"\nXDG_DOCUMENTS_DIR=\"$HOME/Dokumente\"\n", "/home/u/Dokumente", true},
		{"absolute", "XDG_DOCUMENTS_DIR=\"/data/docs\"\n", "/data/docs", true},
		{"disabled is $HOME itself", "XDG_DOCUMENTS_DIR=\"$HOME/\"\n", "", false},
		{"absent", "XDG_MUSIC_DIR=\"$HOME/Music\"\n", "", false},
		{"relative is not a path the spec allows", "XDG_DOCUMENTS_DIR=\"docs\"\n", "", false},
		{"unquoted", "XDG_DOCUMENTS_DIR=$HOME/D\n", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseUserDirsDocuments(c.content, home)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestOutputDir_IsBaseProjectHarp(t *testing.T) {
	got, err := OutputDir("/base", "/src/github/acme/widget", "fond-ugly-cycle")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/base", "widget", "fond-ugly-cycle"), got,
		"plain project name: same-named repos share a project folder, harps keep sessions apart")
}

func TestOutputDir_RefusesWhatCannotBeASegment(t *testing.T) {
	_, err := OutputDir("/base", "/", "fond-ugly-cycle")
	assert.Error(t, err, "a project dir with no name")
	_, err = OutputDir("/base", "/src/widget", "../escape")
	assert.Error(t, err, "a harp that traverses")
	_, err = OutputDir("", "/src/widget", "fond-ugly-cycle")
	assert.Error(t, err, "no base")
}

func TestDefaultOutputBase_IsUnderTheSandboxedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	base, err := DefaultOutputBase()
	require.NoError(t, err)
	assert.Equal(t, OutputDirName, filepath.Base(base))
}
