package termui

import (
	"context"
	"io"
	"sync"
	"time"
)

// Summon presents a summoned (code-opened) overlay: full screen, inert while
// arming. It returns nil once the overlay has the screen, ctx's error when
// ctx ends first, ErrUIUnavailable when the layer is degraded or closed (or
// closes while waiting), and ErrOverlayEngaged — after telling that overlay
// notice (Notify) — when one is already up: focus never moves from an
// overlay the human is using. The notice is the caller's because only the
// caller knows who is asking.
//
// It takes the screen only when stdin has been quiet for Present.QuietFor,
// at an input boundary (not mid-sequence, not inside a paste), with no
// teardown in flight. There is no deadline that forces it: a human typing
// without pause keeps the terminal, and the request is signalled by the bar
// and bell instead.
func (c *Controller) Summon(ctx context.Context, start OverlayStart, notice Notice) error {
	start.Summoned = true
	for {
		if c.unavailable() {
			return ErrUIUnavailable
		}
		changed := c.changed.wait()
		if e := c.engaged(); e != nil {
			e.ov.Notify(notice)
			return ErrOverlayEngaged
		}
		shown, retry, err := c.trySummon(start)
		if shown || err != nil {
			return err
		}
		if err := c.await(ctx, changed, retry); err != nil {
			return err
		}
	}
}

// trySummon makes one attempt. Not shown and no error: retry after retry (0 =
// when the terminal layer's state next changes).
func (c *Controller) trySummon(start OverlayStart) (shown bool, retry time.Duration, err error) {
	if !c.session.TryLock() {
		return false, 0, nil
	}
	e, err := c.prepare(start)
	if e == nil {
		c.session.Unlock()
		if err != nil {
			c.degrade(err)
		}
		return false, 0, ErrUIUnavailable
	}
	pr, pw := io.Pipe()
	retry, ok := c.ic.engageExternal(pw, c.present.QuietFor, c.present.ArmFor, e.gen)
	if !ok {
		c.session.Unlock()
		return false, retry, nil
	}
	c.show(e, pr)
	return true, 0, nil
}

// await blocks until the layer's state changes, retry elapses (if non-zero),
// ctx ends, or the controller closes.
func (c *Controller) await(ctx context.Context, changed <-chan struct{}, retry time.Duration) error {
	var timer <-chan struct{}
	if retry > 0 {
		fired := make(chan struct{})
		stop := c.clock.AfterFunc(retry, func() { close(fired) })
		defer stop()
		timer = fired
	}
	select {
	case <-changed:
	case <-timer:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrUIUnavailable
	}
	return nil
}

// startArming begins a summoned engagement's inert window at its first frame.
func (c *Controller) startArming(e *engagement) {
	if until, ok := c.ic.armFrame(e.gen); ok {
		c.armAt(e, until)
	}
}

func (c *Controller) armAt(e *engagement, until time.Time) {
	c.clock.AfterFunc(until.Sub(c.clock.Now()), func() { c.armCheck(e) })
}

// armCheck ends the window, or re-schedules it when a key typed during it
// moved its end.
func (c *Controller) armCheck(e *engagement) {
	armed, discarded, until, ok := c.ic.tryArm(e.gen)
	switch {
	case !ok:
	case armed:
		e.ov.Armed(discarded)
	default:
		c.armAt(e, until)
	}
}

// frameWatch reports an overlay's first write to the terminal: the moment
// the modal is on screen, which is when arming starts to count.
type frameWatch struct {
	w     io.Writer
	once  sync.Once
	first func()
}

func (f *frameWatch) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	f.once.Do(f.first)
	return n, err
}

// signal is a broadcast: wait returns a channel that the next fire closes.
type signal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (s *signal) wait() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

func (s *signal) fire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch != nil {
		close(s.ch)
		s.ch = nil
	}
}
