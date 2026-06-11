// Package selfexec resolves the path to use when re-invoking the running
// ctxloom binary (spawning `ctxloom llm serve`, `ctxloom session distill`,
// `ctxloom run`, ...). It exists because the obvious os.Executable() answer
// goes stale after an in-place upgrade: a long-running process (e.g. the MCP
// server) would exec "/path/ctxloom (deleted)" and fail.
package selfexec

import (
	"os"
	"strings"
)

// osExecutable and osStat are seams the tests override to drive the Path
// decision tree without depending on the real filesystem or binary path.
var (
	osExecutable = os.Executable
	osStat       = os.Stat
)

// Path returns the path to use when re-invoking ctxloom from inside a running
// ctxloom process. Prefers the OS-reported absolute path (one syscall:
// `readlink /proc/self/exe` on Linux, `_NSGetExecutablePath` on macOS,
// `GetModuleFileNameW` on Windows), and falls back to bare `"ctxloom"` (a PATH
// lookup) in two cases:
//
//  1. The OS call itself errored (rare; AIX, some sandboxed environments).
//  2. The returned path no longer points at a live binary. On Linux,
//     `os.Executable()` returns a literal `"<path> (deleted)"` string after
//     the binary is replaced via unlink+recreate (the typical `go install`
//     upgrade pattern). exec'ing that path fails at .Run() time, which is
//     recoverable but ugly. We strip the suffix, stat the result, and only
//     use it if the file still exists.
func Path() string {
	const fallback = "ctxloom"
	exe, err := osExecutable()
	if err != nil {
		return fallback
	}
	// Linux: "/path/ctxloom (deleted)" after the running inode is unlinked.
	if trimmed, ok := strings.CutSuffix(exe, " (deleted)"); ok {
		exe = trimmed
	}
	if _, err := osStat(exe); err != nil {
		return fallback
	}
	return exe
}
