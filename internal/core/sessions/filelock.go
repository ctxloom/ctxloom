package sessions

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// WithFileLock runs fn as ONE serialized read-modify-write transaction
// against target — an engine-owned settings file OUTSIDE any .ctxloom tree
// (a real home ~/.claude/settings.json, a project's .mcp.json, ...). Hooks,
// the MCP server, the CLI, the runner and an in-container ctxloom (the same
// files bind-mounted) all read-modify-write these files concurrently; two
// racing RMWs is a lost update on a file ctxloom does not own.
//
// locks are the caller's Root's (safefs.Root.Locks), paired with the fs fn
// reads and writes target through, so the lock is taken on the filesystem
// the transaction runs on; it is never skipped, and a failure to take it
// fails the call without running fn. The lock lives under the ctxloom home
// at paths.HomePathFor(target), never beside the file: a sidecar for a file
// this project does NOT own left untracked lock litter in every project and
// a ctxloom-owned file inside the user's real engine home, a directory
// ctxloom otherwise never writes to. See paths.HomePathFor's doc for the
// full reasoning.
func WithFileLock(locks safefs.Locks, target string, fn func() error) error {
	lockPath, err := paths.HomePathFor(target)
	if err != nil {
		return fmt.Errorf("sessions: deriving home lock path for %s: %w", target, err)
	}
	return safefs.WithLock(locks, lockPath, fn)
}
