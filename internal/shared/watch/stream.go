package watch

import (
	"context"
	"time"
)

// Stream drives w until ctx is cancelled, w's event channel closes, or emit or
// the watcher reports an error, calling emit once per debounced burst of
// filesystem events. It emits once up front, before any change, so a
// subscriber renders current state immediately with no separate
// initial-query race.
//
// debounce coalesces the burst a single logical change produces — a write plus
// a chmod, lock-file churn, an editor's write-to-temp-and-rename — into one
// emit. It is the caller's, because how long a burst lasts depends on what is
// writing.
//
// This is the whole watch loop every consumer needs: the domain-specific part
// is the emit closure (which event shape goes on the wire) and the watcher's
// root/filter, nothing else. A consumer writing its own loop is how one copy
// gains a fix the other does not.
func Stream(ctx context.Context, w *Watcher, debounce time.Duration, emit func() error) error {
	if err := emit(); err != nil {
		return err
	}
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-w.Events():
			if !ok {
				return nil
			}
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
			} else {
				timer.Reset(debounce)
			}
		case <-timerC:
			if err := emit(); err != nil {
				return err
			}
		case err := <-w.Errors():
			if err != nil {
				return err
			}
		}
	}
}
