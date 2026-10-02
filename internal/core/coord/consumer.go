package coord

import (
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The consumer watch API: read-only observation for viewers (the TUI, a
// parent watching a child live). Two pieces live here: the credential class
// (consumerCreds) and the live event fan-out (watchHub); the wire surface
// (ConsumerService) is the adapter's, projecting WatchRuns/ListRuns/SpoolStats.

// consumerCreds mints and verifies the read-only consumer credential class: a
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

// watchHub is the live consumer broadcast: every Event HandleEvent
// processes on ANY live RunChannel is teed here for WatchRuns subscribers,
// full payload (including delta text) — the durable journal stays
// counts-only for deltas (items.go); this is a separate, additive read
// path, never the source of truth.
type watchHub struct {
	rep  report.Reporter
	mu   sync.Mutex
	subs map[*watchSub]struct{}
}

func newWatchHub(rep report.Reporter) *watchHub {
	return &watchHub{rep: rep, subs: make(map[*watchSub]struct{})}
}

// watchSub is one WatchRuns call's subscription. runIDs nil/empty means
// "every run visible to this credential" (visibility is not scoped below
// the whole project — a consumer credential sees the whole
// coordinator, matching its loopback-only, host-local trust boundary).
//
// lost is non-nil while this subscriber is LAGGED: events were not queued
// (ring full) or were evicted (sendTerminal) and the subscriber has not yet
// been told. It is flushed as ONE synthetic EventsLost marker the moment the
// ring has room for the marker AND the next event, so the marker always
// precedes the first event delivered after the loss. Guarded by watchHub.mu.
type watchSub struct {
	runIDs map[string]bool
	ch     chan Event
	lost   []LostRange
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
func (h *watchHub) subscribe(runIDs map[string]bool) (events <-chan Event, cancel func(), narrow func(runID string)) {
	sub := &watchSub{runIDs: runIDs, ch: make(chan Event, watchRingSize)}
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

func isTerminal(ev Event) bool {
	_, ok := ev.Payload.(RunCompleted)
	return ok
}

// isLossMarker reports whether ev is a synthetic EventsLost marker — the
// hub's own, never a runner's.
func isLossMarker(ev Event) bool {
	_, ok := ev.Payload.(EventsLost)
	return ok
}

// isEvictable is the complement of the two kinds sendTerminal must never
// sacrifice: a terminal (its loss is unrecoverable — no seq gap ever reveals
// it, and a watcher waits on it forever) and a loss marker (evicting the
// notice of a loss is the silent drop this hub exists to rule out).
func isEvictable(ev Event) bool {
	return !isTerminal(ev) && !isLossMarker(ev)
}

// broadcast fans ev out to every matching subscriber. Two invariants govern
// this function, and both are load-bearing:
//
//  1. NEVER block the caller. This runs synchronously inside
//     HandleEvent, on the goroutine that services a live RunChannel's
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
func (h *watchHub) broadcast(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if len(sub.runIDs) > 0 && !sub.runIDs[ev.RunID] {
			continue
		}
		if isTerminal(ev) {
			sendTerminal(h.rep, sub, ev)
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
func deliver(sub *watchSub, ev Event) {
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
func (sub *watchSub) noteLost(ev Event) {
	for i := len(sub.lost) - 1; i >= 0; i-- {
		r := &sub.lost[i]
		if r.RunID != ev.RunID {
			continue
		}
		if r.LastSeq+1 == ev.Seq {
			r.LastSeq = ev.Seq
			return
		}
		break
	}
	sub.lost = append(sub.lost, LostRange{RunID: ev.RunID, FirstSeq: ev.Seq, LastSeq: ev.Seq})
}

// flushLost queues sub's pending loss as one EventsLost marker and clears
// it. The caller has already checked there is room.
func (sub *watchSub) flushLost() {
	if sub.lost == nil {
		return
	}
	sub.ch <- Event{OccurredAt: time.Now(), Payload: EventsLost{Lost: sub.lost}}
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
func sendTerminal(rep report.Reporter, sub *watchSub, ev Event) {
	for attempt := 0; attempt < terminalEvictAttempts; attempt++ {
		if sub.room() >= sub.need() {
			sub.flushLost()
			sub.ch <- ev
			return
		}
		evicted, ok := evictOneEvictable(sub.ch)
		if !ok {
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
	rep.Warnf("consumer watch: dropped terminal event for run %q after %d evict attempts on a full ring — a watcher on that run may hang until its own timeout",
		ev.RunID, terminalEvictAttempts)
}

// evictOneEvictable frees one slot in ch by removing its OLDEST evictable
// event (isEvictable) and returns it, or false when every queued event is a
// terminal or a loss marker. It drains events until it finds one to
// sacrifice, holding any protected events it must drain past and re-queuing
// them in order, so only a seq-recoverable event is removed. Stays
// non-blocking throughout (never trades away broadcast's never-block
// invariant), and runs under watchHub.mu so no concurrent broadcast races
// the drain — only the serving loop drains ch too, which is safe (it
// delivers, never loses).
func evictOneEvictable(ch chan Event) (Event, bool) {
	var held []Event
	for {
		var e Event
		select {
		case e = <-ch:
		default:
			// Ring drained without an evictable event: put the protected
			// ones back (in order) and let the retry bound decide.
			requeue(ch, held)
			return Event{}, false
		}
		if isEvictable(e) {
			requeue(ch, held)
			return e, true
		}
		held = append(held, e)
	}
}

func requeue(ch chan Event, evs []Event) {
	for _, e := range evs {
		select {
		case ch <- e:
		default:
			return
		}
	}
}

// listRunsSnapshot is the roster projection shared by every caller of the
// roster (the plane-2 roster request, serveRoster, and the consumer plane's
// ListRuns/WatchRuns snapshot below) — single state, N transports. A non-empty
// parent keeps only that harp's direct children; "" keeps every run.
func (c *Coordinator) listRunsSnapshot(includeTerminal bool, role, parent string) RunsSnapshot {
	result := RunsSnapshot{}
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
			if parent != "" && rec.ParentHarp != parent {
				continue
			}
			result.Runs = append(result.Runs, RunInfo{
				RunID: rec.RunID,
				Agent: &AgentIdentity{
					AgentID: e.Harp,
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
				ParentRunID:   rec.ParentRunID,
				// F1: the run's resolved permission mode and MCP server
				// NAMES ONLY (rec.Permission/MCPServers, fixed at enqueue) —
				// the roster consumer's only black-box view onto the
				// delegation privilege-scoping guarantee.
				PermissionMode: rec.Permission,
				MCPServers:     rec.MCPServers,
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
func (c *Coordinator) WatchRuns(runIDs []string) (snapshot RunsSnapshot, events <-chan Event, cancel func(), narrow func(runID string)) {
	var filter map[string]bool
	if len(runIDs) > 0 {
		filter = make(map[string]bool, len(runIDs))
		for _, id := range runIDs {
			filter[id] = true
		}
	}
	events, cancel, narrow = c.watch.subscribe(filter)
	snapshot = c.listRunsSnapshot(true, "", "")
	return snapshot, events, cancel, narrow
}

// ListRuns is the in-process form of ConsumerService.ListRuns.
//
// test-only: no production caller — production reaches
// listRunsSnapshot directly (the roster verb; the wire's ConsumerService). This
// was deleted once in this wave and reverted: repointing its in-package test
// call sites at listRunsSnapshot compiled fine, but
// internal/adapters/mcp/mcp_tools_agents_test.go (a different package) also calls it
// via require.Eventually to poll for a roster change — `go vet ./...`, not a
// package-scoped vet, is what caught that. Kept for that cross-package test
// caller, the same reason as LoopbackURL.
func (c *Coordinator) ListRuns(includeTerminal bool, role string) RunsSnapshot {
	return c.listRunsSnapshot(includeTerminal, role, "")
}

// SpoolStats is the spool counters snapshot the consumer plane reports.
func (c *Coordinator) SpoolStats() SpoolStats {
	delivery := c.SpoolDeliveryStats()
	doorbell := c.SpoolDoorbellStats()
	return SpoolStats{
		Delivered:        delivery.Delivered,
		Consumed:         delivery.Consumed,
		Failed:           delivery.Failed,
		DoorbellDropped:  doorbell.Dropped,
		DoorbellRejected: doorbell.Rejected,
	}
}
