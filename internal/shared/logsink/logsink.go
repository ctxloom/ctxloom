// Package logsink opens the file a ctxloom-family binary writes its STRUCTURED
// log to: ~/.ctxloom/logs/<prog>.log (paths.HomeLogFilePath).
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
	"sync"

	"go.uber.org/zap/zapcore"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// MaxBytes is the size at which the log is rolled aside at open. Bounded
// because the busiest ctxloom process is the statusline hook, which runs once
// per assistant message: a config that warns on every load turns an unbounded
// file into a disk-filling loop nobody is watching.
const MaxBytes = 8 << 20

// Open returns prog's log open for append, creating ~/.ctxloom/logs and
// rolling an oversized log aside first.
//
// The caller owns the returned file and must close it. A process holds it for
// its whole lifetime, so this is deliberately not a cached singleton: two opens
// of the same path both append, which is what O_APPEND is for.
func Open(prog string) (*os.File, error) {
	path, err := paths.HomeLogFilePath(prog)
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

// Lazy returns a sink over prog's log that does not touch the filesystem until
// its first Write: that is when it opens the file (through Open, so the roll
// and the directory creation happen then too). A process that logs nothing
// creates nothing — not the file, not the logs directory — and a Sync before
// any write is a no-op for the same reason. This is what lets a binary that
// runs as a hook before every agent shell command install a logger at all: an
// eager open would be a write on the hook path on every run, for no record.
//
// An open that fails is reported ONCE on stderr and every write then fails
// with its error. The report cannot go through zap: zap's own error channel is
// this sink, so without the stderr line a lost log is indistinguishable from a
// process that had nothing to say. The open is not retried, so a process that
// logs often does not repeat the report per record.
func Lazy(prog string) zapcore.WriteSyncer {
	return &lazyFile{prog: prog}
}

// lazyFile is Lazy's sink. mu guards the one-shot open and the file it yields.
type lazyFile struct {
	prog string

	mu     sync.Mutex
	opened bool
	f      *os.File
	err    error
}

func (l *lazyFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.opened {
		l.opened = true
		l.f, l.err = Open(l.prog)
		if l.err != nil {
			fmt.Fprintf(os.Stderr, "%s: warning: could not open the log (%v); continuing without it\n", l.prog, l.err)
		}
	}
	if l.err != nil {
		return 0, l.err
	}
	return l.f.Write(p)
}

func (l *lazyFile) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	return l.f.Sync()
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
