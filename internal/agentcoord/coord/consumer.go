package coord

import (
	"context"
	"sync"

	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// D1 — the consumer watch API: read-only observation for viewers (the TUI,
// ACP frontends' D3 push, a parent watching a child live) over a NEW,
// additive ConsumerService. Two pieces live here: the credential class
// (consumerCreds) and the live event fan-out (watchHub); the gRPC surface
// itself (consumerService) is the thin server adapter at the bottom.

// consumerCreds mints and verifies the D1 read-only credential class: a
// SINGLE token per coordinator process lifetime (not per-watcher — every
// consumer of one coordinator shares it, discovered via endpoint.json,
// 0600, host-local). Deliberately NOT journaled: a viewer's ability to
// watch is not runtime-coordinator STATE (the CQRS scope boundary,
// doc.go) — it dies and re-mints with the process, exactly like the
// listener ports persisted beside it in the same endpoint.json. Unlike
// every OTHER credential class in this package (run/session — only the
// hash is ever retained; the plaintext rides the one-shot env seam), the
// plaintext is kept here too: endpoint.json IS the discovery mechanism
// (there is no spawn-time env seam for an out-of-process viewer), 0600
// host-local matching the same trust boundary the env-seam alternative
// (a 0600 cred file) already uses elsewhere in this design.
type consumerCreds struct {
	mu    sync.Mutex
	plain string // "" until minted
	hash  string // hex SHA-256(token); "" until minted
}

// mint mints a fresh consumer token, replacing any prior one (a relaunched
// coordinator's viewers re-read the new token from the rewritten
// endpoint.json — the same re-bind story as the listener ports).
func (cc *consumerCreds) mint() (token string, err error) {
	token, hash, err := mintToken()
	if err != nil {
		return "", err
	}
	cc.mu.Lock()
	cc.plain = token
	cc.hash = hash
	cc.mu.Unlock()
	return token, nil
}

// token returns the current plaintext consumer credential ("" before mint).
func (cc *consumerCreds) token() string {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.plain
}

// verify checks presented against the current consumer credential,
// constant-time (reuses verifyToken's compare so the timing shape matches
// every other credential check in this package).
func (cc *consumerCreds) verify(presented string) bool {
	cc.mu.Lock()
	hash := cc.hash
	cc.mu.Unlock()
	if hash == "" || presented == "" {
		return false
	}
	_, ok := verifyToken(presented, map[string]Identity{hash: {}})
	return ok
}

// watchRingSize bounds one subscriber's buffer: a stalled watcher drops its
// own newest events rather than ever stalling the RunChannel recv loop that
// calls broadcast (the same never-block invariant TapHub documents for the
// legacy per-harp tap; this hub generalizes it across every run).
const watchRingSize = 256

// watchHub is the D1 live consumer broadcast: every AgentEvent
// handleAgentEvent processes on ANY live RunChannel is teed here for
// ConsumerService.WatchRuns subscribers, full payload (including delta
// text) — the durable journal stays counts-only for deltas (items.go); this
// is a separate, additive read path, never the source of truth. Because it
// covers the RunChannel uniformly (StartRun-migrated children included),
// wiring it into handleAgentEvent is also the Recon #1 fix: a migrated
// child's live activity becomes observable again, independent of the
// legacy agentbus TapHub this hub does not replace until D2.
type watchHub struct {
	mu   sync.Mutex
	subs map[*watchSub]struct{}
}

func newWatchHub() *watchHub { return &watchHub{subs: make(map[*watchSub]struct{})} }

// watchSub is one WatchRuns call's subscription. runIDs nil/empty means
// "every run visible to this credential" (D1 does not yet scope visibility
// below the whole project — a consumer credential sees the whole
// coordinator, matching its loopback-only, host-local trust boundary).
type watchSub struct {
	runIDs map[string]bool
	ch     chan *agentcoordpb.AgentEvent
}

// subscribe registers a subscriber and returns its event channel, a cancel
// func that unregisters it (call exactly once, when the watching stream
// ends), and a narrow func that re-scopes an already-live subscription to
// exactly one run: a caller that must subscribe before its own
// run's ID exists (StartOwnedRun mints one internally, mid-call) starts
// hub-wide with subscribe(nil) and calls narrow(runID) the moment it learns
// the ID, so it only competes for its own ring's budget for the run's
// remaining, near-entire lifetime instead of forever. narrow is safe to
// discard — a caller that never needs it (D3's acp_children.go, which
// legitimately wants every run in the project) can simply ignore it.
func (h *watchHub) subscribe(runIDs map[string]bool) (events <-chan *agentcoordpb.AgentEvent, cancel func(), narrow func(runID string)) {
	sub := &watchSub{runIDs: runIDs, ch: make(chan *agentcoordpb.AgentEvent, watchRingSize)}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	cancel = func() {
		h.mu.Lock()
		delete(h.subs, sub)
		h.mu.Unlock()
	}
	narrow = func(runID string) {
		h.mu.Lock()
		sub.runIDs = map[string]bool{runID: true}
		h.mu.Unlock()
	}
	return sub.ch, cancel, narrow
}

func isTerminal(ev *agentcoordpb.AgentEvent) bool {
	_, ok := ev.GetPayload().(*agentcoordpb.AgentEvent_RunCompleted)
	return ok
}

// broadcast fans ev out to every matching subscriber. Two invariants govern
// this function, and both are load-bearing:
//
//  1. NEVER block the caller. This runs synchronously inside
//     handleAgentEvent, on the goroutine that services a live RunChannel's
//     recv loop (runchannel.go) — stalling here stalls that runner's
//     liveness. A full subscriber ring therefore drops ev for that
//     subscriber only (a simpler discipline than TapObserver's
//     drop-oldest+gap-accounting — D1's pre-made semantics promise live
//     delta text, not a gap-free guarantee; a lagging viewer's next event
//     still arrives, and roster/ListRuns always gives it a consistent
//     non-live fallback).
//  2. The one terminal event a run ever emits (RunCompleted — "terminal;
//     exactly one per run", coordination.proto) fights for delivery instead
//     of taking its chances with the plain drop above: silently losing it
//     both evades any seq-based gap detection (nothing arrives afterward to
//     reveal the hole) and hangs a consumer that waits on it forever
//     (operations.adaptConsumerFeed only ends the feed on RunCompleted).
//     sendTerminal below evicts a queued event to make room — bounded,
//     still never a blocking send — rather than trading invariant 1 away
//     even for this one event. The evicted event becomes an honest seq gap,
//     which is exactly what PART 1's consumer-side detection is for.
func (h *watchHub) broadcast(ev *agentcoordpb.AgentEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	terminal := isTerminal(ev)
	for sub := range h.subs {
		if len(sub.runIDs) > 0 && !sub.runIDs[ev.GetRunId()] {
			continue
		}
		if terminal {
			sendTerminal(sub.ch, ev)
			continue
		}
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

// terminalEvictAttempts bounds sendTerminal's evict-then-retry loop: a
// handful of tries, never an unbounded or blocking one.
const terminalEvictAttempts = 4

// sendTerminal places a run's terminal event onto ch, evicting one
// already-queued event (oldest first — ch is a plain FIFO channel) to make
// room when the ring is full. Both selects stay non-blocking throughout:
// this races the serving loop that is concurrently draining ch from the
// other end (consumerService.WatchRuns / Coordinator.WatchRuns's forwarding
// goroutine), which is fine either way — if that loop already freed a slot,
// the send succeeds without needing an eviction; if the eviction "misses"
// because the loop got there first, the next send attempt succeeds instead.
func sendTerminal(ch chan *agentcoordpb.AgentEvent, ev *agentcoordpb.AgentEvent) {
	for attempt := 0; ; attempt++ {
		select {
		case ch <- ev:
			return
		default:
		}
		if attempt == terminalEvictAttempts {
			// Every attempt raced a full ring that never yielded a slot, OR the
			// ring is saturated with OTHER runs' terminals evictOneNonTerminal
			// refuses to sacrifice. Dropping this terminal both evades seq-gap
			// detection (nothing follows to reveal the hole) and hangs any
			// watcher on this run (operations.adaptConsumerFeed ends its feed
			// only on RunCompleted), so unlike an ordinary dropped event it must
			// NOT be silent (F10): name the run whose terminal was lost so a hung
			// viewer is diagnosable from the coordinator's logs.
			clidiag.Warn("ctxloom", "consumer watch: dropped terminal event for run %q after %d evict attempts on a full ring — a watcher on that run may hang until its own timeout",
				ev.GetRunId(), terminalEvictAttempts)
			return
		}
		evictOneNonTerminal(ch)
	}
}

// evictOneNonTerminal frees one slot in ch by dropping its OLDEST NON-terminal
// event, and only a non-terminal (F09): a terminal is the one event whose loss
// is unrecoverable (no seq gap ever reveals it, and a watcher waits on it
// forever), so sendTerminal draining another run's terminal here to make room —
// the payload-blind `<-ch` this replaced — destroyed the very invariant it
// exists to protect (both production subscribers watch ALL runs, so one ring
// carries many runs' terminals). It drains events until it finds a non-terminal
// to sacrifice, holding any terminals it must drain past and re-queuing them so
// only a seq-gap-recoverable event is dropped. If every queued event is itself a
// terminal, nothing is evicted and the caller's bounded retry falls through to
// the F10 drop-with-diagnostic. Stays non-blocking throughout (never trades away
// broadcast's never-block invariant), and runs under watchHub.mu so no
// concurrent broadcast races the drain — only the serving loop drains ch too,
// which is safe (it delivers, never loses).
func evictOneNonTerminal(ch chan *agentcoordpb.AgentEvent) {
	var held []*agentcoordpb.AgentEvent
	for {
		var e *agentcoordpb.AgentEvent
		select {
		case e = <-ch:
		default:
			// Ring drained without a non-terminal to sacrifice: put the
			// terminals back (in order) and let the retry bound decide.
			requeue(ch, held)
			return
		}
		if !isTerminal(e) {
			// Sacrifice this one non-terminal, then restore any terminals we
			// had to drain past to reach it.
			requeue(ch, held)
			return
		}
		held = append(held, e)
	}
}

func requeue(ch chan *agentcoordpb.AgentEvent, evs []*agentcoordpb.AgentEvent) {
	for _, e := range evs {
		select {
		case ch <- e:
		default:
			return
		}
	}
}

// listRunsSnapshot is the roster projection shared by every caller of the
// roster (the plane-2 agent_run ListRuns handler, runchannel.go, and D1's
// ConsumerService ListRuns/WatchRuns snapshot below) — single state, N
// transports.
func (c *Coordinator) listRunsSnapshot(includeTerminal bool, role string) *agentcoordpb.ListRunsResult {
	result := &agentcoordpb.ListRunsResult{}
	c.runs.View(func() {
		for _, e := range c.rosterF.snapshot() {
			if !includeTerminal && e.State == StateEnded {
				continue
			}
			rec := c.runsF.currentRun(e.Harp)
			if rec == nil {
				continue
			}
			if role != "" && rec.Agent != role {
				continue
			}
			result.Runs = append(result.Runs, &agentcoordpb.ListRunsResult_RunInfo{
				RunId: rec.RunID,
				Agent: &agentcoordpb.AgentIdentity{
					AgentId: e.Harp,
					Role:    rec.Agent,
				},
				Phase:         e.State,
				LatestSummary: c.reportsF.latestSummary(e.Harp),
				ParentRunId:   rec.ParentRunID,
				// F1: the run's resolved permission mode and MCP server
				// NAMES ONLY (rec.Permission/MCPServers, fixed at enqueue) —
				// the roster consumer's only black-box view onto the
				// delegation privilege-scoping guarantee.
				PermissionMode: rec.Permission,
				McpServers:     rec.MCPServers,
			})
		}
	})
	return result
}

// WatchRuns is the in-process form of ConsumerService.WatchRuns — D3's acp
// session loop, hosting this coordinator library directly, calls this
// instead of dialing its own gRPC loopback. Same semantics: a snapshot
// (returned directly, not framed) plus a live event channel from
// subscribe-time forward; call cancel exactly once when done watching. narrow
// lets a caller that subscribed unscoped (runIDs nil/empty, e.g.
// because its own run's ID does not exist yet) re-scope down to one run the
// moment it learns that ID — see watchHub.subscribe's doc. A caller that
// already knows its run IDs, or genuinely wants every run (D3's
// acp_children.go), can simply discard it.
func (c *Coordinator) WatchRuns(runIDs []string) (snapshot *agentcoordpb.ListRunsResult, events <-chan *agentcoordpb.AgentEvent, cancel func(), narrow func(runID string)) {
	var filter map[string]bool
	if len(runIDs) > 0 {
		filter = make(map[string]bool, len(runIDs))
		for _, id := range runIDs {
			filter[id] = true
		}
	}
	events, cancel, narrow = c.watch.subscribe(filter)
	snapshot = c.listRunsSnapshot(true, "")
	return snapshot, events, cancel, narrow
}

// ListRuns is the in-process form of ConsumerService.ListRuns.
//
// test-only: no production caller — production reaches
// listRunsSnapshot directly (consumerService.ListRuns, serveListRuns). This
// was deleted once in this wave and reverted: repointing its in-package test
// call sites at listRunsSnapshot compiled fine, but
// internal/cli/mcp_tools_agents_test.go (a different package) also calls it
// via require.Eventually to poll for a roster change — `go vet ./...`, not a
// package-scoped vet, is what caught that. Kept for that cross-package test
// caller, the same reason as LoopbackURL.
func (c *Coordinator) ListRuns(includeTerminal bool, role string) *agentcoordpb.ListRunsResult {
	return c.listRunsSnapshot(includeTerminal, role)
}

// consumerService implements agentcoord.v1.ConsumerService (D1): additive,
// read-only, no change to CoordinatorService. Both RPCs also work called
// in-process (no gRPC hop) via the Coordinator methods below — D3's acp
// session loop, hosting the coordinator library itself, uses that path.
type consumerService struct {
	agentcoordpb.UnimplementedConsumerServiceServer
	c *Coordinator
}

func (s *consumerService) ListRuns(_ context.Context, req *agentcoordpb.ListRunsRequest) (*agentcoordpb.ListRunsResult, error) {
	return s.c.listRunsSnapshot(req.GetIncludeTerminal(), req.GetRole()), nil
}

// WatchRuns serves the stream: snapshot first, then live AgentEvents
// (subscribe BEFORE building the snapshot so nothing published in the gap
// between subscribing and sending is missed — it simply arrives, correctly
// ordered, right after the snapshot frame instead of before).
func (s *consumerService) WatchRuns(req *agentcoordpb.WatchRunsRequest, stream grpc.ServerStreamingServer[agentcoordpb.WatchEvent]) error {
	c := s.c
	var filter map[string]bool
	if ids := req.GetRunIds(); len(ids) > 0 {
		filter = make(map[string]bool, len(ids))
		for _, id := range ids {
			filter[id] = true
		}
	}
	events, cancel, _ := c.watch.subscribe(filter)
	defer cancel()

	snap := c.listRunsSnapshot(true, "")
	if err := stream.Send(&agentcoordpb.WatchEvent{Kind: &agentcoordpb.WatchEvent_Snapshot{Snapshot: &agentcoordpb.RosterSnapshot{Runs: snap.GetRuns()}}}); err != nil {
		return err
	}
	ctx := stream.Context()
	for {
		select {
		case ev := <-events:
			if err := stream.Send(&agentcoordpb.WatchEvent{Kind: &agentcoordpb.WatchEvent_Event{Event: ev}}); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
