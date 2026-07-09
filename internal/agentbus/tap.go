package agentbus

import (
	"context"
	"errors"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is the live-observation broadcast seam: the orchestrator tees each
// child's ChatEvent stream through a TapHub so N observers can subscribe by
// harp (the socket's observe verb) without perturbing delivery. Only CHILDREN
// are tappable — the orchestrator never drives its own serving session's
// engine, so there is no stream to tee for the coordinator itself.

// observerRingSize bounds one observer's buffer: a stalled observer costs at
// most this many buffered events before drop-oldest kicks in (the gap is
// reported, never silent — see TapObserver.Take).
const observerRingSize = 256

// ErrNotLive rejects an observe of a harp this orchestrator holds no live
// event stream for (never spawned here, or its engine ended). The caller's
// fallback is the store tail.
var ErrNotLive = errors.New("observe: session is not live in this orchestrator; tail its transcript instead")

// TapHub registers one live tap per child harp. The orchestrator calls Tee
// where it would otherwise consume the launch's event channel; observers
// subscribe by harp and read at their own pace.
type TapHub struct {
	ringSize int

	mu   sync.Mutex
	taps map[string]*tap
}

// NewTapHub builds an empty hub with the default per-observer buffer.
func NewTapHub() *TapHub {
	return &TapHub{ringSize: observerRingSize, taps: make(map[string]*tap)}
}

// Tee interposes on a child's event stream: the returned channel carries every
// event of in, in order, for the orchestrator's own consumption, while
// subscribed observers receive copies from subscribe-time forward (history is
// the store's job). A re-Tee of the same harp (resume) replaces the ended
// predecessor's registration.
func (h *TapHub) Tee(harp string, in <-chan agent.ChatEvent) <-chan agent.ChatEvent {
	t := &tap{observers: make(map[*TapObserver]struct{})}
	h.mu.Lock()
	h.taps[harp] = t
	h.mu.Unlock()

	out := make(chan agent.ChatEvent)
	go func() {
		defer func() {
			h.detach(harp, t)
			t.end()
			close(out)
		}()
		for ev := range in {
			// INVARIANT (never block delivery): a slow or stuck observer must
			// never delay the orchestrator's event consumption, turn handling,
			// or bus delivery. publish is a non-blocking push into bounded
			// per-observer rings (drop-oldest + gap accounting); only the
			// orchestrator's own read of out exerts backpressure — exactly the
			// pace it had reading the launch channel directly.
			t.publish(ev)
			out <- ev
		}
	}()
	return out
}

// Subscribe attaches an observer to harp's live tap, delivering events from
// now forward. ErrNotLive when no live stream is held for harp.
func (h *TapHub) Subscribe(harp string) (*TapObserver, error) {
	h.mu.Lock()
	t := h.taps[harp]
	h.mu.Unlock()
	if t == nil {
		return nil, ErrNotLive
	}
	ob := &TapObserver{
		tap:  t,
		ring: make([]agent.ChatEvent, h.ringSize),
		wake: make(chan struct{}, 1),
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrNotLive
	}
	t.observers[ob] = struct{}{}
	t.mu.Unlock()
	return ob, nil
}

// detach unregisters t, unless a resume already replaced it with a fresh tap.
func (h *TapHub) detach(harp string, t *tap) {
	h.mu.Lock()
	if h.taps[harp] == t {
		delete(h.taps, harp)
	}
	h.mu.Unlock()
}

// tap is one harp's live registration: the observer set for a single teed
// stream. closed flips when the source channel closes (the child ended).
type tap struct {
	mu        sync.Mutex
	observers map[*TapObserver]struct{}
	closed    bool
}

// publish pushes one event to every observer, never blocking (bounded rings).
func (t *tap) publish(ev agent.ChatEvent) {
	t.mu.Lock()
	for ob := range t.observers {
		ob.push(ev)
	}
	t.mu.Unlock()
}

// end marks the stream over for every observer (their Take drains what is
// buffered, then reports the end).
func (t *tap) end() {
	t.mu.Lock()
	t.closed = true
	for ob := range t.observers {
		ob.end()
	}
	t.mu.Unlock()
}

// TapObserver is one subscription: a bounded ring the tap pushes into and the
// observer drains via Take. Overflow drops the OLDEST buffered events and
// counts them, so a lagging viewer sees fresh events plus an explicit gap.
type TapObserver struct {
	tap *tap

	mu      sync.Mutex
	ring    []agent.ChatEvent
	head, n int
	dropped int // evicted since the last Take — the pending gap
	ended   bool
	wake    chan struct{} // 1-buffered: coalesced "ring changed" signal
}

// push appends ev, evicting the oldest event when the ring is full. Never
// blocks (the seam's invariant).
func (o *TapObserver) push(ev agent.ChatEvent) {
	o.mu.Lock()
	if o.n == len(o.ring) {
		o.head = (o.head + 1) % len(o.ring)
		o.n--
		o.dropped++
	}
	o.ring[(o.head+o.n)%len(o.ring)] = ev
	o.n++
	o.mu.Unlock()
	o.signal()
}

func (o *TapObserver) end() {
	o.mu.Lock()
	o.ended = true
	o.mu.Unlock()
	o.signal()
}

func (o *TapObserver) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Take blocks until events are buffered, the live stream ends, or ctx is
// done. dropped is how many events this observer missed immediately before
// events[0] (ring overflow) — the caller renders it as an explicit gap
// marker. ok=false (with err nil) means the stream ended: the child's engine
// exited or was torn down; nothing more will arrive.
func (o *TapObserver) Take(ctx context.Context) (dropped int, events []agent.ChatEvent, ok bool, err error) {
	for {
		o.mu.Lock()
		if o.n > 0 {
			events = make([]agent.ChatEvent, 0, o.n)
			for i := 0; i < o.n; i++ {
				events = append(events, o.ring[(o.head+i)%len(o.ring)])
			}
			o.head, o.n = 0, 0
			dropped, o.dropped = o.dropped, 0
			o.mu.Unlock()
			return dropped, events, true, nil
		}
		ended := o.ended
		o.mu.Unlock()
		if ended {
			return 0, nil, false, nil
		}
		select {
		case <-o.wake:
		case <-ctx.Done():
			return 0, nil, false, ctx.Err()
		}
	}
}

// Close detaches the observer from its tap; events published after it are no
// longer buffered. Safe to call more than once.
func (o *TapObserver) Close() {
	o.tap.mu.Lock()
	delete(o.tap.observers, o)
	o.tap.mu.Unlock()
}
