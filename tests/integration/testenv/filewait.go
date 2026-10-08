package testenv

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/watch"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// AwaitFileContaining blocks until a file named name anywhere under root
// contains want, or testsupport.Expiry(t) fires. It returns the path that matched.
//
// It wakes on filesystem events, never on a timer: the watch is armed before
// the first look, so a write that lands between the two is still seen — by
// the look itself, or by the event it raises.
func AwaitFileContaining(t *testing.T, root, name, want string) (string, bool) {
	t.Helper()
	w, err := watch.New(root, true, func(p string) bool { return filepath.Base(p) == name })
	require.NoError(t, err, "watch %s", root)
	defer func() { _ = w.Close() }()

	expired := testsupport.Expiry(t)
	for {
		if path, ok := fileContaining(root, name, want); ok {
			return path, true
		}
		select {
		case <-w.Events():
		case err := <-w.Errors():
			require.NoError(t, err, "watch %s", root)
		case <-expired:
			return "", false
		}
	}
}

// fileContaining reports the first file named name under root whose content
// contains want.
func fileContaining(root, name, want string) (string, bool) {
	var found string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != name {
			return nil
		}
		if b, readErr := os.ReadFile(path); readErr == nil && strings.Contains(string(b), want) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found, found != ""
}
