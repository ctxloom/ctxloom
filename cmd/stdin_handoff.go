package cmd

import (
	"io"
	"os"
	"sync"
)

// stdinHandoff multiplexes one underlying reader (in practice os.Stdin)
// between strictly sequential consumers, so a later reader never races a
// goroutine the earlier consumer left parked in Read.
//
// The motivating bug: the gRPC client's stdin pump stays blocked in
// stdin.Read after the Run stream ends. When the same process then prompts
// on os.Stdin (init's post-discovery relaunch question), the user's first
// answer line is consumed by the leaked pump and discarded.
//
// Mechanism: a single pump goroutine owns the underlying reader and is
// demand-driven — it issues a Read only when a consumer is waiting, so at
// most one fd read is ever outstanding. Data is appended to a shared pending
// buffer before any consumer copies it out, so bytes that arrive while no
// consumer is attached (e.g. the answer typed while a detached lease's read
// was still in flight) are delivered to the next consumer instead of being
// lost. Detaching a lease wakes its parked Read with io.EOF without
// consuming anything.
//
// Demand-driven reads also mean that once a consumer has its line and stops
// reading, the pump goroutine idles on its request channel rather than in an
// fd read — so a child process exec'd with the real os.Stdin afterwards (the
// relaunched `ctxloom run`) owns the terminal without competition.
type stdinHandoff struct {
	mu          sync.Mutex
	cond        *sync.Cond
	pending     []byte // bytes read from the source, not yet claimed by a lease
	err         error  // sticky source error, surfaced after pending drains
	outstanding bool   // a source read has been requested and not yet completed
	reqCh       chan struct{}
}

// newStdinHandoff wraps r and starts its pump goroutine. The goroutine exits
// when r reports an error (EOF included).
func newStdinHandoff(r io.Reader) *stdinHandoff {
	h := &stdinHandoff{reqCh: make(chan struct{}, 1)}
	h.cond = sync.NewCond(&h.mu)
	go h.pump(r)
	return h
}

func (h *stdinHandoff) pump(r io.Reader) {
	buf := make([]byte, 4096)
	for range h.reqCh {
		n, err := r.Read(buf)
		h.mu.Lock()
		h.pending = append(h.pending, buf[:n]...)
		if err != nil {
			h.err = err
		}
		h.outstanding = false
		h.cond.Broadcast()
		h.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// Attach returns a lease on the source. Leases are meant to be used one at a
// time (sequential consumers); detach the previous one before reading from
// the next.
func (h *stdinHandoff) Attach() *stdinLease {
	return &stdinLease{h: h}
}

// stdinLease is one consumer's view of the handoff source. Read blocks like
// a normal stdin read; Detach makes all current and future Reads on this
// lease return io.EOF immediately, leaving unclaimed bytes for the next lease.
type stdinLease struct {
	h        *stdinHandoff
	detached bool // guarded by h.mu
}

func (l *stdinLease) Read(p []byte) (int, error) {
	h := l.h
	h.mu.Lock()
	defer h.mu.Unlock()
	for {
		// Detach wins over buffered data: bytes stay in pending for the
		// next consumer rather than being handed to a detached reader.
		if l.detached {
			return 0, io.EOF
		}
		if len(h.pending) > 0 {
			n := copy(p, h.pending)
			h.pending = h.pending[n:]
			return n, nil
		}
		if h.err != nil {
			return 0, h.err
		}
		if !h.outstanding {
			h.outstanding = true
			h.reqCh <- struct{}{} // cap 1 + outstanding guard: never blocks
		}
		h.cond.Wait()
	}
}

// Detach releases the lease: its parked and future Reads return io.EOF, and
// any in-flight source read's result is preserved for the next lease.
func (l *stdinLease) Detach() {
	h := l.h
	h.mu.Lock()
	l.detached = true
	h.cond.Broadcast()
	h.mu.Unlock()
}

// sharedStdin is the process-wide handoff over os.Stdin, created lazily so
// commands that never need post-run stdin handoff (the common case) keep
// reading os.Stdin directly with zero extra goroutines.
var (
	sharedStdinOnce sync.Once
	sharedStdin     *stdinHandoff
)

// sharedStdinHandoff returns the process-wide os.Stdin handoff, starting it
// on first use. Once started, all in-process stdin readers on the same flow
// must go through leases (direct os.Stdin reads would race the pump); child
// processes may still inherit os.Stdin as long as no lease is mid-Read.
func sharedStdinHandoff() *stdinHandoff {
	sharedStdinOnce.Do(func() { sharedStdin = newStdinHandoff(os.Stdin) })
	return sharedStdin
}
