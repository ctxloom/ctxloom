// Package darwin is the platform behaviour of a macOS host.
package darwin

import (
	"context"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/platform/posix"
)

// OS is a macOS host's platform behaviour.
type OS struct{ posix.Linker }

// DocumentsDir is ~/Documents: macOS has no per-user relocation of it.
func (OS) DocumentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents"), nil
}

// PrivateTmpfs is absent: macOS offers no per-user memory-backed dir, and an
// XDG_RUNTIME_DIR a user exported there promises nothing about its backing.
func (OS) PrivateTmpfs(func(string) string) (string, bool) { return "", false }

// Notify shows nothing: a macOS notifier is not built.
func (OS) Notify(context.Context, func(string) (string, error), string, string) error { return nil }
