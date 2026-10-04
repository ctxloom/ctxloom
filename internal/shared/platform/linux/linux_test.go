package linux

import (
	"os"
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

func TestDocumentsDir_ReadsUserDirsElseHomeDocuments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, "cfg")
	t.Setenv("XDG_CONFIG_HOME", cfg)

	got, err := OS{}.DocumentsDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "Documents"), got, "no user-dirs.dirs: ~/Documents")

	require.NoError(t, os.MkdirAll(cfg, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte("XDG_DOCUMENTS_DIR=\"$HOME/Dokumente\"\n"), 0o600))
	got, err = OS{}.DocumentsDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "Dokumente"), got)
}

func TestPrivateTmpfs_IsTheXDGRuntimeDir(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	_, ok := OS{}.PrivateTmpfs(getenv)
	assert.False(t, ok, "unset: none")
	env["XDG_RUNTIME_DIR"] = "/run/user/1000"
	dir, ok := OS{}.PrivateTmpfs(getenv)
	assert.True(t, ok)
	assert.Equal(t, "/run/user/1000", dir)
}
