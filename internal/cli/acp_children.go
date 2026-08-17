package cli

import (
	"context"
	"strings"

	"github.com/ctxloom/ctxloom/internal/acpagent"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
)

// D3 (manly-grant (2)) production wiring: adapts the
// coordinator's D1 watch API onto acpagent.EngineChat.WatchChildren's
// abstract shape. Unlike internal/operations/sessionfeed.go's twin adapter
// (a SEPARATE process dialing ConsumerService over gRPC — this package
// cannot import coord at all, see that file's header), `ctxloom acp`
// already HOSTS the coordinator library in-process, so this calls
// Coordinator.WatchRuns directly — no network hop, no credential. Simpler
// than sessionfeed's adapter too: no scrollback stitching, no boundary
// bookkeeping, no delta accumulation — MessageDelta chunks forward AS THEY
// ARRIVE (matching how the session's OWN output already streams, one
// content chunk per session/update, mapping.go's mapEvent).

// acpChildWatcher builds an acpagent.EngineChat.WatchChildren closure bound
// to the hosted coordinator c. Every session/new on this ACP process shares
// it (one coordinator per process, per project — coord_acp.go), so a watch
// covers every delegation tree in the project, not just the caller's own
// children; scoping precisely to one session's lineage is a documented
// simplification (most ACP usage is one active session per editor window).
func acpChildWatcher(c *coord.Coordinator) func(ctx context.Context) (<-chan acpagent.ChildUpdate, func()) {
	return func(ctx context.Context) (<-chan acpagent.ChildUpdate, func()) {
		snapshot, events, cancel, _ := c.WatchRuns(nil)
		out := make(chan acpagent.ChildUpdate, 32)
		go adaptChildWatch(ctx, snapshot, events, out)
		return out, cancel
	}
}

// WatchChildren implements operations.EngineSessionCoordinator
// (internal/operations/engine_session.go): the D3 child-update watch closure
// for a session hosted by this coordinator, or nil when no coordinator has
// stood up yet — the same "coordinator() != nil" gate that used to live at
// OpenEngineSession's call site before the ISO0 extraction moved that
// function out of this package.
func (a *acpCoordinator) WatchChildren() func(context.Context) (<-chan acpagent.ChildUpdate, func()) {
	sc := a.coordinator()
	if sc == nil {
		return nil
	}
	return acpChildWatcher(sc)
}

// adaptChildWatch translates the coordinator's live AgentEvent stream into
// acpagent.ChildUpdate values until events closes or ctx ends.
func adaptChildWatch(ctx context.Context, snapshot *agentcoordpb.ListRunsResult, events <-chan *agentcoordpb.AgentEvent, out chan<- acpagent.ChildUpdate) {
	defer close(out)
	harpByRun := make(map[string]string, len(snapshot.GetRuns()))
	for _, r := range snapshot.GetRuns() {
		harpByRun[r.GetRunId()] = r.GetAgent().GetAgentId()
	}
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			u, ok := childUpdateFor(ev, harpByRun)
			if !ok {
				continue
			}
			select {
			case out <- u:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// childUpdateFor maps ONE AgentEvent onto the child update an ACP client sees,
// maintaining the run→harp table as it goes. ok is false when the event
// contributes nothing.
//
// Three of AgentEvent's sixteen payload variants are surfaced, and the silence
// on the other thirteen is a decision, not an omission: an ACP client's child
// pane is start / streamed text / completion. Step and status transitions,
// message start/stop framing, the whole tool-call family, interaction echoes,
// artifacts, summaries and the raw/custom escape hatches have no place in it,
// and forwarding them would mean inventing a ChildUpdate kind per variant.
// TestAdaptChildWatch_UnsurfacedVariantsEmitNothing pins that.
func childUpdateFor(ev *agentcoordpb.AgentEvent, harpByRun map[string]string) (acpagent.ChildUpdate, bool) {
	runID := ev.GetRunId()
	switch p := ev.GetPayload().(type) {
	case *agentcoordpb.AgentEvent_RunStarted:
		return startedUpdate(p.RunStarted, runID, harpByRun), true
	case *agentcoordpb.AgentEvent_MessageDelta:
		txt := p.MessageDelta.GetText()
		if txt == "" {
			return acpagent.ChildUpdate{}, false
		}
		return acpagent.ChildUpdate{Harp: harpByRun[runID], Kind: acpagent.ChildUpdateMessage, Text: txt}, true
	case *agentcoordpb.AgentEvent_RunCompleted:
		return completedUpdate(p.RunCompleted, runID, harpByRun), true
	default:
		return acpagent.ChildUpdate{}, false
	}
}

// startedUpdate records the run's harp (falling back to the snapshot's, for a
// start event that omits the identity) and labels the child by its role.
func startedUpdate(started *agentcoordpb.RunStarted, runID string, harpByRun map[string]string) acpagent.ChildUpdate {
	harp := started.GetAgent().GetAgentId()
	if harp == "" {
		harp = harpByRun[runID]
	}
	harpByRun[runID] = harp

	text := started.GetAgent().GetRole()
	if text == "" {
		text = "delegated task"
	}
	return acpagent.ChildUpdate{Harp: harp, Kind: acpagent.ChildUpdateStarted, Text: text}
}

// completedUpdate forgets the run's harp and reports the result text, falling
// back to the terminal status when the run produced none.
func completedUpdate(completed *agentcoordpb.RunCompleted, runID string, harpByRun map[string]string) acpagent.ChildUpdate {
	harp := harpByRun[runID]
	delete(harpByRun, runID)

	text := completed.GetResult().GetText()
	if text == "" {
		text = strings.ToLower(strings.TrimPrefix(completed.GetResult().GetStatus().String(), "RUN_STATUS_"))
	}
	return acpagent.ChildUpdate{Harp: harp, Kind: acpagent.ChildUpdateCompleted, Text: text}
}
