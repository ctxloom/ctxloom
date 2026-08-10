package operations

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/discover"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// This file is the per-harp observation-feed resolver (agent-io plan §3): ONE
// feed per harp, one vocabulary (WatchEvent/SessionEntry), two sources behind
// it. The LIVE TAP — a coordinator currently holding the child's run, reached
// over its D1 ConsumerService (internal/agentcoord/discover finds candidate
// coordinators; this package cannot import internal/agentcoord/coord
// directly — that package imports operations, so the reverse import would
// cycle) — is preferred; the STORE TAIL (the S0 locators: WatchSession by
// bound session id, WatchHistoryByPath by located transcript) is the
// workhorse fallback. Consumers never know which source fed them.
//
// D2 note: this package cannot import internal/agentcoord/coord (the cycle
// above), so it cannot construct coord.Identity or read coord's exported
// vocabulary directly — it speaks the wire contract (internal/agentcoord,
// the generated proto package, which has no such cycle) over a bare gRPC
// client instead.

// FeedSource selects how WatchSessionFeed sources a harp's feed.
type FeedSource string

const (
	// FeedSourceAuto prefers the live tap when an orchestrator holds the harp,
	// falling back to the store tail.
	FeedSourceAuto FeedSource = "auto"
	// FeedSourceLive requires the live tap; errors when no orchestrator holds
	// the harp (a debugging aid — auto is the published behavior).
	FeedSourceLive FeedSource = "live"
	// FeedSourceStore skips live discovery entirely (S0 behavior).
	FeedSourceStore FeedSource = "store"
)

// ParseFeedSource validates a --source flag value; empty means auto.
func ParseFeedSource(s string) (FeedSource, error) {
	switch FeedSource(s) {
	case "", FeedSourceAuto:
		return FeedSourceAuto, nil
	case FeedSourceLive, FeedSourceStore:
		return FeedSource(s), nil
	}
	return "", fmt.Errorf("unknown feed source %q (want auto, live, or store)", s)
}

// SessionFeedEvent is one event on a unified observation feed. Exactly one
// field is meaningful: Event carries a WatchEvent (entry/boundary/heartbeat —
// the S0 vocabulary); Gap > 0 reports that a live observer's bounded buffer
// dropped that many events (live source only; boundary indexes after a gap
// are approximate).
type SessionFeedEvent struct {
	Event *pb.WatchEvent
	Gap   int
}

// SessionFeed is one resolved observation feed. Events closes when the feed
// ends — for the live source that is the child's engine exiting (re-watch to
// follow a resume); the store tail runs until the consumer cancels. Errs
// (buffered) carries a terminal stream error after Events drains.
type SessionFeed struct {
	// Source is the source that actually feeds the stream: "live" or "store".
	Source string
	Events <-chan SessionFeedEvent
	Errs   <-chan error
}

// SessionFeedRequest names the harp and source policy for WatchSessionFeed.
type SessionFeedRequest struct {
	Harp   string
	Source FeedSource
}

// WatchSessionFeed resolves a harp to its observation feed. Only delegation
// CHILDREN held by a live orchestrator are tappable (the orchestrator never
// drives its own serving session's engine), so most sessions — including
// every coordinator — resolve to the store tail.
func WatchSessionFeed(ctx context.Context, req SessionFeedRequest) (*SessionFeed, error) {
	entry, err := GetSession(req.Harp)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("harp not found: %q", req.Harp)
	}
	backend := entry.Backend
	if backend == "" {
		cfg, cerr := config.Load()
		if cerr != nil {
			return nil, fmt.Errorf("load config: %w", cerr)
		}
		backend = cfg.GetDefaultLLM()
	}

	source := req.Source
	if source == "" {
		source = FeedSourceAuto
	}
	if source != FeedSourceStore {
		feed, lerr := watchLiveFeed(ctx, entry, backend)
		if lerr == nil {
			return feed, nil
		}
		if source == FeedSourceLive {
			return nil, fmt.Errorf("no live tap for %q (only children an orchestrator currently holds are tappable): %w", req.Harp, lerr)
		}
		// Auto mode used to discard lerr entirely here — a
		// coordinator that is up but rejecting the bearer credential (a real
		// D1 auth problem) was indistinguishable from one that simply isn't
		// holding the harp. Warn before falling back so the failure has SOME
		// visible signal; the fallback itself is still the right behavior.
		clidiag.Warn("ctxloom", "watch %s: live tap unavailable, using store tail: %v", req.Harp, lerr)
	}
	return watchStoreFeed(ctx, entry, backend)
}

