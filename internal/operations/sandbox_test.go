package operations

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/pidalive"
)

// sandboxRootName is the directory (under os.TempDir()) that groups every
// operations-package test process's sandbox by pid. One process = one pid =
// one TestMain invocation, so a single subdirectory per pid is enough to hold
// both the HOME and CWD sandboxes for that run, and a single RemoveAll of the
// pid directory tears both down together.
const sandboxRootName = "ctxloom-optest"

// maxOrphanAge is how long a sandbox directory may sit unreaped before it is
// reclaimed regardless of whether its pid currently resolves to a live
// process. No real test run of this package takes anywhere near this long; it
// exists only to bound the rare case where a dead run's pid gets recycled by
// an unrelated process before the liveness-only reap gets a chance to run.
const maxOrphanAge = 2 * time.Hour

// reapSandboxes removes sandbox directories left behind by processes that can
// no longer be running, without ever touching a directory that a CONCURRENT
// sibling process might still be using — gremlins runs several mutant test
// binaries in parallel, so this must be provably safe under that concurrency.
//
// A directory is only removed when its owning pid is verifiably dead (an
// ESRCH-equivalent signal-0 probe, not a heuristic), or when it is older than
// maxOrphanAge regardless of pid liveness. The age fallback exists because a
// dead pid can be recycled by an unrelated, currently-alive process, which
// would otherwise make an orphaned directory look permanently "live" and
// never get reclaimed; recycling within maxOrphanAge is implausible on any
// machine this runs on. Neither path can misfire against a live sibling: a
// pid that is genuinely still running never satisfies either condition
// within the time it takes that sibling's own test run to finish, and a
// signal-0 probe cannot report "dead" for a process that is in fact alive.
//
// A directory named after THIS process's own pid is a special case: since
// this call always runs before this process creates its own directory, any
// existing entry under our pid cannot be ours — it is a leftover from
// whichever earlier process last held this (now recycled) pid, and that
// process is, by definition, not us and not running as this pid anymore, so
// it is always safe to remove.
func reapSandboxes(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return // nothing to reap yet (root doesn't exist) or unreadable; best-effort
	}
	self := os.Getpid()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not one of our pid-named directories; leave it alone
		}
		path := filepath.Join(root, e.Name())
		if pid == self {
			_ = os.RemoveAll(path)
			continue
		}
		info, statErr := e.Info()
		orphanedByAge := statErr == nil && time.Since(info.ModTime()) > maxOrphanAge
		if !pidalive.Alive(pid) || orphanedByAge {
			_ = os.RemoveAll(path)
		}
	}
}

// acquireSandbox reaps whatever dead runs left behind, then stakes out this
// process's own pid-scoped sandbox directory. The returned cleanup removes
// the whole directory (both HOME and CWD sandboxes live under it); on the
// normal exit path that single deferred call is enough. On a panic, tRunner's
// re-panic tears down the process before any defer runs, so cleanup never
// fires — the NEXT process to call acquireSandbox reaps it instead via the
// pid-liveness check above, since a panicked process's pid is dead the
// instant the process exits.
func acquireSandbox() (dir string, cleanup func()) {
	root := filepath.Join(os.TempDir(), sandboxRootName)
	_ = os.MkdirAll(root, 0o700)
	reapSandboxes(root)

	dir = filepath.Join(root, strconv.Itoa(os.Getpid()))
	_ = os.MkdirAll(dir, 0o700)
	return dir, func() { _ = os.RemoveAll(dir) }
}
