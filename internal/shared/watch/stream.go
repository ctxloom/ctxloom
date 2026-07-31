package watch

import (
	"context"
	"errors"
	"time"
)

// Stream drives w until ctx is cancelled, w's events close, or emit or the
// watcher errors, calling emit once per debounced burst. It emits once up
// front, before any change, so a subscriber renders current state with no
// separate initial-query race. debounce coalesces the several filesystem
// events one logical change produces; it is the caller's because that depends
// on what is writing. This is the whole loop a consumer needs — only the emit
// closure and the watcher's root/filter are domain-specific.
// debounceCeiling is how many debounce intervals a burst may withhold an emit
// for before one is forced. A debounce answers "has the writing stopped?";
// without a ceiling it also answers "never" for a writer that never stops.
const debounceCeiling = 10

// maxDebounceWait is the longest a stream may go without emitting while
// events keep arriving. Derived from the caller's debounce rather than a
// fixed duration, so a caller that chose a coarse debounce gets a
// proportionally coarse ceiling and neither caller has a second knob to set.
func maxDebounceWait(debounce time.Duration) time.Duration {
	return debounceCeiling * debounce
}

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
				// The event channel closes for two very different reasons.
				// A caller-requested Close is a clean end. Anything else is
				// the underlying watcher dying, after which this stream can
				// never deliver another event — and returning nil there
				// reports a successful, complete watch to a subscriber that
				// will now wait forever.
				if w.stopped() {
					return nil
				}
				return errors.New("watch: the filesystem watcher stopped delivering events; no further changes can be reported")
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