// watchLiveFeed dials each candidate coordinator (internal/agentcoord/
// discover, most-recently-active first) over ConsumerService and subscribes
// on the first one that holds the harp live. A candidate the harp isn't
// live on, or that's unreachable, just moves to the next.
func watchLiveFeed(ctx context.Context, entry *sessions.Entry, backend string) (*SessionFeed, error) {
	endpoints, skipped := discover.List()
	// Skipped candidates (unreadable/undecodable endpoint.json, or
	// discovery itself failing) used to be indistinguishable from "no
	// endpoint file exists at all" — surface them so a permission error or a
	// corrupt state dir does not masquerade as "nothing is running".
	for _, serr := range skipped {
		clidiag.Warn("ctxloom", "discover coordinator endpoint: %v", serr)
	}
	if len(endpoints) == 0 {
		if len(skipped) > 0 {
			return nil, fmt.Errorf("no usable coordinator endpoint found: %d candidate(s) present but skipped (%v)", len(skipped), skipped[0])
		}
		return nil, fmt.Errorf("no coordinator endpoint found (no ~/.ctxloom/coord/*/endpoint.json)")
	}
	var lastErr error
	for _, ep := range endpoints {
		feed, err := watchConsumerFeed(ctx, ep, entry, backend)
		if err == nil {
			return feed, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// consumerDialTimeout bounds the ListRuns lookup that decides whether a
// candidate coordinator holds the harp — the WatchRuns stream itself, once
// opened, has no deadline (it runs until the caller cancels or the run
// ends).
const consumerDialTimeout = 5 * time.Second

// bearerToken is a grpc.PerRPCCredentials carrying a D1 consumer credential
// (mirrors coord/runnerlink.go's bearerCreds — that package cannot be
// imported here, see the file header).
type bearerToken string

func (b bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}
func (bearerToken) RequireTransportSecurity() bool { return false }

// watchConsumerFeed dials one coordinator candidate, resolves the harp to a
// live run_id via ListRuns (ConsumerService has no by-harp lookup — the
// roster is small; a client-side scan is simplest), and opens WatchRuns
// filtered to that run. Returns an error (never partially wires up a feed)
// when this candidate does not hold the harp, so the caller moves on.
func watchConsumerFeed(ctx context.Context, ep discover.Endpoint, entry *sessions.Entry, backend string) (*SessionFeed, error) {
	u, err := url.Parse(ep.URL)
	if err != nil {
		return nil, fmt.Errorf("watch: parse coordinator endpoint %q: %w", ep.URL, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("watch: coordinator endpoint %q has no host", ep.URL)
	}
	conn, err := grpc.NewClient(u.Host,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerToken(ep.Cred)))
	if err != nil {
		return nil, fmt.Errorf("watch: dial coordinator %s: %w", ep.URL, err)
	}
	client := agentcoordpb.NewConsumerServiceClient(conn)

	lctx, cancel := context.WithTimeout(ctx, consumerDialTimeout)
	runs, err := client.ListRuns(lctx, &agentcoordpb.ListRunsRequest{IncludeTerminal: false})
	cancel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("watch: list runs at %s: %w", ep.URL, err)
	}
	runID := ""
	for _, r := range runs.GetRuns() {
		if r.GetAgent().GetAgentId() == entry.HarpName {
			runID = r.GetRunId()
			break
		}
	}
	if runID == "" {
		_ = conn.Close()
		return nil, fmt.Errorf("watch: %q is not held live by the coordinator at %s", entry.HarpName, ep.URL)
	}

	stream, err := client.WatchRuns(ctx, &agentcoordpb.WatchRunsRequest{RunIds: []string{runID}})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("watch: open WatchRuns at %s: %w", ep.URL, err)
	}
	// The first frame is always a RosterSnapshot (D1 contract) — this call
	// already has a fresher one from ListRuns above; discard it.
	if _, err := stream.Recv(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("watch: read snapshot frame at %s: %w", ep.URL, err)
	}

	events, errs := adaptConsumerFeed(ctx, entry, backend, conn, stream)
	return &SessionFeed{Source: "live", Events: events, Errs: errs}, nil
}

// consumerFeedState is the per-feed item-lifecycle accumulator: the reverse
// of enginehost.go's forward adapt() (agent.ChatEvent -> AgentEvent) — here
// AgentEvent's started/delta*/completed item lifecycle folds back onto whole
// agent.SessionEntry values the existing WatchEvent/NDJSON vocabulary
// already renders. Best-effort, like the retired agentbus adapter it
// replaces: an entry recorded while the snapshot read runs can appear twice
// (scrollback and live).
type consumerFeedState struct {
	msgs  map[string]*agent.SessionEntry // message_id -> accumulating entry
	tools map[string]string              // tool_call_id -> tool name (for the matching result)
}

// entryTypeFromRoute reverses enginehost.go's messageRouting: role+channel
// back to the transcript's EntryType. messageRouting emits exactly three
// combinations — ASSISTANT/FINAL (the answer), ASSISTANT/REASONING
// (thinking), and SYSTEM/LOG (everything else, including its default arm) —
// so this is a closed, small mapping, not a general contract projection.
//
// The channel is only consulted to separate the first two: anything that is
// not ASSISTANT/REASONING and carries the ASSISTANT role lands on
// EntryTypeAssistant. That is deliberate for FINAL and INCIDENTAL for
// MESSAGE_CHANNEL_UNSPECIFIED, whose meaning the readers of this field do
// not agree on — coord's accumulateFinalText and cli's owned-run renderer
// admit FINAL only, so they read 0 as "not the answer" while this mapping
// reads ASSISTANT plus 0 as the answer. Nothing emits 0 today
// (TestMessageRouting_NeverEmitsAnUnspecifiedChannel pins that), and what 0
// ought to mean is a question about the wire enum, not about this function.
func entryTypeFromRoute(role agentcoordpb.MessageRole, channel agentcoordpb.MessageChannel) agent.SessionEntryType {
	switch {
	case role == agentcoordpb.MessageRole_MESSAGE_ROLE_ASSISTANT && channel == agentcoordpb.MessageChannel_MESSAGE_CHANNEL_REASONING:
		return agent.EntryTypeThinking
	case role == agentcoordpb.MessageRole_MESSAGE_ROLE_ASSISTANT:
		return agent.EntryTypeAssistant
	default:
		return agent.EntryTypeSystem
	}
}

// customEventTurnIdle mirrors coord.CustomTurnIdle (internal/agentcoord/
// coord/runchannel.go) — a literal string duplicate, documented on both
// sides, because this package cannot import coord (see the file header).
const customEventTurnIdle = "ctxloom/turn_idle"

// adaptConsumerFeed normalizes a live ConsumerService.WatchRuns stream onto
// the WatchEvent vocabulary, stitching scrollback from the store first (see
// adaptLiveFeed's retired doc comment for the rationale — unchanged by D2).
// The D1 watchHub broadcast this stream rides (consumer.go) is a
// non-blocking send on a bounded per-subscriber ring: a stalled subscriber
// silently loses events at the HUB, which never notices the drop itself.
// This adapter recovers truth from the wire's own seq (per-run monotonic,
// starts at 1, no gaps at the source — coordination.proto's AgentEvent.seq
// contract): a jump ahead of the last observed seq means the hub dropped
// exactly that many events, and the loop below emits a Gap marker for it
// before converting the arriving event's own payload. See the seq
// bookkeeping just inside the main receive loop for the three disciplines
// (mid-run baseline join, reconnect-reissue duplicates, seq==0 tolerance).
func adaptConsumerFeed(ctx context.Context, entry *sessions.Entry, backend string, conn *grpc.ClientConn, stream grpc.ServerStreamingClient[agentcoordpb.WatchEvent]) (<-chan SessionFeedEvent, <-chan error) {
	events := make(chan SessionFeedEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		defer func() { _ = conn.Close() }()
		emit := func(fe SessionFeedEvent) bool {
			select {
			case events <- fe:
				return true
			case <-ctx.Done():
				return false
			}
		}
		sent := 0
		for _, e := range feedScrollback(ctx, entry, backend) {
			if !emit(SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: pb.EntryToProto(e)}}}) {
				return
			}
			sent++
		}
		if sent > 0 {
			if !emit(boundaryFeedEvent(0, sent)) {
				return
			}
		}
		lastBoundary := sent

		// Seq-discontinuity tracking (truthful gap detection — see this
		// function's doc comment). haveSeq gates the BASELINE case: a
		// subscription can join mid-run, so the jump from zero to the
		// first observed seq is not a gap, just where we started looking.
		var lastSeq uint64
		haveSeq := false

		st := consumerFeedState{msgs: map[string]*agent.SessionEntry{}, tools: map[string]string{}}
		flush := func(e agent.SessionEntry) bool {
			sent++
			return emit(SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: pb.EntryToProto(e)}}})
		}
		boundary := func() bool {
			if sent <= lastBoundary {
				return true // a boundary with no new material emits nothing
			}
			ok := emit(boundaryFeedEvent(lastBoundary, sent))
			lastBoundary = sent
			return ok
		}

		for {
			frame, rerr := stream.Recv()
			if rerr != nil {
				if rerr != io.EOF && ctx.Err() == nil {
					errs <- fmt.Errorf("watch: consumer stream: %w", rerr)
				}
				return
			}
			ev := frame.GetEvent()
			if ev == nil {
				continue // a stray extra snapshot frame — ignored
			}

			// Truthful gap detection: a hub-side drop (consumer.go's
			// broadcast, non-blocking on a full ring) leaves no trace at
			// the hub, but the source stamps seq contiguously — so a jump
			// here is the drop's only remaining evidence. Emitted BEFORE
			// this event's own payload conversion below: the renderers
			// (CLI session_watch.go, tui feed.go) expect Gap to arrive as
			// its own SessionFeedEvent, not attached to the entry after it.
			if seq := ev.GetSeq(); seq != 0 {
				switch {
				case !haveSeq:
					lastSeq, haveSeq = seq, true
				case seq <= lastSeq:
					// A reconnect reissues unacked events verbatim
					// (home.go), so a subscriber can see a seq repeat.
					// That is a duplicate, not a gap: ignore it for gap
					// accounting and leave lastSeq where it stood.
				default:
					if seq > lastSeq+1 {
						if !emit(SessionFeedEvent{Gap: int(seq - lastSeq - 1)}) {
							return
						}
					}
					lastSeq = seq
				}
			}
			// seq == 0 (no in-repo producer emits it, but the streaming
			// path tolerates it): skip gap accounting entirely for this
			// event: neither a baseline, a duplicate, nor a gap.

			switch p := ev.GetPayload().(type) {
			case *agentcoordpb.AgentEvent_MessageStarted:
				st.msgs[p.MessageStarted.GetMessageId()] = &agent.SessionEntry{
					Type: entryTypeFromRoute(p.MessageStarted.GetRole(), p.MessageStarted.GetChannel()),
				}
			case *agentcoordpb.AgentEvent_MessageDelta:
				if m := st.msgs[p.MessageDelta.GetMessageId()]; m != nil {
					m.Content += p.MessageDelta.GetText()
				}
			case *agentcoordpb.AgentEvent_MessageCompleted:
				id := p.MessageCompleted.GetMessageId()
				if m := st.msgs[id]; m != nil {
					delete(st.msgs, id)
					if !flush(*m) {
						return
					}
				}
			case *agentcoordpb.AgentEvent_ToolCallStarted:
				st.tools[p.ToolCallStarted.GetToolCallId()] = p.ToolCallStarted.GetToolName()
				if !flush(agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: p.ToolCallStarted.GetToolName()}) {
					return
				}
			case *agentcoordpb.AgentEvent_ToolCallCompleted:
				id := p.ToolCallCompleted.GetToolCallId()
				name := st.tools[id]
				delete(st.tools, id)
				if !flush(agent.SessionEntry{
					Type: agent.EntryTypeToolResult, ToolName: name,
					ToolOutput: p.ToolCallCompleted.GetResultText(), IsError: p.ToolCallCompleted.GetIsError(),
				}) {
					return
				}
			case *agentcoordpb.AgentEvent_Custom:
				if p.Custom.GetName() == customEventTurnIdle {
					if !boundary() {
						return
					}
				}
			case *agentcoordpb.AgentEvent_RunCompleted:
				// Flush any message left open by an abrupt ending, then end
				// the feed — no further events will ever arrive for this
				// run_id (a resume mints a fresh one).
				for id, m := range st.msgs {
					delete(st.msgs, id)
					if !flush(*m) {
						return
					}
				}
				_ = boundary()
				return
			}
		}
	}()
	return events, errs
}

