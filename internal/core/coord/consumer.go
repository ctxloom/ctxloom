package coord

import (
	"context"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
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

// watchRingSize bounds one subscriber's buffer: a stalled watcher loses its
// own newest events rather than ever stalling the RunChannel recv loop that
// calls broadcast — but it is TOLD what it lost (watchSub.lost), never
// silently shorted.
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
//
// lost is non-nil while this subscriber is LAGGED: events were not queued
// (ring full) or were evicted (sendTerminal) and the subscriber has not yet
// been told. It is flushed as ONE synthetic EventsLost marker the moment the
// ring has room for the marker AND the next event, so the marker always
// precedes the first event delivered after the loss. Guarded by watchHub.mu.
type watchSub struct {
	runIDs map[string]bool
	ch     chan *agentcoordpb.AgentEvent
	lost   []*agentcoordpb.EventsLost_Range
}

// subscribe registers a subscriber and returns its event channel, a cancel
// func that unregisters it (call exactly once, when the watching stream
// ends), and a narrow func that re-scopes an already-live subscription to
// exactly one run: a caller that must subscribe before its own
// run's ID exists (StartOwnedRun mints one internally, mid-call) starts
// hub-wide with subscribe(nil) and calls narrow(runID) the moment it learns
// the ID, so it only competes for its own ring's budget for the run's
// remaining, near-entire lifetime instead of forever. narrow is safe to
// discard — a caller that never needs it (one that legitimately wants every
// run in the project) can simply ignore it.
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

// isLossMarker reports whether ev is a synthetic EventsLost marker — the
// hub's own, never a runner's.
func isLossMarker(ev *agentcoordpb.AgentEvent) bool {
	_, ok := ev.GetPayload().(*agentcoordpb.AgentEvent_EventsLost)
	return ok
}

// isEvictable is the complement of the two kinds sendTerminal must never
// sacrifice: a terminal (its loss is unrecoverable — no seq gap ever reveals
// it, and a watcher waits on it forever) and a loss marker (evicting the
// notice of a loss is the silent drop this hub exists to rule out).
func isEvictable(ev *agentcoordpb.AgentEvent) bool {
	return !isTerminal(ev) && !isLossMarker(ev)
}

// broadcast fans ev out to every matching subscriber. Two invariants govern
// this function, and both are load-bearing:
//
//  1. NEVER block the caller. This runs synchronously inside
//     handleAgentEvent, on the goroutine that services a live RunChannel's
//     recv loop (runchannel.go) — stalling here stalls that runner's
//     liveness. A full subscriber ring therefore loses ev for that
//     subscriber only — but NEVER silently: the loss is recorded on the
//     subscriber (watchSub.lost) and delivered as one synthetic EventsLost
//     marker, carrying the exact per-run seq ranges, ahead of the next event
//     that does fit (deliver). A MessageDelta is part of a child's streamed
//     answer; a reader that is not told it lagged reports a truncated answer
//     as the whole one. Rejected alternatives, both ruled out: blocking the
//     producer (one stuck viewer stalls every subscriber and the run) and
//     the plain drop this replaced.
//  2. The one terminal event a run ever emits (RunCompleted — "terminal;
//     exactly one per run", coordination.proto) fights for delivery instead
//     of taking its chances with the ring: silently losing it hangs a
//     consumer that waits on it forever (operations.adaptConsumerFeed only
//     ends the feed on RunCompleted). sendTerminal evicts queued events to
//     make room — bounded, still never a blocking send — rather than trading
//     invariant 1 away even for this one event. Each evicted event is a loss
//     like any other and is reported in the marker the terminal is preceded
//     by.
func (h *watchHub) broadcast(ev *agentcoordpb.AgentEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if len(sub.runIDs) > 0 && !sub.runIDs[ev.GetRunId()] {
			continue
		}
		if isTerminal(ev) {
			sendTerminal(sub, ev)
			continue
		}
		deliver(sub, ev)
	}
}

// deliver queues ev on sub's ring, non-blocking. A lagged subscriber needs
// two free slots — the pending EventsLost marker goes first, then ev — so
// the marker is never delivered after the event it was meant to precede;
// with fewer, ev joins the pending loss. Runs under watchHub.mu, so the only
// concurrent actor is the reader draining sub.ch, which can only ADD room:
// a slot counted free here stays free through the send.
func deliver(sub *watchSub, ev *agentcoordpb.AgentEvent) {
	if sub.room() < sub.need() {
		sub.noteLost(ev)
		return
	}
	sub.flushLost()
	sub.ch <- ev
}

// room is the number of free slots on sub's ring.
func (sub *watchSub) room() int { return cap(sub.ch) - len(sub.ch) }

// need is how many slots the next delivery takes: the event itself, plus
// the pending EventsLost marker that must precede it when sub is lagged.
func (sub *watchSub) need() int {
	if sub.lost != nil {
		return 2
	}
	return 1
}

// noteLost records that ev was not delivered to sub. Contiguous seqs of one
// run coalesce into one range; per loss episode a run can therefore
// contribute at most two ranges (its newest events, not queued; its oldest,
// evicted for a terminal), so the slice is bounded by the runs on the ring.
func (sub *watchSub) noteLost(ev *agentcoordpb.AgentEvent) {
	for i := len(sub.lost) - 1; i >= 0; i-- {
		r := sub.lost[i]
		if r.GetRunId() != ev.GetRunId() {
			continue
		}
		if r.GetLastSeq()+1 == ev.GetSeq() {
			r.LastSeq = ev.GetSeq()
			return
		}
		break
	}
	sub.lost = append(sub.lost, &agentcoordpb.EventsLost_Range{
		RunId: ev.GetRunId(), FirstSeq: ev.GetSeq(), LastSeq: ev.GetSeq(),
	})
}

