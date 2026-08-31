package acpagent

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// fsUpstreamDirPrefix names the per-session temp directory startFsUpstream
// mints to hold its socket. Declared once and used by BOTH the creator and the
// reaper: a reaper keyed on a hand-copied prefix silently stops matching the
// moment the creator's literal changes, and nothing would fail to say so.
const fsUpstreamDirPrefix = "ctxloom-acp-fs-"

// fsUpstreamSockName is the socket inside each of those directories.
const fsUpstreamSockName = "fs.sock"

// fsUpstreamReapGrace is how recently a directory may have been touched and
// still be spared. A listener that has been created but has not bound yet is
// indistinguishable from an abandoned one — both refuse a connection — so
// without a grace window the reaper would delete the socket path out from
// under a process that is still starting.
const fsUpstreamReapGrace = 2 * time.Minute

// FsUpstreamReapResult reports what one sweep did. Counted rather than
// returned as paths: the caller is a startup path that logs a summary, and a
// list of directories nobody reads is a bigger surface than the number.
type FsUpstreamReapResult struct {
	Reaped  int // confirmed dead, removed
	Spared  int // something is listening — a live session
	Skipped int // ambiguous, or inside the grace window: LEFT ALONE
}

// ReapStaleFsUpstreams removes fs-upstream directories whose socket is dead.
//
// WHY THIS EXISTS AT ALL, given fsUpstreamListener.Close already removes its
// own directory: Close is not always REACHED. Its only caller is
// teardownSession, via closeAllSessions, which runs as
// `defer s.closeAllSessions()` inside Serve — and a defer does not run when the
// process is killed. Being killed is the NORMAL case here: an editor spawns
// `ctxloom acp serve` as its own child and kills it. Measured before this
// existed: 2,571 orphaned directories in TMPDIR, oldest two weeks old, newest
// minutes old.
//
// A context trigger would not have been enough — no in-process mechanism
// survives SIGKILL — so this is a sweep at startup, the same shape as
// mcp.reapStaleDiscoveryMarkers and isolation.ReapOrphanedWorktrees.
//
// FAULT-TOLERANT BY CONTRACT: every failure warns and is counted as skipped.
// A startup sweep must never be able to stop the agent starting; leaving
// litter is strictly better than refusing to run.
//
// now is injected so the grace window is testable without sleeping.
func ReapStaleFsUpstreams(root string, now time.Time) FsUpstreamReapResult {
	var res FsUpstreamReapResult
	if root == "" {
		root = os.TempDir()
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		// Includes "root does not exist", which is not worth a warning: there
		// is simply nothing to sweep.
		if !os.IsNotExist(err) {
			clidiag.Warn("ctxloom", "acp fs-upstream reap: %v", err)
		}
		return res
	}

	for _, e := range entries {
		// Ours by PREFIX, and a DIRECTORY. A file that happens to match the
		// prefix is not one of ours, and this runs in a shared tmpdir where
		// anything not ours must be left exactly as it is.
		if !strings.HasPrefix(e.Name(), fsUpstreamDirPrefix) || !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())

		info, err := e.Info()
		if err != nil {
			// Unreadable means UNKNOWN, and unknown must never authorise a
			// delete.
			res.Skipped++
			continue
		}
		if now.Sub(info.ModTime()) < fsUpstreamReapGrace {
			res.Skipped++
			continue
		}

		switch fsUpstreamState(filepath.Join(dir, fsUpstreamSockName)) {
		case fsUpstreamLive:
			res.Spared++
		case fsUpstreamDead:
			if err := os.RemoveAll(dir); err != nil {
				clidiag.Warn("ctxloom", "acp fs-upstream reap: %s: %v", dir, err)
				res.Skipped++
				continue
			}
			res.Reaped++
		default:
			res.Skipped++
		}
	}
	return res
}

type fsUpstreamLiveness int

const (
	fsUpstreamUnknown fsUpstreamLiveness = iota
	fsUpstreamLive
	fsUpstreamDead
)

// fsUpstreamState probes one socket by DIALING it, which is the only thing
// that distinguishes a listener from a leftover inode — the socket file exists
// in both cases, so its presence proves nothing.
//
// A successful dial is the sole evidence of life, and anything else that is not
// a clean refusal is reported as UNKNOWN rather than dead: a permission error
// or an unexpected failure mode is not proof that nobody is there, and this
// function's answer authorises a delete.
func fsUpstreamState(sock string) fsUpstreamLiveness {
	if _, err := os.Stat(sock); err != nil {
		if os.IsNotExist(err) {
			// The directory outlived its socket: nothing can be listening.
			return fsUpstreamDead
		}
		return fsUpstreamUnknown
	}
	c, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err == nil {
		_ = c.Close()
		return fsUpstreamLive
	}
	// Refused/unreachable on a unix socket that EXISTS means the listener is
	// gone. A timeout is deliberately NOT treated as dead: a busy listener can
	// fail to accept in time, and reaping it would break a live session.
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return fsUpstreamUnknown
	}
	return fsUpstreamDead
}
