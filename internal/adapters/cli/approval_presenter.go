package cli

import (
	"context"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// modalPresenter presents the root's approval queue on the interactive run's
// terminal: the bar's ⚑ count and bell, and the approvals modal summoned at
// the human's next typing pause. It is the queue's one presenter; only the
// modal answers.
type modalPresenter struct {
	ui    *termui.Controller
	clock termui.Clock
}

// Present runs until ctx ends. It implements coord.ApprovalPresenter.
func (p modalPresenter) Present(ctx context.Context, src coord.ApprovalSource) error {
	return presentApprovals(ctx, src, presenterUI{summon: p.ui.Summon, setApprovals: p.ui.SetApprovals, noteBar: p.ui.NoteBar}, p.clock)
}

var _ coord.ApprovalPresenter = modalPresenter{}

// presenterUI is what the presenter drives on the terminal layer.
type presenterUI struct {
	summon       func(ctx context.Context, start termui.OverlayStart, n termui.Notice) error
	setApprovals func(n int, oldest time.Time, arrived bool)
	noteBar      func(text string, d time.Duration)
}

const (
	// presenterTick refreshes the bar's oldest age and checks the warning.
	presenterTick = time.Second
	// warnBefore is when a request still waiting gets one more summon and
	// one more bell.
	warnBefore = 2 * time.Minute
	// timedOutNote is what the bar says after a request nobody decided.
	timedOutNote = "approval resolved (timed out)"
	// timedOutNoteFor outlasts the tombstone the modal shows first (the
	// modal closes only after it), so the note is still there when the bar
	// comes back.
	timedOutNoteFor = 15 * time.Second
)

// presentApprovals is the presenter's policy:
//   - the bar always carries the count and the oldest request's arrival;
//   - an arrival rings the bell and summons the modal, naming its asker —
//     replacing a summon still waiting for the human's typing pause;
//   - a hidden modal comes back only for a new request, or once per request
//     at warnBefore left (with one more bell);
//   - nothing pending withdraws a waiting summon;
//   - a request that timed out leaves a note on the bar: nobody decided it.
func presentApprovals(ctx context.Context, src coord.ApprovalSource, ui presenterUI, clock termui.Clock) error {
	events := src.Subscribe(ctx)
	p := &presenter{src: src, ui: ui, clock: clock, warned: map[coord.ApprovalID]bool{}}
	defer p.stopSummon()
	wake := make(chan struct{}, 1)
	stop := clock.AfterFunc(presenterTick, func() { nudge(wake) })
	defer func() { stop() }()
	p.start(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			p.onEvent(ctx, ev)
		case <-wake:
			p.onTick(ctx)
			stop = clock.AfterFunc(presenterTick, func() { nudge(wake) })
		}
	}
}

func nudge(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// presenter is presentApprovals' state; only its loop touches it.
type presenter struct {
	src    coord.ApprovalSource
	ui     presenterUI
	clock  termui.Clock
	warned map[coord.ApprovalID]bool
	// cancel withdraws the summon in flight; wg waits for it to return.
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// start shows what was already waiting: no bell, since nothing arrived.
func (p *presenter) start(ctx context.Context) {
	pending := p.bar(false)
	if len(pending) > 0 {
		p.markWarned(pending)
		p.summon(ctx, pending[0].From.Harp)
	}
}

func (p *presenter) onEvent(ctx context.Context, ev coord.QueueEvent) {
	switch ev.Kind {
	case coord.QueueAdded:
		pending := p.bar(true)
		for _, req := range pending {
			if req.ID == ev.ID {
				p.markWarned([]coord.PendingApproval{req})
				p.summon(ctx, req.From.Harp)
			}
		}
	case coord.QueueResolved:
		if ev.Decider == agent.DeciderTimeout {
			p.ui.noteBar(timedOutNote, timedOutNoteFor)
		}
		if len(p.bar(false)) == 0 {
			p.stopSummon()
		}
	}
}

// onTick refreshes the bar and gives each request its one warning.
func (p *presenter) onTick(ctx context.Context) {
	pending := p.bar(false)
	live := make(map[coord.ApprovalID]bool, len(pending))
	now := p.clock.Now()
	for _, req := range pending {
		live[req.ID] = true
		if !p.warned[req.ID] && req.Deadline.Sub(now) <= warnBefore {
			p.warned[req.ID] = true
			p.bar(true)
			p.summon(ctx, req.From.Harp)
		}
	}
	for id := range p.warned {
		if !live[id] {
			delete(p.warned, id)
		}
	}
}

// markWarned spends the warning of a request that is already inside it when
// first seen: its arrival summon is the warning.
func (p *presenter) markWarned(reqs []coord.PendingApproval) {
	now := p.clock.Now()
	for _, req := range reqs {
		if req.Deadline.Sub(now) <= warnBefore {
			p.warned[req.ID] = true
		}
	}
}

// bar sets the bar from the queue and returns what is pending; ring asks
// for the bell.
func (p *presenter) bar(ring bool) []coord.PendingApproval {
	pending := p.src.Pending()
	var oldest time.Time
	for _, req := range pending {
		if oldest.IsZero() || req.Since.Before(oldest) {
			oldest = req.Since
		}
	}
	p.ui.setApprovals(len(pending), oldest, ring)
	return pending
}

// summon asks for the modal, naming the asker to an overlay that already
// holds the screen. Its result is not the presenter's to act on: shown, the
// modal takes over; refused because an overlay is up, that overlay was told;
// unavailable, the bar and bell are all there is.
func (p *presenter) summon(ctx context.Context, harp string) {
	p.stopSummon()
	sctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	notice := termui.Notice{Text: "approval from " + harp}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		_ = p.ui.summon(sctx, termui.OverlayStart{View: "approvals"}, notice)
	}()
}

func (p *presenter) stopSummon() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.wg.Wait()
}
