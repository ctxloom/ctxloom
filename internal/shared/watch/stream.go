package watch

import (
	"context"
	"errors"
	"time"
)

// debounceCeiling is how many debounce intervals a burst may withhold an emit
// for before one is forced. A debounce answers "has the writing stopped?";
// with no ceiling it also answers "never" for a source that never stops.
const debounceCeiling = 10

// maxDebounceWait is the longest a stream may go without emitting while events
// keep arriving. Derived from the caller's debounce rather than being a fixed
// duration, so a caller that chose a coarse debounce gets a proportionally
// coarse ceiling and neither caller has a second knob to set.
func maxDebounceWait(debounce time.Duration) time.Duration {
	return debounceCeiling * debounce
}

// Stream drives w until ctx is cancelled, w's events close, or emit or the
// watcher errors, calling emit once per debounced burst. It emits once up
// front, before any change, so a subscriber renders current state with no
// separate initial-query race. debounce coalesces the several filesystem
// events one logical change produces; it is the caller's because that depends
// on what is writing. This is the whole loop a consumer needs — only the emit
// closure and the watcher's root/filter are domain-specific.
//
// A burst is bounded at both ends. The debounce says "the writing has
// stopped"; maxDebounceWait says "the writing has gone on long enough that
// the subscriber needs to hear something regardless". Without the second, a
// source changing faster than the debounce pushes the deadline forward on
// every event and the subscriber hears NOTHING for as long as the writing
// lasts — silence exactly when there is the most to report.
func Stream(ctx context.Context, w *Watcher, debounce time.Duration, emit func() error) error {
	if err := emit(); err != nil {
		return err
	}

	var (
		debounced, ceiling   *time.Timer
		debouncedC, ceilingC <-chan time.Time
	)
	// endBurst disarms both timers and forgets them, so the next event starts
	// a fresh burst with a fresh ceiling. Deferred as well as called on each
	// emit: a stream that ends mid-burst (ctx cancelled, watcher died) must
	// not leave timers running behind it.
	endBurst := func() {
		if debounced != nil {
			debounced.Stop()
			debounced, debouncedC = nil, nil
		}
		if ceiling != nil {
			ceiling.Stop()
			ceiling, ceilingC = nil, nil
		}
	}
	defer endBurst()

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
			if debounced == nil {
				debounced = time.NewTimer(debounce)
				debouncedC = debounced.C
				// Armed once, at the START of a burst, and deliberately never
				// Reset by the events that follow — resetting it would make it
				// a second debounce rather than a ceiling.
				ceiling = time.NewTimer(maxDebounceWait(debounce))
				ceilingC = ceiling.C
			} else {
				debounced.Reset(debounce)
			}
		case <-debouncedC:
			endBurst()
			if err := emit(); err != nil {
				return err
			}
		case <-ceilingC:
			endBurst()
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
