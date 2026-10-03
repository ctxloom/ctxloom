// Package linux is the platform behaviour of a Linux host, and of any other
// freedesktop (XDG) host the composition point gives it to.
package linux

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/platform/posix"
)

// OS is a Linux host's platform behaviour.
type OS struct{ posix.Linker }

// DocumentsDir is the XDG Documents folder: XDG_DOCUMENTS_DIR from
// $XDG_CONFIG_HOME/user-dirs.dirs (default ~/.config), else ~/Documents.
// Read from the file rather than by running xdg-user-dir, which is not
// installed everywhere and would make path resolution spawn a process.
func (OS) DocumentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	if data, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs")); err == nil {
		if dir, ok := parseUserDirsDocuments(string(data), home); ok {
			return dir, nil
		}
	}
	return filepath.Join(home, "Documents"), nil
}

// runtimeDirEnv names the user's per-session tmpfs (XDG base dirs).
const runtimeDirEnv = "XDG_RUNTIME_DIR"

// PrivateTmpfs is $XDG_RUNTIME_DIR, which the XDG spec makes a per-user,
// owner-only, memory-backed dir; absent when the session sets none.
func (OS) PrivateTmpfs(getenv func(string) string) (string, bool) {
	dir := getenv(runtimeDirEnv)
	return dir, dir != ""
}

// userDirsDocumentsKey is the user-dirs.dirs variable naming the Documents
// folder (freedesktop xdg-user-dirs).
const userDirsDocumentsKey = "XDG_DOCUMENTS_DIR"

// parseUserDirsDocuments reads XDG_DOCUMENTS_DIR out of a user-dirs.dirs
// file. The format allows exactly two value shapes, both double-quoted:
// "$HOME/<rel>" and an absolute path. "$HOME/" alone is how the format
// DISABLES a folder, so it is reported absent, as is any other shape.
func parseUserDirsDocuments(content, home string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		val, found := strings.CutPrefix(line, userDirsDocumentsKey+"=")
		if !found {
			continue
		}
		if len(val) < 2 || val[0] != '"' || val[len(val)-1] != '"' {
			return "", false
		}
		val = val[1 : len(val)-1]
		if rel, ok := strings.CutPrefix(val, "$HOME/"); ok {
			if rel == "" {
				return "", false
			}
			return filepath.Join(home, filepath.FromSlash(rel)), true
		}
		if filepath.IsAbs(val) {
			return filepath.Clean(val), true
		}
		return "", false
	}
	return "", false
}
