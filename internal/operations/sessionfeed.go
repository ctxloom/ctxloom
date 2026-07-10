package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ctxloom/ctxloom/internal/agentbus"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// This file is the per-harp observation-feed resolver (agent-io plan §3): ONE
// feed per harp, one vocabulary (WatchEvent/SessionEntry), two sources behind
// it. The LIVE TAP — an orchestrator currently holding the child's event
// stream, reached over its agent-bus socket — is preferred; the STORE TAIL
// (the S0 locators: WatchSession by bound session id, WatchHistoryByPath by
// located transcript) is the workhorse fallback. Consumers never know which
// source fed them.

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
	}
	return watchStoreFeed(ctx, entry, backend)
}

// watchLiveFeed dials each candidate bus socket and subscribes on the first
// orchestrator that holds the harp live. A dead socket file or a typed
// not-live answer just moves to the next candidate.
func watchLiveFeed(ctx context.Context, entry *sessions.Entry, backend string) (*SessionFeed, error) {
	socks := busSocketCandidates()
	if len(socks) == 0 {
		return nil, fmt.Errorf("no coordinator bus socket found (no session-dir sockets)")
	}
	var lastErr error
	for _, sock := range socks {
		obsEvents, obsErrs, err := agentbus.ObserveSession(ctx, sock, entry.HarpName)
		if err != nil {
			lastErr = err
			continue
		}
		events, errs := adaptLiveFeed(ctx, entry, backend, obsEvents, obsErrs)
		return &SessionFeed{Source: "live", Events: events, Errs: errs}, nil
	}
	return nil, lastErr
}

// busSocketCandidates lists the sockets a live tap could sit behind: every
// session-dir socket — the coordinator binds
// <sessions>/<owner harp>/agent-bus.sock, and the index does not record a
// child's parent, so the scan is the discovery. (The ambient
// CTXLOOM_BUS_SOCKET candidate died with the executor shim in the agentcoord
// migration — children no longer carry a socket path.) Most-recently-active
// sockets are tried first; dead files fail the dial in microseconds.
func busSocketCandidates() []string {
	var out []string
	root, err := paths.HomeSessionsDir()
	if err != nil {
		return out
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*", "agent-bus.sock"))
	sort.Slice(matches, func(i, j int) bool { return socketMTime(matches[i]).After(socketMTime(matches[j])) })
	out = append(out, matches...)
	return out
}

func socketMTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// adaptLiveFeed normalizes a live observation stream onto the WatchEvent
// vocabulary, stitching scrollback from the store first: live subscriptions
// deliver from subscribe-time forward (history is the store's job), so the
// harp's recorded transcript — when one exists — replays as entry events
// ahead of the live stream. The seam is best-effort: an entry recorded while
// the snapshot read runs can appear twice (snapshot and live); a harp with no
// transcript (e.g. a generic-acp child) starts at now. Live boundaries carry
// feed-relative indexes, and after a gap they are approximate.
func adaptLiveFeed(ctx context.Context, entry *sessions.Entry, backend string, obsEvents <-chan agentbus.ObserveEvent, obsErrs <-chan error) (<-chan SessionFeedEvent, <-chan error) {
	events := make(chan SessionFeedEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
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
		for ev := range obsEvents {
			switch {
			case ev.Gap > 0:
				if !emit(SessionFeedEvent{Gap: ev.Gap}) {
					return
				}
			case ev.Entry != nil:
				if !emit(SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: pb.EntryToProto(*ev.Entry)}}}) {
					return
				}
				sent++
			case ev.Complete != nil:
				// A turn boundary with no new material (a cancelled turn) emits
				// nothing — same as the store tail's stall rule.
				if sent > lastBoundary {
					if !emit(boundaryFeedEvent(lastBoundary, sent)) {
						return
					}
					lastBoundary = sent
				}
			case ev.Ended:
				return // the child's engine exited; the feed ends with it
			}
		}
		// Stream ended without an Ended line: surface the transport fault.
		if e := <-obsErrs; e != nil {
			errs <- e
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

// feedScrollback reads the harp's recorded transcript once, for the live
// feed's scrollback prefix. Best-effort by design: a failed read degrades the
// view to live-only with a warning, never kills the feed.
func feedScrollback(ctx context.Context, entry *sessions.Entry, backend string) []agent.SessionEntry {
	var (
		sess *agent.Session
		err  error
	)
	switch {
	case entry.SessionID != "":
		sess, err = pb.NewSessionReader(backend, 0).GetSession(ctx, entry.SessionID)
	case entry.TranscriptPath != "":
		var hist agent.SessionHistory
		if hist, err = HistoryForBackend(backend); err == nil {
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
