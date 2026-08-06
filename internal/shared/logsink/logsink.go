// Package logsink opens the file every ctxloom process writes its STRUCTURED
// log to: ~/.ctxloom/logs/ctxloom.log.
//
// This is the debugging channel, and it is deliberately not stderr. A ctxloom
// process is very often a machine callback whose stderr belongs to the calling
// engine's protocol — Claude Code renders a SessionStart hook's stderr as an
// error, and a statusline command's stderr lands on the terminal outside the
// alt-screen, where it destroys scrollback and copy. Structured zap output on
// that surface does not inform anyone; it corrupts the session it was reporting
// on, once per assistant message in the statusline's case.
//
// The HUMAN-readable channel (clidiag) is a different thing and stays on
// stderr, hooks included: those messages are written for a person, they say
// what to do about the problem, and a hook that quietly did nothing must still
// be able to say so out loud.
package logsink

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// MaxBytes is the size at which the log is rolled aside at open. Bounded
// because the busiest ctxloom process is the statusline hook, which runs once
// per assistant message: a config that warns on every load turns an unbounded
// file into a disk-filling loop nobody is watching.
const MaxBytes = 8 << 20

// Open returns the process log open for append, creating ~/.ctxloom/logs and
// rolling an oversized log aside first.
//
// The caller owns the returned file and must close it. Every ctxloom process
// holds it for its whole lifetime, so this is deliberately not a cached
// singleton: two opens of the same path both append, which is what O_APPEND is
// for.
func Open() (*os.File, error) {
	path, err := paths.HomeLogFilePath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create the log directory %s: %w", filepath.Dir(path), err)
	}
	rollIfOversized(path, MaxBytes)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open the log file %s: %w", path, err)
	}
	return f, nil
}

// rollIfOversized renames an over-limit log to <path>.1, keeping exactly one
// generation. Best effort throughout: a log that cannot be rolled is still a
// log worth appending to, so every failure here leaves the existing file in
// place rather than blocking the run. Concurrent ctxloom processes may race to
// roll the same file; rename is atomic, so the loser rolls an already-rolled
// (near-empty) file at worst.
func rollIfOversized(path string, max int64) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < max {
		return
	}
	_ = os.Rename(path, path+".1")
}
