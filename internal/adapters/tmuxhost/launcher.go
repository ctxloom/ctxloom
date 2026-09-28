package tmuxhost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// launcher is one window's launch: the script tmux runs, which carries the
// ARGV, and the feed that hands the script its ENVIRONMENT.
type launcher struct {
	path string
	feed *envFeed
}

// writeLauncher writes a small shell script carrying the launch's ARGV, and
// hands it the launch's ENVIRONMENT through a FIFO the script reads.
//
// It exists because `tmux new-window`'s command line is not an ordinary argv:
// the client packs it into a fixed-size buffer and refuses anything longer with
// "command too long". Spelling the launch inline made that line grow with two
// unbounded inputs at once — one `-e` per environment variable (a desktop
// session carries a hundred), and the command's own arguments, which for an
// engine include a multi-kilobyte prompt. Routing both off the command line
// makes it CONSTANT-SIZE, so no input can overflow it.
//
// The environment goes through a FIFO, never the file: it carries the
// engine's credentials (the token or key its auth mode resolved to, and
// whatever the human's own environment holds), and a file would leave them on
// disk until it was removed — for good, if this process died first. A FIFO
// holds no bytes on disk; the feed writes into the kernel pipe once the
// script opens it, and the name is unlinked as soon as both ends are open.
func writeLauncher(dir, name string, env map[string]string, command string, args []string) (launcher, error) {
	if command == "" {
		return launcher{}, fmt.Errorf("launcher: no command given")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return launcher{}, fmt.Errorf("launcher: create %s: %w", dir, err)
	}
	var e strings.Builder
	// Sorted so the stream is deterministic for identical input.
	for _, k := range collections.SortedKeys(env) {
		fmt.Fprintf(&e, "export %s=%s\n", k, shellQuote(env[k]))
	}
	fifo := filepath.Join(dir, "ctxloom-env-"+name+".fifo")
	feed, err := startEnvFeed(fifo, []byte(e.String()))
	if err != nil {
		return launcher{}, fmt.Errorf("launcher: environment channel %s: %w", fifo, err)
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	// eval of cat, not `.`: macOS's /bin/sh is bash 3.2, whose `.` reads
	// exactly the file's st_size bytes — zero for a FIFO — so it would
	// source nothing and the program would start without its environment.
	b.WriteString("eval \"$(cat " + shellQuote(fifo) + ")\"\n")
	// exec, so the hosted program REPLACES this shell: the wrapper waits on
	// the process it started and reports its exit status, and an extra shell
	// in between would report its own.
	b.WriteString("exec " + shellQuote(command))
	for _, a := range args {
		b.WriteString(" " + shellQuote(a))
	}
	b.WriteString("\n")

	path := filepath.Join(dir, "ctxloom-launch-"+name+".sh")
	// 0o700: the wrapper execs this file, so it must carry the exec bit, and
	// nothing but its owner has any business running it.
	if err := iox.WriteFileAtomic(path, []byte(b.String()), 0o700); err != nil {
		feed.release()
		return launcher{}, fmt.Errorf("launcher: write %s: %w", path, err)
	}
	return launcher{path: path, feed: feed}, nil
}

// envFeed delivers one launch's environment through a FIFO: the writer
// blocks until the launcher script opens the read end, writes, and closes.
type envFeed struct {
	fifo string
	done chan struct{}
}

func startEnvFeed(fifo string, content []byte) (*envFeed, error) {
	if err := mkfifo(fifo, 0o600); err != nil {
		return nil, err
	}
	f := &envFeed{fifo: fifo, done: make(chan struct{})}
	go func() {
		defer close(f.done)
		w, err := iox.OpenFIFOWriter(fifo)
		_ = os.Remove(fifo)
		if err != nil {
			return
		}
		_, _ = w.Write(content)
		_ = w.Close()
	}()
	return f, nil
}

// release ends a feed whose script never read it (a window that was never
// created, or killed first): it opens and closes the read end so the blocked
// writer's open returns and its write fails, then removes the FIFO. The
// writer is this feed's own goroutine and reaches its open at once, so the
// retry only covers the instant before it does.
func (f *envFeed) release() {
	if f == nil {
		return
	}
	for {
		select {
		case <-f.done:
			_ = os.Remove(f.fifo)
			return
		default:
		}
		if r, err := os.OpenFile(f.fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = r.Close()
		}
		select {
		case <-f.done:
		case <-time.After(10 * time.Millisecond):
		}
	}
}
