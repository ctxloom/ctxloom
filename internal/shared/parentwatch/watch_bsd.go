//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package parentwatch

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// WithParent returns a context cancelled when this process's parent exits,
// watched with a kqueue EVFILT_PROC/NOTE_EXIT registration on the parent pid.
//
// Arming is ordered so the parent cannot slip away unobserved: a parent of 1
// means we were already reparented (the parent is gone); a registration that
// fails with ESRCH means the parent exited before we got there; and getppid is
// read again after registering, because a parent that died between the first
// read and the registration has reparented us and the pid we registered on
// may already belong to someone else. Each of those cancels at once.
//
// The watch is one goroutine blocked in kevent for the rest of the process's
// life — call this once per process. The returned CancelFunc releases the
// context, not the watch. A non-nil error means the kqueue could not be armed
// and the parent is NOT being watched; the returned context is still usable.
func WithParent(ctx context.Context) (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(ctx)
	ppid := unix.Getppid()
	if ppid == 1 {
		cancel()
		return ctx, cancel, nil
	}
	kq, err := unix.Kqueue()
	if err != nil {
		return ctx, cancel, fmt.Errorf("parentwatch: kqueue: %w", err)
	}
	unix.CloseOnExec(kq)
	var ev unix.Kevent_t
	unix.SetKevent(&ev, ppid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	ev.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		_ = unix.Close(kq)
		if errors.Is(err, unix.ESRCH) {
			cancel()
			return ctx, cancel, nil
		}
		return ctx, cancel, fmt.Errorf("parentwatch: watch parent %d: %w", ppid, err)
	}
	if unix.Getppid() != ppid {
		_ = unix.Close(kq)
		cancel()
		return ctx, cancel, nil
	}
	go awaitExit(kq, cancel)
	return ctx, cancel, nil
}

// awaitExit blocks until the registered NOTE_EXIT fires, then cancels.
// EINTR is a signal landing on this thread, not an event; any other error
// means the watch is broken, and cancelling is the side that fails safe.
func awaitExit(kq int, cancel context.CancelFunc) {
	defer func() { _ = unix.Close(kq) }()
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, nil, events, nil)
		if errors.Is(err, unix.EINTR) || (err == nil && n == 0) {
			continue
		}
		cancel()
		return
	}
}
