package termui

import (
	"bytes"
	"io"
	"sync"
)

// interceptor states. The machine is byte-deterministic — no timers — so the
// double-press-literal contract is exact: a second prefix byte that follows
// the first before any other key yields ONE literal prefix byte to the engine
// (cancelling the engagement if it already fired).
type ixState int

const (
	// ixPass forwards every byte to the engine untouched (the hot path).
	ixPass ixState = iota
	// ixFresh is "prefix just seen, no viewer key yet": the next byte decides
	// between literal passthrough (prefix again) and viewer input (anything
	// else).
	ixFresh
	// ixUI forwards every byte to the engaged viewer's input sink; the viewer
	// owns key interpretation (including prefix/q = back) until Disengage.
	ixUI
	// ixOff is the permanent-degrade state after a viewer failure: pure
	// passthrough, prefix included. A UI-layer fault never costs keystrokes.
	ixOff
)

// InterceptorCallbacks are the controller hooks the state machine drives.
// Both are invoked on the stdin pump goroutine, outside the interceptor lock.
type InterceptorCallbacks struct {
	// Engage fires when the prefix engages viewer mode. It returns the sink
	// for viewer-bound bytes, or nil when the viewer could not start — the
	// interceptor then drops back to passthrough for this engagement.
	Engage func() io.Writer
	// AbortLiteral fires on the double-press-literal path when an engagement
	// had already fired (the prefix pair straddled a read boundary): the
	// controller closes the just-opened viewer. The literal prefix byte is
	// emitted by the interceptor itself.
	AbortLiteral func()
}

// interceptor wraps the frontend's raw stdin stream with the prefix-key state
// machine. It is the single reader of the underlying stream; Read is called
// by the plugin client's stdin pump (one goroutine), while Disengage/Off may
// arrive from the controller's overlay goroutine — hence the mutex on state.
type interceptor struct {
	src    io.Reader
	prefix byte
	cb     InterceptorCallbacks

	mu      sync.Mutex
	state   ixState
	engaged bool      // Engage fired for the current engagement
	sink    io.Writer // viewer input sink while engaged

	uiBuf []byte // scratch for viewer-bound bytes, reused across reads
}

// newInterceptor wraps src. prefix is the raw control byte from
// ParsePrefixKey.
func newInterceptor(src io.Reader, prefix byte, cb InterceptorCallbacks) *interceptor {
	return &interceptor{src: src, prefix: prefix, cb: cb}
}

// Read implements the engine-bound side: it returns only bytes destined for
// the engine, routing viewer-bound bytes to the sink in between. When a chunk
// is consumed entirely by the viewer it reads again rather than returning
// (0, nil).
func (ic *interceptor) Read(p []byte) (int, error) {
	for {
		n, err := ic.src.Read(p)
		if n > 0 {
			out, ui, act := ic.scan(p[:n])
			ic.dispatch(ui, act)
			if len(out) > 0 {
				return len(out), err
			}
		}
		if err != nil {
			return 0, err
		}
	}
}

// ixActions are the deferred side effects of one scan, fired outside the lock.
type ixActions struct {
	engage bool
	abort  bool
}

// scan runs the state machine over one chunk IN PLACE: engine-bound bytes are
// compacted to the front of chunk (chronological order preserved, including a
// literal prefix emitted by a double press), viewer-bound bytes land in the
// reused scratch buffer. The hot path — passthrough with no prefix byte in
// the chunk — is a single IndexByte scan returning the chunk unchanged: zero
// copies, zero allocations.
func (ic *interceptor) scan(chunk []byte) (out, ui []byte, act ixActions) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	switch ic.state {
	case ixOff:
		return chunk, nil, ixActions{}
	case ixPass:
		if bytes.IndexByte(chunk, ic.prefix) < 0 {
			return chunk, nil, ixActions{}
		}
	}
	ui = ic.uiBuf[:0]
	k := 0
	for _, b := range chunk {
		switch ic.state {
		case ixPass:
			if b == ic.prefix {
				ic.state = ixFresh
			} else {
				chunk[k] = b
				k++
			}
		case ixFresh:
			if b == ic.prefix {
				// Double press: one literal prefix byte to the engine. When
				// the pair sat in one chunk no engagement ever fired — no
				// viewer flash; across chunks the controller aborts the
				// just-opened viewer.
				chunk[k] = b
				k++
				if ic.engaged {
					act.abort = true
					ic.engaged = false
					ic.sink = nil
				}
				ic.state = ixPass
			} else {
				ui = append(ui, b)
				ic.state = ixUI
			}
		case ixUI:
			ui = append(ui, b)
		}
	}
	if (ic.state == ixFresh || ic.state == ixUI) && !ic.engaged {
		act.engage = true
		ic.engaged = true
	}
	ic.uiBuf = ui
	return chunk[:k], ui, act
}

// dispatch fires scan's deferred effects without holding the state lock (the
// Engage hook re-enters the interceptor via the controller). A failed engage
// (nil sink) drops back to passthrough and discards the viewer-bound bytes —
// they were keystrokes aimed at a viewer that never opened, not engine input.
func (ic *interceptor) dispatch(ui []byte, act ixActions) {
	if act.abort && ic.cb.AbortLiteral != nil {
		ic.cb.AbortLiteral()
	}
	if act.engage {
		var sink io.Writer
		if ic.cb.Engage != nil {
			sink = ic.cb.Engage()
		}
		ic.mu.Lock()
		if sink == nil {
			ic.engaged = false
			if ic.state == ixFresh || ic.state == ixUI {
				ic.state = ixPass
			}
			ic.mu.Unlock()
			return
		}
		ic.sink = sink
		ic.mu.Unlock()
	}
	if len(ui) > 0 {
		ic.mu.Lock()
		sink := ic.sink
		ic.mu.Unlock()
		if sink != nil {
			_, _ = sink.Write(ui) // a closed sink (viewer just exited) drops the bytes
		}
	}
}

// Disengage returns the machine to passthrough after the viewer exits.
// Idempotent; safe from any goroutine.
func (ic *interceptor) Disengage() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.state == ixFresh || ic.state == ixUI {
		ic.state = ixPass
	}
	ic.engaged = false
	ic.sink = nil
}

// Off permanently degrades to pure passthrough (viewer failure): the prefix
// byte flows to the engine like any other key from here on.
func (ic *interceptor) Off() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.state = ixOff
	ic.engaged = false
	ic.sink = nil
}
