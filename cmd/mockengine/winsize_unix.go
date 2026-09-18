//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// resizeNotifications turns SIGWINCH into the Runtime's resize channel: on
// each signal the CURRENT geometry is read back off tty's own descriptor
// (TIOCGWINSZ), so what the mock reports is what the kernel says the terminal
// is, not what the signal implied. Latest-wins: the channel holds one
// pending size and a newer one replaces it, because a burst of resizes only
// ever matters by its final geometry. Returns nil when tty is not a terminal
// — a nil channel never fires, which is the right silence for a session that
// has no window to resize.
func resizeNotifications(tty *os.File) <-chan agent.WindowSize {
	fd := int(tty.Fd())
	if _, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err != nil {
		return nil
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	out := make(chan agent.WindowSize, 1)
	go func() {
		for range sig {
			ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
			if err != nil {
				continue
			}
			size := agent.WindowSize{Rows: ws.Row, Cols: ws.Col}
			select {
			case out <- size:
			default:
				// Replace the stale pending size; the single producer means
				// the drain-then-send cannot race another sender.
				select {
				case <-out:
				default:
				}
				out <- size
			}
		}
	}()
	return out
}
