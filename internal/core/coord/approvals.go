package coord

import (
	"context"
	"fmt"
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Approvals is the root's approval queue: the one source a presenter reads
// and the one place a request is answered.
func (c *Coordinator) Approvals() *ApprovalQueue { return c.approvals }

// parkApproval serves a run's ApprovalRequest: the request parks in the
// queue, stamped with who is asking, until it is decided. Park's context is
// the coordinator's own — the asking turn's end withdraws the request (the
// turn-idle event), as does the asking run's end (terminateRun), and the
// coordinator closing withdraws them all.
func (c *Coordinator) parkApproval(caller Identity, req ApprovalRequest) ApprovalDecision {
	p := PendingApproval{Kind: ApprovalTool, Ask: req.Ask, Transitions: req.Transitions, turn: req.turn}
	c.runs.View(func() {
		if r := c.runsF.currentRun(caller.Harp); r != nil {
			p.Agent = r.Agent
		}
		p.Lineage = c.lineageOf(caller.Harp)
	})
	p.engine = c.runEngine(caller.Harp, caller.RunID)
	return c.approvals.Park(c.baseCtx, caller, p, req.Timeout)
}

// runEngine is the engine harp's run runID was launched on; empty for a
// run this coordinator holds no runtime for.
func (c *Coordinator) runEngine(harp, runID string) engine.Name {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt := c.runtimeForLocked(harp, runID); rt != nil && rt.plan != nil {
		return engine.Name(rt.plan.Backend)
	}
	return ""
}

// covers reports whether rule, granted to req's asker, allows req's call, as
// the asker's engine judges it.
func (c *Coordinator) covers(req PendingApproval, rule string) bool {
	e, ok := c.engines.Lookup(req.engine)
	if !ok {
		return false
	}
	codec, ok := e.Approvals().Get()
	return ok && codec.Covers(rule, req.Ask)
}

// runGone reports that from's run can take no decision: its record says it
// ended, or its harp's current run is a newer one. It needs no state of its
// own — a harp's current run is never reaped, and neither is its record while
// it is current. An asker the fold does not know is not judged gone.
func (c *Coordinator) runGone(from Identity) bool {
	gone := false
	c.runs.View(func() {
		if r := c.runsF.currentRun(from.Harp); r != nil {
			gone = r.RunID != from.RunID || r.Ended
		}
	})
	return gone
}

// lineageOf is harp's delegation chain, root first. Call inside runs.View.
func (c *Coordinator) lineageOf(harp string) []string {
	chain := []string{harp}
	for r := c.runsF.currentRun(harp); r != nil && !r.TopLevel(); r = c.runsF.currentRun(r.ParentHarp) {
		if slices.Contains(chain, r.ParentHarp) {
			break
		}
		chain = append(chain, r.ParentHarp)
	}
	slices.Reverse(chain)
	return chain
}

// pushGrants hands harp's live run its full remaining grant set on eng. A
// harp with no run to reach has nothing to update: the grant set is the
// journal's. Nor has a run on another engine, which holds none of eng's
// rules.
func (c *Coordinator) pushGrants(harp string, eng engine.Name, rules []string) error {
	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			cp := *r
			rec = &cp
		}
	})
	if rec == nil || rec.Ended || !c.runnerReachable(rec) || c.runEngine(harp, rec.RunID) != eng {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.baseCtx, DefaultRequestTimeout)
	defer cancel()
	resp, err := c.requestRunner(ctx, rec.CredHash, RunnerRequest{Kind: SetGrants{RunID: rec.RunID, Rules: rules}})
	if err != nil {
		return fmt.Errorf("set grants on %s: %w", harp, err)
	}
	if resp.Err != nil {
		return fmt.Errorf("set grants on %s: the run refused: %w", harp, resp.Err)
	}
	return nil
}
