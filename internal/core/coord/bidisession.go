package coord

import (
	"context"
	"sync"
)

// BidiSession is the ONE scaffold under every bidirectional stream session
// the coordination planes hold — the coordinator's side of a runner session
// (RunnerSession) and of a run channel (RunChannel), and the runner's Home
// on its run channel: a single-writer send queue drained by ONE pump onto the
// stream, and the request/response correlation for the requests THIS side
// issues over it.
//
// Out is the frame type this side sends; Req and Resp are the request this
// side issues and the response it correlates by request id. A side that
// issues no requests instantiates them with the frame types it never uses.
//
// The correlation is what a session must get right at its end: once the
// stream is gone nothing will ever answer, so FailPending answers every
// waiter itself AND marks the session ended, and a Register after that mark
// is REFUSED — the two are one atomic step under reqMu, or a caller that
// read the session just before its teardown registers a waiter nobody is
// left to resolve.
type BidiSession[Out, Req, Resp any] struct {
	// send is the single-writer queue: everything outbound funnels through
	// it, and pump is the only goroutine that writes the stream.
	send   chan Out
	cancel context.CancelFunc

	reqMu   sync.Mutex
	pending map[string]pendingReq[Req, Resp]
	ended   bool
}

// pendingReq is one issued request awaiting its answer. The request itself
// is kept so a reconnect can reissue it under its ORIGINAL id.
type pendingReq[Req, Resp any] struct {
	req Req
	ch  chan Resp
}

func NewBidiSession[Out, Req, Resp any](cancel context.CancelFunc, sendDepth int) BidiSession[Out, Req, Resp] {
	return BidiSession[Out, Req, Resp]{
		send:    make(chan Out, sendDepth),
		cancel:  cancel,
		pending: make(map[string]pendingReq[Req, Resp]),
	}
}

// Pump drains send onto write until ctx ends or a write fails (which cancels
// the session: the stream is unusable, and the receiving half sees the same
// failure).
func (s *BidiSession[Out, Req, Resp]) Pump(ctx context.Context, write func(Out) error) {
	for {
		select {
		case frame := <-s.send:
			if err := write(frame); err != nil {
				s.cancel()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// Register records a waiter for id. ok is false once the session has ended:
// the caller must not wait, because nothing will answer.
func (s *BidiSession[Out, Req, Resp]) Register(id string, req Req) (ch chan Resp, ok bool) {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	if s.ended {
		return nil, false
	}
	ch = make(chan Resp, 1)
	s.pending[id] = pendingReq[Req, Resp]{req: req, ch: ch}
	return ch, true
}

// Resolve hands resp to id's waiter, if one is registered, and forgets it.
func (s *BidiSession[Out, Req, Resp]) Resolve(id string, resp Resp) bool {
	s.reqMu.Lock()
	p, ok := s.pending[id]
	delete(s.pending, id)
	s.reqMu.Unlock()
	if ok {
		p.ch <- resp
	}
	return ok
}

// Withdraw forgets id's waiter (the caller gave up).
func (s *BidiSession[Out, Req, Resp]) Withdraw(id string) {
	s.reqMu.Lock()
	delete(s.pending, id)
	s.reqMu.Unlock()
}

// Outstanding returns every request still awaiting an answer — what a
// reconnect reissues.
func (s *BidiSession[Out, Req, Resp]) Outstanding() []Req {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	reqs := make([]Req, 0, len(s.pending))
	for _, p := range s.pending {
		reqs = append(reqs, p.req)
	}
	return reqs
}

// FailPending answers every in-flight request with fail(id) and marks the
// session ended, so no waiter hangs past the session's end and no later
// register succeeds.
func (s *BidiSession[Out, Req, Resp]) FailPending(fail func(id string) Resp) {
	s.reqMu.Lock()
	pending := s.pending
	s.pending = make(map[string]pendingReq[Req, Resp])
	s.ended = true
	s.reqMu.Unlock()
	for id, p := range pending {
		p.ch <- fail(id)
	}
}

// Send is the single-writer queue's write end: a frame queued here reaches
// the stream through Pump, and only through Pump.
func (s *BidiSession[Out, Req, Resp]) Send() chan<- Out { return s.send }

// Cancel ends the session's stream context.
func (s *BidiSession[Out, Req, Resp]) Cancel() { s.cancel() }
