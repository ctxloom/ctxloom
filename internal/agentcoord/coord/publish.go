package coord

import (
	"context"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// Wave C4 deliverable 1 (manly-grant (7)): the unary at-least-once event-plane
// fallback the contract describes as "for constrained agents (batch CI, flaky
// networks) and for replaying a locally journaled event log ... dedupe on
// (run_id, seq)". Two entry points:
//   - Coordinator.PublishEvents is the trusted, in-process core: an
//     already-authorized caller (the oneshot bridging in children.go) hands it
//     events for a run_id it already owns — the same shape as serveSpawnAgent
//     calling AgentRun directly with an already-validated Identity.
//   - coordService.PublishEvents is the credential-authenticated, ownership-
//     checked gRPC entry point for a real external/out-of-process publisher
//     (a batch-CI runner, or a runner replaying its own journaled log).

// PublishEvents journals a batch of AgentEvents, deduped on (run_id, seq)
// against the SAME durable items store/fold the streaming RunChannel plane-1
// path (items.go) journals into — a unary publisher and a live runner
// converge on one committed watermark per run_id. Only the ITEM-shaped
// payload kinds items.go's itemKind already recognizes (RunStarted through
// ToolCallCompleted, Interaction, Raw) are accepted here: Summary/
// ArtifactProduced/Custom have no unary caller today (they ride RunChannel's
// per-role Home.Report / custom-event seam, keyed by HARP — an AgentEvent
// carries no harp, only a run_id) and are rejected rather than silently
// misfiled under the wrong key; wiring them is additive-safe if a caller
// ever needs it.
func (c *Coordinator) PublishEvents(events []*agentcoordpb.AgentEvent) *agentcoordpb.PublishEventsResponse {
	resp := &agentcoordpb.PublishEventsResponse{CommittedSeqByRun: map[string]uint64{}}
	if len(events) == 0 {
		return resp
	}

	accept := make([]*agentcoordpb.AgentEvent, 0, len(events))
	for _, ev := range events {
		switch {
		case ev.GetRunId() == "":
			resp.Rejected = append(resp.Rejected, rejectEvent(ev, codes.InvalidArgument, "run_id is required"))
		case ev.GetSeq() == 0:
			resp.Rejected = append(resp.Rejected, rejectEvent(ev, codes.InvalidArgument, "seq is required (per-run monotonic, starts at 1)"))
		case itemKind(ev) == "":
			resp.Rejected = append(resp.Rejected, rejectEvent(ev, codes.Unimplemented, "payload kind not supported over the unary fallback in this window"))
		default:
			accept = append(accept, ev)
		}
	}
	if len(accept) == 0 {
		return resp
	}

	if err := c.items.Exec(func() ([]Fact, error) {
		facts := make([]Fact, 0, len(accept))
		for _, ev := range accept {
			// (run_id, seq) at-least-once dedupe INSIDE the journal window: the
			// streaming path leans on the live channel's own ackSeq to
			// short-circuit most repeats before they ever reach the fold; the
			// unary path has no channel, so the fold's committed watermark is
			// consulted here instead — a retried publish must not grow the
			// journal unboundedly.
			if ev.GetSeq() <= c.itemsF.maxSeq[ev.GetRunId()] {
				continue
			}
			facts = append(facts, factAt(factItem, c.now(), itemFact{
				RunID: ev.GetRunId(), Seq: ev.GetSeq(), Kind: itemKind(ev), Chars: itemChars(ev),
			}))
		}
		return facts, nil
	}); err != nil {
		for _, ev := range accept {
			resp.Rejected = append(resp.Rejected, rejectEvent(ev, codes.Internal, err.Error()))
		}
		return resp
	}

	c.items.View(func() {
		seen := make(map[string]bool, len(accept))
		for _, ev := range accept {
			runID := ev.GetRunId()
			if seen[runID] {
				continue
			}
			seen[runID] = true
			// "Highest contiguous committed seq" per the contract doc: this
			// codebase's fold tracks a rolling max watermark (not literal
			// gap-contiguity, matching the streaming path's own ackSeq
			// simplification — see runchannel.go's handleAgentEvent), so that
			// is what is reported here too.
			resp.CommittedSeqByRun[runID] = c.itemsF.maxSeq[runID]
		}
	})
	return resp
}

// rejectEvent builds one RejectedEvent for the response.
func rejectEvent(ev *agentcoordpb.AgentEvent, code codes.Code, msg string) *agentcoordpb.RejectedEvent {
	return &agentcoordpb.RejectedEvent{
		RunId:  ev.GetRunId(),
		Seq:    ev.GetSeq(),
		Reason: &rpcstatus.Status{Code: int32(code), Message: msg},
	}
}

// PublishEvents is the gRPC entry point: identity re-verified per this
// package's per-request discipline (grpcServer's unary interceptor already
// ran auth; handlers re-check anyway — see its doc), then ownership-checked
// exactly like RunChannel's Hello: every event's run_id must be the one this
// credential was issued for (a foreign run_id is rejected, never silently
// filed under someone else's watermark).
func (s *coordService) PublishEvents(ctx context.Context, req *agentcoordpb.PublishEventsRequest) (*agentcoordpb.PublishEventsResponse, error) {
	c := s.c
	id, ok := c.Identify(mdToken(ctx))
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unknown or revoked credential")
	}
	for _, ev := range req.GetEvents() {
		if ev.GetRunId() != id.RunID {
			return nil, status.Errorf(codes.PermissionDenied, "run %q was not issued to this credential", ev.GetRunId())
		}
	}
	return c.PublishEvents(req.GetEvents()), nil
}
