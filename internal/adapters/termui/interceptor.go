package termui

import (
	"bytes"
	"io"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// interceptor states. The prefix machine is token-deterministic — no timers —
// so the double-press-literal contract is exact: a second prefix press that
// follows the first before any other key yields ONE literal prefix, spelled as
// the terminal sent it, to the engine (cancelling the engagement if it
// already fired). The prefix is a key, matched in every encoding (prefixKey).
type ixState int

const (
	// ixPass forwards every byte to the engine untouched (the hot path).
	ixPass ixState = iota
	// ixFresh is "prefix just seen, no viewer key yet": the next token
	// decides between literal passthrough (prefix again) and viewer input
	// (anything else).
	ixFresh
	// ixUI routes input to the engaged viewer's sink, a whole token at a
	// time: keys to the viewer (which owns their meaning, prefix/q = back
	// included), terminal replies to the engine that asked for them, mouse
	// reports nowhere. Until Disengage.
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
// machine and the modal's focus locks. It is the single reader of the
// underlying stream; Read is called by the plugin client's stdin pump (one
// goroutine), while Disengage/Off/engageExternal and the arming calls arrive
// from other goroutines — hence the mutex on state.
type interceptor struct {
	src    io.Reader
	prefix prefixKey
	cb     InterceptorCallbacks
	now    func() time.Time

	mu      sync.Mutex
	state   ixState
	engaged bool      // Engage fired for the current engagement
	sink    io.Writer // viewer input sink while engaged
	tok     inputTok
	// fwd is how many leading bytes of tok's carried tail already reached the
	// engine. Passthrough forwards an incomplete tail at once unless it could
	// still become the prefix (holdable); when the token completes, only the
	// rest is forwarded.
	fwd       int
	lastInput time.Time // when the last read arrived: the quiet gate's clock
	// draining: a paste began inside a viewer that has since closed; the rest
	// of it, to its end marker, is dropped rather than typed into the engine
	// without its brackets.
	draining    bool
	pasteToSink bool // where the paste in progress goes (decided at its start)
	modal       modalFocus

	engBuf, uiBuf []byte // scratch for one read's routed bytes, reused
	// outPend/errPend: engine bytes (and the read error behind them) that did
	// not fit the caller's buffer. Touched only by the Read goroutine.
	outPend []byte
	errPend error
}

// modalFocus is the arming state of a summoned engagement (lock 3): keys are
// discarded and counted until ArmFor has passed since both the first frame
// and the last discarded key.
type modalFocus struct {
	on        bool
	gen       uint64
	armFor    time.Duration
	arming    bool
	frameSeen bool
	armUntil  time.Time
	discarded int
}

// newInterceptor wraps src. prefix is the control byte from ParsePrefixKey,
// which names the key; now is the controller's clock.
func newInterceptor(src io.Reader, prefix byte, cb InterceptorCallbacks, now func() time.Time) *interceptor {
	return &interceptor{src: src, prefix: newPrefixKey(prefix), cb: cb, now: now}
}

// Read implements the engine-bound side: it returns only bytes destined for
// the engine, routing viewer-bound bytes to the sink in between. When a chunk
// is consumed entirely by the viewer it reads again rather than returning
// (0, nil).
func (ic *interceptor) Read(p []byte) (int, error) {
	if len(ic.outPend) > 0 {
		return ic.takePending(p)
	}
	for {
		n, err := ic.src.Read(p)
		if n > 0 {
			out, ui, act := ic.scan(p[:n])
			ic.dispatch(ui, act)
			if len(out) > 0 {
				return ic.deliver(p, out, err)
			}
		}
		if err != nil {
			return 0, err
		}
	}
}

// deliver hands out to the caller. The hot path's out IS p's prefix, so there
// is nothing to move; a routed read can carry more engine bytes than it read
// (a reply completing one carried from the previous read), and the excess
// waits for the next Read.
func (ic *interceptor) deliver(p, out []byte, err error) (int, error) {
	if &out[0] == &p[0] {
		return len(out), err
	}
	n := copy(p, out)
	if n == len(out) {
		return n, err
	}
	ic.outPend = append(ic.outPend[:0], out[n:]...)
	ic.errPend = err
	return n, nil
}

func (ic *interceptor) takePending(p []byte) (int, error) {
	n := copy(p, ic.outPend)
	ic.outPend = ic.outPend[n:]
	if len(ic.outPend) > 0 {
		return n, nil
	}
	err := ic.errPend
	ic.errPend = nil
	return n, err
}

// ixActions are the deferred side effects of one scan, fired outside the lock.
type ixActions struct {
	engage bool
	abort  bool
}

// scan runs the state machine over one chunk. The hot path — passthrough with
// nothing carried and neither the prefix byte nor an ESC in the chunk, so no
// encoding of the prefix can be in it — returns the chunk unchanged after one
// boundary-tracking check: zero copies, zero allocations. Otherwise the chunk
// is taken a whole token at a time: the engine-bound bytes (chronological
// order preserved, including a literal prefix emitted by a double press) and
// the viewer-bound bytes land in reused scratch buffers.
func (ic *interceptor) scan(chunk []byte) (out, ui []byte, act ixActions) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.lastInput = ic.now()
	if ic.state == ixOff {
		return ic.offScan(chunk), nil, act
	}
	if ic.hotPath(chunk) {
		ic.tok.track(chunk) // can carry only a rune's leading bytes, delivered here
		ic.fwd = len(ic.tok.pend)
		return chunk, nil, act
	}
	ic.engBuf, ic.uiBuf = ic.engBuf[:0], ic.uiBuf[:0]
	ic.tok.feed(chunk, true, func(k tokKind, b []byte) { ic.token(k, b, &act) })
	ic.forwardUnheldTail()
	if ic.engageOnce() {
		act.engage = true
	}
	return ic.engBuf, ic.uiBuf, act
}

// hotPath reports whether chunk can pass straight through: passthrough with
// nothing carried, not draining, and neither the prefix byte nor an ESC in
// the chunk.
func (ic *interceptor) hotPath(chunk []byte) bool {
	return ic.state == ixPass && !ic.draining && len(ic.tok.pend) == 0 &&
		bytes.IndexByte(chunk, ic.prefix.b) < 0 && bytes.IndexByte(chunk, ansi.ESC) < 0
}

// forwardUnheldTail sends the engine, in passthrough, the part of the carried
// tail it does not have yet unless that tail may still complete as a prefix.
func (ic *interceptor) forwardUnheldTail() {
	if ic.state == ixPass && !ic.draining && len(ic.tok.pend) > ic.fwd && !ic.holdable() {
		ic.engBuf = append(ic.engBuf, ic.tok.pend[ic.fwd:]...)
		ic.fwd = len(ic.tok.pend)
	}
}

// engageOnce reports whether this scan is the one that first took the
// machine into the prefix UI (fresh or UI state), marking it engaged.
func (ic *interceptor) engageOnce() bool {
	if (ic.state != ixFresh && ic.state != ixUI) || ic.engaged {
		return false
	}
	ic.engaged = true
	return true
}

// offScan is degraded passthrough: a tail held for the prefix when the
// machine went off goes out first.
func (ic *interceptor) offScan(chunk []byte) []byte {
	if len(ic.tok.pend) <= ic.fwd {
		return chunk
	}
	ic.engBuf = append(append(ic.engBuf[:0], ic.tok.pend[ic.fwd:]...), chunk...)
	ic.dropTail()
	return ic.engBuf
}

// maxHeldTail bounds a held tail: every encoding of a prefix key is far
// shorter.
const maxHeldTail = 32

// holdable reports whether the carried tail could still complete as an
// encoded prefix (ESC [ then a digit, then parameters, no final yet). Only
// such a tail waits for the next read; a lone Esc, Alt+[ or anything else
// reaches the engine in the read it arrived in. A terminal writes a sequence
// in one burst, so the wait is a read boundary's, never a human's.
func (ic *interceptor) holdable() bool {
	t := ic.tok.pend
	if ic.tok.inPaste || ic.fwd > 0 || len(t) > maxHeldTail {
		return false
	}
	return openNumericCSI(t)
}

// openNumericCSI reports whether t is ESC [ and a digit followed only by
// parameter bytes (digits, ';', ':'): a CSI with no final byte yet.
func openNumericCSI(t []byte) bool {
	if len(t) < 3 || t[0] != ansi.ESC || t[1] != '[' || !isDigit(t[2]) {
		return false
	}
	for _, c := range t[3:] {
		if !isDigit(c) && c != ';' && c != ':' {
			return false
		}
	}
	return true
}

// isDigit reports whether c is an ASCII decimal digit.
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (ic *interceptor) dropTail() {
	ic.tok.reset()
	ic.fwd = 0
}

// token advances the machine over one whole input token. Leading bytes the
// engine already has (a tail forwarded before it completed) are skipped, and
// such a token is never the prefix: half of it is already the engine's.
func (ic *interceptor) token(k tokKind, b []byte, act *ixActions) {
	skip := min(ic.fwd, len(b))
	ic.fwd -= skip
	if ic.draining {
		ic.drainToken(k, b[skip:])
		return
	}
	hit := keyMiss
	if k == tokKey && skip == 0 {
		hit = ic.prefix.hit(b)
	}
	switch ic.state {
	case ixPass:
		switch hit {
		case keyPress:
			ic.state = ixFresh
		case keyRelease:
			// The engine never saw the press; it does not get the release.
		default:
			ic.engBuf = append(ic.engBuf, b[skip:]...)
		}
	case ixFresh:
		ic.fresh(k, b, hit, act)
	default:
		ic.routeToken(k, b)
	}
}

// fresh decides the token after a prefix: the prefix again is one literal
// prefix to the engine; its release changes nothing; anything else is viewer
// input.
func (ic *interceptor) fresh(k tokKind, b []byte, hit keyHit, act *ixActions) {
	switch hit {
	case keyRelease:
		return
	case keyMiss:
		ic.state = ixUI
		ic.routeToken(k, b)
		return
	}
	// Double press: one literal prefix to the engine. When the pair sat in
	// one chunk no engagement ever fired — no viewer flash; across chunks the
	// controller aborts the just-opened viewer.
	ic.engBuf = append(ic.engBuf, b...)
	if ic.engaged {
		act.abort = true
		ic.engaged = false
		ic.sink = nil
	}
	ic.state = ixPass
}

// routeToken is lock 5 plus lock 3 for one whole token under a viewer.
func (ic *interceptor) routeToken(k tokKind, b []byte) {
	switch k {
	case tokReport:
		ic.engBuf = append(ic.engBuf, b...)
	case tokMouse:
		// Dropped: acting on it would click a screen the human cannot see.
	case tokPasteBegin:
		ic.pasteToSink = ic.acceptKey()
		ic.pasteToViewer(b)
	case tokPasteData, tokPasteEnd:
		ic.pasteToViewer(b)
	default:
		if ic.acceptKey() {
			ic.uiBuf = append(ic.uiBuf, b...)
		}
	}
}

func (ic *interceptor) pasteToViewer(b []byte) {
	if ic.pasteToSink {
		ic.uiBuf = append(ic.uiBuf, b...)
	}
}

// acceptKey reports whether a key may reach the viewer, discarding (and
// counting) it while a summoned modal is arming. A discarded key after the
// first frame restarts the window.
func (ic *interceptor) acceptKey() bool {
	m := &ic.modal
	if !m.on || !m.arming {
		return true
	}
	m.discarded++
	if m.frameSeen {
		m.armUntil = ic.now().Add(m.armFor)
	}
	return false
}

// drainToken drops the rest of a paste the closed viewer began; the stream
// after its end marker is the engine's again.
func (ic *interceptor) drainToken(k tokKind, b []byte) {
	switch k {
	case tokPasteData:
	case tokPasteEnd:
		ic.draining = false
	default:
		ic.engBuf = append(ic.engBuf, b...)
	}
}

// dispatch fires scan's deferred effects without holding the state lock (the
// Engage hook re-enters the interceptor via the controller). A failed engage
// (nil sink) drops back to passthrough and discards the viewer-bound bytes —
// they were keystrokes aimed at a viewer that never opened, not engine input.
func (ic *interceptor) dispatch(ui []byte, act ixActions) {
	if act.abort && ic.cb.AbortLiteral != nil {
		ic.cb.AbortLiteral()
	}
	if act.engage && !ic.engageSink() {
		return
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

func (ic *interceptor) engageSink() bool {
	var sink io.Writer
	if ic.cb.Engage != nil {
		sink = ic.cb.Engage()
	}
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if sink == nil {
		ic.engaged = false
		if ic.state == ixFresh || ic.state == ixUI {
			ic.state = ixPass
		}
		return false
	}
	ic.sink = sink
	return true
}

// engageExternal moves focus to a summoned viewer's sink — only between
// reads, at an input boundary, after stdin has been quiet for quietFor, and
// never over a prefix engagement (locks 1 and 2). The check and the switch
// are one step under the lock scan runs under, so no read can land between
// them. On refusal, retryIn is when the quiet gate could next pass (zero:
// the prefix path holds focus — wait for it to finish).
func (ic *interceptor) engageExternal(sink io.Writer, quietFor, armFor time.Duration, gen uint64) (retryIn time.Duration, ok bool) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.state != ixPass {
		return 0, false
	}
	if idle := ic.now().Sub(ic.lastInput); idle < quietFor {
		return quietFor - idle, false
	}
	if ic.draining || !ic.tok.atBoundary(true) {
		return quietFor, false
	}
	// A carried lone ESC already reached the engine: it was the Esc key.
	ic.dropTail()
	ic.state, ic.engaged, ic.sink = ixUI, true, sink
	ic.modal = modalFocus{on: true, gen: gen, armFor: armFor, arming: true}
	return 0, true
}

// armFrame starts the arming window at a summoned modal's first frame and
// returns when it would end; ok is false once that engagement is gone or
// armed.
func (ic *interceptor) armFrame(gen uint64) (until time.Time, ok bool) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	m := &ic.modal
	if !m.on || m.gen != gen || !m.arming {
		return time.Time{}, false
	}
	if !m.frameSeen {
		m.frameSeen = true
		m.armUntil = ic.now().Add(m.armFor)
	}
	return m.armUntil, true
}

// tryArm ends the arming window if it has run out, returning how many keys it
// discarded; otherwise it returns the window's current end. ok is false once
// that engagement is gone or armed.
func (ic *interceptor) tryArm(gen uint64) (armed bool, discarded int, until time.Time, ok bool) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	m := &ic.modal
	if !m.on || m.gen != gen || !m.arming {
		return false, 0, time.Time{}, false
	}
	if ic.now().Before(m.armUntil) {
		return false, 0, m.armUntil, true
	}
	m.arming = false
	return true, m.discarded, time.Time{}, true
}

// Disengage returns the machine to passthrough after the viewer exits.
// Idempotent; safe from any goroutine. A token the viewer was still
// collecting is dropped with it; a paste still arriving is drained.
func (ic *interceptor) Disengage() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.state == ixFresh || ic.state == ixUI {
		ic.state = ixPass
		ic.draining = ic.tok.inPaste
		if !ic.draining {
			ic.dropTail()
		}
	}
	ic.engaged = false
	ic.sink = nil
	ic.modal = modalFocus{}
}

// Off permanently degrades to pure passthrough (viewer failure): the prefix
// flows to the engine like any other key from here on. A tail the viewer was
// collecting, or a paste being drained, is dropped; one passthrough held for
// the prefix goes out ahead of the next read.
func (ic *interceptor) Off() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.state != ixPass || ic.draining {
		ic.dropTail()
	}
	ic.state = ixOff
	ic.engaged = false
	ic.sink = nil
	ic.modal = modalFocus{}
	ic.draining = false
}
