// Package fakeclock is a manual clock for tests of code that takes its time
// from an injected Now/AfterFunc pair (termui.Clock is one): time moves only
// on Advance, which runs every timer that has come due, in deadline order, on
// the caller's goroutine — so a test forces each interleaving instead of
// sleeping into one.
package fakeclock

import (
	"sort"
	"sync"
	"time"
)

// Epoch is where a New clock starts.
var Epoch = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// Clock is the manual clock. The zero value is not usable; call New.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*timer
}

type timer struct {
	at   time.Time
	seq  int
	f    func()
	done bool
}

// New returns a clock standing at Epoch.
func New() *Clock { return &Clock{now: Epoch} }

// Now is the clock's current time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc arms f to run d after now; stop disarms it and reports whether it
// had not yet fired.
func (c *Clock) AfterFunc(d time.Duration, f func()) (stop func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &timer{at: c.now.Add(d), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		stopped := !t.done
		t.done = true
		return stopped
	}
}

// Pending reports how many timers are armed and not yet fired or stopped.
func (c *Clock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.done {
			n++
		}
	}
	return n
}

// Advance moves time forward by d, firing due timers one at a time (a timer a
// fired callback schedules inside the window fires too). Timers due at the
// same instant fire in the order they were armed.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		next := c.nextDueLocked(end)
		if next == nil {
			c.now = end
			c.mu.Unlock()
			return
		}
		next.done = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

func (c *Clock) nextDueLocked(end time.Time) *timer {
	live := c.timers[:0]
	for _, t := range c.timers {
		if !t.done {
			live = append(live, t)
		}
	}
	c.timers = live
	sort.Slice(live, func(i, j int) bool {
		if live[i].at.Equal(live[j].at) {
			return live[i].seq < live[j].seq
		}
		return live[i].at.Before(live[j].at)
	})
	if len(live) == 0 || live[0].at.After(end) {
		return nil
	}
	return live[0]
}
