// Package agentbus is the in-memory inter-agent message bus for ctxloom's
// agent delegation (agent_send / agent_recv). The broker lives in the
// long-lived orchestrating process — the `ctxloom mcp` server that owns the
// child chat drivers — so its lifetime is the orchestration's lifetime:
// messages are transient coordination between live endpoints, never durable
// records (anything that must outlive the run goes to the durable stores).
// Delivery is at-most-once, per-sender FIFO.
//
// Topology is hub-and-spoke: executors (spawned children) may address only
// their parent; executor-to-executor routing is rejected with a typed error.
// Children reach this broker from their own `ctxloom mcp` subprocess over a
// Unix socket (see socket.go); the wire protocol is package-internal.
package agentbus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ParentAddress is the one recipient a spawned child may address: its own
// coordinator, resolved from the lineage the orchestrator registered.
const ParentAddress = "parent"

// UserSender is the sender identity on messages the human injects through the
// observation viewer. It is never a harp, so a recipient — a child whose
// parked receive completes, or a coordinator draining its mailbox — can tell
// a user message from its parent's.
const UserSender = "user"

// KindUserInjected marks the O3 mirror notice a user injection always sends
// to the target's parent: From names the injected child (the coordinator's
// from-keyed handling attributes the notice to that child's thread), Body
// carries the bounded digest.
const KindUserInjected = "user_injected"

// SocketEnv names the Unix-socket path a child's ctxloom-mcp subprocess dials
// to reach its orchestrator's broker. Set by the orchestrator on the child
// engine's environment alongside CTXLOOM_SESSION_HARP (the child's ambient
// identity — never client-claimed).
const SocketEnv = "CTXLOOM_BUS_SOCKET"

// Typed bus failures. The recv timeout is a contract, not a fault: the caller
// is expected to drop the coordination and write its report/deferral state —
// there is no server-side retry.
var (
	// ErrRecvTimeout completes a parked agent_recv whose bounded wait elapsed
	// with no message.
	ErrRecvTimeout = errors.New("agent_recv: timed out with no message; drop the coordination, write your report/deferral state, and finish")
	// ErrPeerRouting rejects executor→executor addressing (hub-and-spoke).
	ErrPeerRouting = errors.New(`agent_send: executors may only address "parent"; route via coordinator`)
	// ErrUnknownSender rejects a forwarded message whose sender harp was never
	// registered as a child of this orchestrator.
	ErrUnknownSender = errors.New("agent_send: unknown sender: not a child of this orchestrator")
	// ErrRecvBusy rejects a second concurrent agent_recv for the same harp —
	// one parked receive per session is the contract.
	ErrRecvBusy = errors.New("agent_recv: a receive is already parked for this session")
	// ErrNotInjectable rejects a user injection whose target this
	// orchestrator does not hold and cannot resume (an unknown harp, or a
	// foreign process's session — there is no delivery channel into another
	// process's terminal, by design).
	ErrNotInjectable = errors.New("inject: target is not a child this orchestrator holds or can resume")
)

// Message is one bus message. From/To are session harps (To may have been
// addressed as "parent" and resolved via lineage before delivery).
type Message struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind,omitempty"`
	Body string `json:"body"`
}

// Hooks lets the orchestrator tie recv parking to its execution-slot
// accounting (§6a slot yield): a child parked in agent_recv consumes no
// compute, so the orchestrator releases its execution slot in OnPark and
// re-acquires it in OnUnpark. OnUnpark may block (slot re-acquisition) and is
// called on every park exit — message, timeout, or cancellation — exactly
// once, before the parked call completes. Either hook may be nil.
type Hooks struct {
	OnPark   func(harp string)
	OnUnpark func(harp string)
}

// parkedRecv is one held agent_recv call. done flips under the broker mutex
// so the message/timeout/cancel races resolve to exactly one completion.
type parkedRecv struct {
	done bool
	ch   chan recvResult
}

type recvResult struct {
	msgs []Message
	err  error
}

// Broker is the in-memory mailbox hub. All state is process-local; mailbox
// lifetime is the orchestrator process's lifetime.
type Broker struct {
	hooks Hooks

	mu       sync.Mutex
	parentOf map[string]string    // child harp → parent harp (lineage)
	queues   map[string][]Message // recipient harp → pending FIFO
	parked   map[string]*parkedRecv
}

// New builds an empty broker with the orchestrator's park hooks.
func New(hooks Hooks) *Broker {
	return &Broker{
		hooks:    hooks,
		parentOf: make(map[string]string),
		queues:   make(map[string][]Message),
		parked:   make(map[string]*parkedRecv),
	}
}

// AddChild registers a spawned child's lineage: the child may address
// ParentAddress (or the parent harp itself), and nothing else.
func (b *Broker) AddChild(child, parent string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.parentOf[child] = parent
}

// SendFromChild routes a message from a spawned child, enforcing the
// hub-and-spoke policy: the only legal recipient is the child's parent
// (addressed as ParentAddress or by the parent's harp). Anything else is a
// typed routing error — peers route via the coordinator.
func (b *Broker) SendFromChild(from, to, kind, body string) error {
	b.mu.Lock()
	parent, ok := b.parentOf[from]
	b.mu.Unlock()
	if !ok {
		return ErrUnknownSender
	}
	if to != ParentAddress && to != parent {
		return ErrPeerRouting
	}
	b.Send(Message{From: from, To: parent, Kind: kind, Body: body})
	return nil
}