func boundaryFeedEvent(from, to int) SessionFeedEvent {
	return SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Boundary{Boundary: &pb.ResponseBoundary{
		FromIndex: int32(from),
		ToIndex:   int32(to),
	}}}}
}

// historyForBackend resolves a backend's session history reader for
// feedScrollback's by-location locator. A package-private indirection over
// HistoryForBackend (never exported) so an in-package test can substitute a
// fake agent.SessionHistory without a new public test-only API — see
// TestFeedScrollback_NilSessionNoErrorWarns.
var historyForBackend = HistoryForBackend

// feedScrollback reads the harp's recorded transcript once, for the live
// feed's scrollback prefix. Best-effort by design: a failed read degrades the
// view to live-only with a warning, never kills the feed.
func feedScrollback(ctx context.Context, entry *sessions.Entry, backend string) []agent.SessionEntry {
	var (
		sess *agent.Session
		err  error
	)
	switch {
	case entry.CanonicalTranscriptPath != "":
		// Tough-cloud S4: ctxloom's own captured transcript is available
		// host-side with no plugin round-trip, regardless of where the engine
		// ran — prefer it over both locators below.
		sess, err = transcript.ParseTranscriptFile(entry.CanonicalTranscriptPath, entry.HarpName)
	case entry.SessionID != "":
		sess, err = pb.NewSessionReader(backend, 0).GetSession(ctx, entry.SessionID)
	case entry.TranscriptPath != "":
		var hist agent.SessionHistory
		if hist, err = historyForBackend(backend); err == nil {
			sess, err = hist.GetSessionByPath(entry.TranscriptPath)
		}
	default:
		return nil // no transcript association — live-only, from now
	}
	if err != nil {
		clidiag.Warn("ctxloom", "watch %s: scrollback unavailable, starting live-only: %v", entry.HarpName, err)
		return nil
	}
	if sess == nil {
		// A nil session with no error produces the exact same
		// user-visible outcome as the warned-error branch above (no
		// scrollback) but used to say nothing at all — the one case that
		// looked like a bug (or a genuinely empty transcript) was the one
		// left unexplained.
		clidiag.Warn("ctxloom", "watch %s: scrollback unavailable (empty session), starting live-only", entry.HarpName)
		return nil
	}
	return sess.Entries
}

