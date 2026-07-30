package watch

import (
	"context"
	"time"
)

// Stream drives w until ctx is cancelled, w's events close, or emit or the
// watcher errors, calling emit once per debounced burst. It emits once up
// front, before any change, so a subscriber renders current state with no
// separate initial-query race. debounce coalesces the several filesystem
// events one logical change produces; it is the caller's because that depends
// on what is writing. This is the whole loop a consumer needs — only the emit
// closure and the watcher's root/filter are domain-specific.
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
