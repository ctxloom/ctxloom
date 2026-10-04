package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	base, err := DefaultOutputBase(filepath.Join(home, "Documents"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "Documents", OutputDirName), base)
	_, err = DefaultOutputBase("")
	assert.Error(t, err, "no Documents folder is no base")
}