// watchStoreFeed is the S0 store tail behind the unified shape. Two locators,
// one contract: a hook-bound session id is tailed through the owning
// backend's agent server (WatchSession); an entry bound only by location — a
// transcript discovered in the harp's own persist/ store, where the bind hook
// never fired — is tailed host-side by path (WatchHistoryByPath), since the
// agent server's self-situated store lookup cannot see a file in ctxloom's
// session dir.
func watchStoreFeed(ctx context.Context, entry *sessions.Entry, backend string) (*SessionFeed, error) {
	var (
		watchEvents <-chan *pb.WatchEvent
		errs        <-chan error
	)
	switch {
	case entry.CanonicalTranscriptPath != "":
		// Tough-cloud S4: prefer ctxloom's own captured transcript — host-side,
		// no plugin round-trip, and correct regardless of which engine or
		// container ran the session (plan §4b "session transcript watch live feed").
		watchEvents, errs = pb.WatchCanonicalTranscript(ctx, entry.CanonicalTranscriptPath, entry.HarpName, 0)
	case entry.SessionID != "":
		var err error
		watchEvents, errs, err = pb.NewSessionReader(backend, 0).WatchSession(ctx, entry.SessionID)
		if err != nil {
			return nil, fmt.Errorf("watch %s: %w", entry.HarpName, err)
		}
	case entry.TranscriptPath != "":
		hist, err := HistoryForBackend(backend)
		if err != nil {
			return nil, fmt.Errorf("watch %s: %w", entry.HarpName, err)
		}
		watchEvents, errs = pb.WatchHistoryByPath(ctx, hist, entry.TranscriptPath, 0)
	default:
		return nil, fmt.Errorf("harp %q has no session bound and no transcript in its session store; nothing to watch yet (the SessionStart bind hook records the id for sessions launched via ctxloom run; containerized runs surface their transcript once the engine writes it)", entry.HarpName)
	}

	events := make(chan SessionFeedEvent)
	go func() {
		defer close(events)
		for ev := range watchEvents {
			select {
			case events <- SessionFeedEvent{Event: ev}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return &SessionFeed{Source: "store", Events: events, Errs: errs}, nil
}

// HistoryForBackend returns the named backend's in-process transcript reader,
// used for host-located (by-location) transcript reads.
func HistoryForBackend(name string) (agent.SessionHistory, error) {
	b := backends.Get(name)
	if b == nil {
		return nil, fmt.Errorf("unknown backend %q", name)
	}
	h := b.History()
	if h == nil {
		return nil, fmt.Errorf("backend %q has no session history", name)
	}
	return h, nil
}