// flushLost queues sub's pending loss as one EventsLost marker and clears
// it. The caller has already checked there is room.
func (sub *watchSub) flushLost() {
	if sub.lost == nil {
		return
	}
	sub.ch <- &agentcoordpb.AgentEvent{
		OccurredAt: timestamppb.Now(),
		Payload:    &agentcoordpb.AgentEvent_EventsLost{EventsLost: &agentcoordpb.EventsLost{Lost: sub.lost}},
	}
	sub.lost = nil
}

// terminalEvictAttempts bounds sendTerminal's evictions: a handful, never an
// unbounded or blocking loop. A lagged subscriber needs two slots (marker,
// then terminal), so the bound leaves slack beyond that.
const terminalEvictAttempts = 4

// sendTerminal places a run's terminal event onto sub's ring — preceded by
// the pending EventsLost marker if sub is lagged — evicting already-queued
// evictable events (oldest first; the ring is a plain FIFO channel) to make
// room when it is full. Every evicted event is recorded as lost, so the
// marker that precedes the terminal names it. When the ring is saturated
// with events that may not be evicted (other runs' terminals, markers) and
// only one slot can be had, the terminal takes it and the marker stays
// pending for the next flush: dropping the terminal hangs the watcher,
// deferring the marker does not. Never blocks: this races only the serving
// loop draining sub.ch from the other end, which can only free slots.
func sendTerminal(sub *watchSub, ev *agentcoordpb.AgentEvent) {
	for attempt := 0; attempt < terminalEvictAttempts; attempt++ {
		if sub.room() >= sub.need() {
			sub.flushLost()
			sub.ch <- ev
			return
		}
		evicted := evictOneEvictable(sub.ch)
		if evicted == nil {
			break // nothing left to sacrifice; another pass would find the same
		}
		sub.noteLost(evicted)
	}
	if sub.room() >= 1 {
		sub.ch <- ev
		return
	}
	// Dropping a terminal hangs any watcher on this run
	// (operations.adaptConsumerFeed ends its feed only on RunCompleted) and,
	// unlike an ordinary lost event, cannot wait for a marker the reader will
	// stop listening for: name the run whose terminal was lost so a hung
	// viewer is diagnosable from the coordinator's logs.
	clidiag.Warn("ctxloom", "consumer watch: dropped terminal event for run %q after %d evict attempts on a full ring — a watcher on that run may hang until its own timeout",
		ev.GetRunId(), terminalEvictAttempts)
}

// evictOneEvictable frees one slot in ch by removing its OLDEST evictable
// event (isEvictable) and returns it, or nil when every queued event is a
// terminal or a loss marker. It drains events until it finds one to
// sacrifice, holding any protected events it must drain past and re-queuing
// them in order, so only a seq-recoverable event is removed. Stays
// non-blocking throughout (never trades away broadcast's never-block
// invariant), and runs under watchHub.mu so no concurrent broadcast races
// the drain — only the serving loop drains ch too, which is safe (it
// delivers, never loses).
func evictOneEvictable(ch chan *agentcoordpb.AgentEvent) *agentcoordpb.AgentEvent {
	var held []*agentcoordpb.AgentEvent
	for {
		var e *agentcoordpb.AgentEvent
		select {
		case e = <-ch:
		default:
			// Ring drained without an evictable event: put the protected
			// ones back (in order) and let the retry bound decide.
			requeue(ch, held)
			return nil
		}
		if isEvictable(e) {
			requeue(ch, held)
			return e
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
					// rec.ContainerName is set only once a container-runtime
					// run's StartRunner returns (factRunContainer) — empty
					// for host runtime, and the roster's only handle for
					// `docker logs -f`/`docker attach` when no multiplexer
					// is available.
					ContainerName: rec.ContainerName,
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

// WatchRuns is the in-process form of ConsumerService.WatchRuns — a caller
// hosting this coordinator library directly calls this instead of dialing
// its own gRPC loopback. Same semantics: a snapshot
// (returned directly, not framed) plus a live event channel from
// subscribe-time forward; call cancel exactly once when done watching. narrow
// lets a caller that subscribed unscoped (runIDs nil/empty, e.g.
// because its own run's ID does not exist yet) re-scope down to one run the
// moment it learns that ID — see watchHub.subscribe's doc. A caller that
// already knows its run IDs, or genuinely wants every run, can simply
// discard it.
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
// internal/adapters/mcp/mcp_tools_agents_test.go (a different package) also calls it
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

// SpoolStats is the unary read of the coordinator's process-lifetime spool
// counters — the three in-process accessors (SpoolDeliveryStats,
// SpoolDoorbellStats, PushUnavailableCount) projected onto one wire message.
// No journal fact records any of these (they are outcome tallies, not
// state), so this RPC is the ONLY way a process that does not host the
// coordinator can see them.
func (s *consumerService) SpoolStats(context.Context, *agentcoordpb.SpoolStatsRequest) (*agentcoordpb.SpoolStatsResult, error) {
	return s.c.spoolStatsSnapshot(), nil
}

// spoolStatsSnapshot projects the live counters onto the wire shape.
func (c *Coordinator) spoolStatsSnapshot() *agentcoordpb.SpoolStatsResult {
	delivery := c.SpoolDeliveryStats()
	doorbell := c.SpoolDoorbellStats()
	return &agentcoordpb.SpoolStatsResult{
		Delivered:        delivery.Delivered,
		Consumed:         delivery.Consumed,
		Failed:           delivery.Failed,
		DoorbellDropped:  doorbell.Dropped,
		DoorbellRejected: doorbell.Rejected,
	}
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