// injectDigestRunes bounds the mirror notice body: enough for the parent to
// recognize what was said, never the bulk (the full text reaches the child as
// its turn, not the parent).
const injectDigestRunes = 120

// InjectFromUser delivers user-typed text to a registered session with sender
// identity UserSender, reporting whether a parked receive consumed it (the
// caller drives the recipient's turn otherwise). An unregistered target is
// the typed ErrNotInjectable — nothing is delivered and nothing mirrored.
//
// INVARIANT (decision O3): the KindUserInjected notice to the target's parent
// mailbox fires on EVERY successful injection, whatever the delivery mode —
// there is no silent-injection path; a coordinator's picture of its child
// never diverges without a trace.
func (b *Broker) InjectFromUser(to, text string) (bool, error) {
	b.mu.Lock()
	parent, ok := b.parentOf[to]
	b.mu.Unlock()
	if !ok {
		return false, ErrNotInjectable
	}
	completed := b.Send(Message{From: UserSender, To: to, Body: text})
	b.Send(Message{From: to, To: parent, Kind: KindUserInjected, Body: injectDigest(text)})
	return completed, nil
}

// injectDigest truncates the mirror body at the rune bound, appending the
// total size so the parent knows how much it didn't see.
func injectDigest(text string) string {
	r := []rune(text)
	if len(r) <= injectDigestRunes {
		return text
	}
	return fmt.Sprintf("%s… (%d chars total)", string(r[:injectDigestRunes]), len(r))
}

// Send enqueues a message for its recipient, completing a parked receive when
// one is waiting. Returns true when a parked receive consumed the message
// (its completion — including the OnUnpark slot re-acquisition — runs
// asynchronously so the sender never blocks on the recipient's slot).
// Routing policy is the caller's: the orchestrator validates recipients on
// the coordinator side, SendFromChild on the executor side.
func (b *Broker) Send(msg Message) bool {
	b.mu.Lock()
	if p, ok := b.parked[msg.To]; ok && !p.done {
		p.done = true
		delete(b.parked, msg.To)
		b.mu.Unlock()
		go func() {
			if b.hooks.OnUnpark != nil {
				b.hooks.OnUnpark(msg.To)
			}
			p.ch <- recvResult{msgs: []Message{msg}}
		}()
		return true
	}
	b.queues[msg.To] = append(b.queues[msg.To], msg)
	b.mu.Unlock()
	return false
}

// TakeNext removes and returns the oldest pending message for harp — the
// orchestrator's turn-boundary drain (§6a: a message queued mid-turn is
// delivered at the next boundary as a new turn).
func (b *Broker) TakeNext(harp string) (Message, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[harp]
	if len(q) == 0 {
		return Message{}, false
	}
	msg := q[0]
	if len(q) == 1 {
		delete(b.queues, harp)
	} else {
		b.queues[harp] = q[1:]
	}
	return msg, true
}

// Pending reports how many messages are queued for harp (the orchestrator's
// ended-child check: leftover mail triggers a resume, never strands).
func (b *Broker) Pending(harp string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queues[harp])
}

// Recv drains all pending messages for harp, or parks for up to wait until a
// message arrives. An elapsed wait (or an empty mailbox with no wait) is the
// typed ErrRecvTimeout. One parked receive per harp; a concurrent second
// receive is ErrRecvBusy.
func (b *Broker) Recv(ctx context.Context, harp string, wait time.Duration) ([]Message, error) {
	b.mu.Lock()
	if q := b.queues[harp]; len(q) > 0 {
		delete(b.queues, harp)
		b.mu.Unlock()
		return q, nil
	}
	if wait <= 0 {
		b.mu.Unlock()
		return nil, ErrRecvTimeout
	}
	if _, busy := b.parked[harp]; busy {
		b.mu.Unlock()
		return nil, ErrRecvBusy
	}
	p := &parkedRecv{ch: make(chan recvResult, 1)}
	b.parked[harp] = p
	b.mu.Unlock()

	if b.hooks.OnPark != nil {
		b.hooks.OnPark(harp)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-p.ch:
		return r.msgs, r.err
	case <-timer.C:
		return b.abandonPark(harp, p, ErrRecvTimeout)
	case <-ctx.Done():
		return b.abandonPark(harp, p, ctx.Err())
	}
}

// abandonPark resolves the timeout/cancel race against a concurrent Send: if
// the send already won (done), its completion is authoritative — wait for it;
// otherwise claim the park, re-acquire the slot (OnUnpark), and fail with err.
func (b *Broker) abandonPark(harp string, p *parkedRecv, err error) ([]Message, error) {
	b.mu.Lock()
	if p.done {
		b.mu.Unlock()
		r := <-p.ch
		return r.msgs, r.err
	}
	p.done = true
	delete(b.parked, harp)
	b.mu.Unlock()
	if b.hooks.OnUnpark != nil {
		b.hooks.OnUnpark(harp)
	}
	return nil, err
}
