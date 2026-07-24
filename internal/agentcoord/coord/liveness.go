package coord

import (
	"context"
	"time"

	"github.com/ctxloom/ctxloom/internal/liveness"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// ---------------------------------------------------------------------------
// Liveness monitoring — the coordinator's adapter onto internal/liveness.
//
// On 2026-07-24 two delegated agents ran for the better part of an hour doing
// nothing, and every signal THIS type produced said they were fine: AgentRun
// returned valid ids, Roster reported state "executing", the worktree existed,
// the transcript existed and was growing. Nothing here could tell "working"
// from "looping".
//
// This file adds the missing question without changing any answer that already
// exists: it READS the folds and the runner registry, and reports. It never
// terminates a run, never releases a slot, never touches the launch gate. See
// liveness.Monitor's doc for why acting is deliberately out of scope.
// ---------------------------------------------------------------------------

// livenessPollInterval is how often the watchdog re-assesses every live child.
//
// Derived rather than picked: the monitor's own quiet grace is 10 minutes
// (liveness.DefaultThresholds), so polling far faster than that buys nothing
// for the absence-based rules — but the POSITIVE loop rules (a re-delivery
// cadence, seq pinned at 0) become true within seconds of a loop starting, and
// the whole point is to shorten "an hour of nothing" to "a minute of nothing".
// One minute is comfortably inside every threshold while costing one bounded
// transcript read per live child per minute.
const livenessPollInterval = time.Minute

// livenessMonitor lazily builds this coordinator's monitor. One per
// coordinator, because it retains per-harp CPU samples between polls (a single
// absolute CPU reading says nothing; only the difference does).
func (c *Coordinator) livenessMonitor() *liveness.Monitor {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.liveness == nil {
		c.liveness = liveness.New(liveness.Options{
			Now: c.now,
			Probes: []liveness.Probe{
				liveness.HostProbe{},
				c.runnerHeartbeatProbe(),
			},
		})
	}
	return c.liveness
}

// runnerHeartbeatProbe is the RUNTIME-POLYMORPHIC evidence this coordinator
// actually has, and it is deliberately UNIVERSAL (empty Runtime()): a runner
// dials home over RunnerChannel and heartbeats every HeartbeatInterval from
// inside a container exactly as it does from the host, so for a migrated child
// the heartbeat — not a pid — is the honest answer to "is the runtime alive".
//
// It reports CPUObserved:false always. That is not laziness, it is the
// distinction the monitor is built around: the coordinator can see THAT a
// container's runner is alive but not how hard it is working, and a probe that
// invented a CPU number would be the silent no-op wearing a different hat. The
// monitor reads the missing CPU accounting as a permanent property of this
// runtime pairing and reaches its verdict from the transcript instead.
//
// A run with no connected runner yields Observed:false, NOT Observed+dead: the
// legacy go-plugin Chat path never dials home at all, and a child on it must
// not be declared dead merely because this probe cannot see it.
func (c *Coordinator) runnerHeartbeatProbe() liveness.Probe {
	return liveness.ProbeFunc{Fn: func(_ context.Context, t liveness.Target) liveness.ProcState {
		var credHash string
		c.runs.View(func() {
			if r := c.runsF.currentRun(t.Harp); r != nil {
				credHash = r.CredHash
			}
		})
		if credHash == "" {
			return liveness.ProcState{Detail: "no run credential for this harp"}
		}
		c.mu.Lock()
		rs := c.runners[credHash]
		var last time.Time
		if rs != nil {
			last = rs.lastBeat
		}
		c.mu.Unlock()
		if rs == nil {
			return liveness.ProcState{Detail: "no runner connected (legacy chat path, or not yet dialed home)"}
		}
		since := c.now().Sub(last)
		return liveness.ProcState{
			Observed: true,
			// runnerLossTimeout is the same bound runnerWatchdog uses to
			// synthesize RunExited, so this probe and the existing loss
			// machinery can never disagree about whether a runner is gone.
			Alive:  since <= runnerLossTimeout,
			Detail: "runner heartbeat " + since.Round(time.Second).String() + " ago",
		}
	}}
}

// livenessTargets projects every LIVE child onto a liveness.Target. Ended runs
// are included for one poll's worth of grace after their terminal so a death
// mid-turn is still reported; permanently-ended harps drop out because
// Forget() clears their retained sample and the roster stops being interesting
// — but the important property is that this list is derived from the FOLDS
// (the durable, authoritative state), never from the runtime attachment map,
// which is exactly the layer that was lying during the incident.
func (c *Coordinator) livenessTargets() []liveness.Target {
	type row struct {
		harp, agent, runtime, state string
		ended                       bool
		enqueued, lastActivity      time.Time
	}
	var rows []row
	c.runs.View(func() {
		for _, e := range c.rosterF.snapshot() {
			r := c.runsF.currentRun(e.Harp)
			if r == nil {
				continue
			}
			rows = append(rows, row{
				harp: e.Harp, agent: r.Agent, runtime: r.Runtime, state: r.State,
				ended: r.Ended, enqueued: r.EnqueuedAt, lastActivity: r.LastActivity,
			})
		}
	})

	// A child parked on an approval rung must NEVER be reported as stalled.
	// Two independent sources feed that, and either is sufficient:
	// the §6a roster state, and this coordinator's own outstanding-approval
	// registry. (The transcript's trailing permission record is a third,
	// checked inside the monitor.)
	parked := make(map[string]bool)
	c.mu.Lock()
	for _, ap := range c.approvals {
		if ap != nil && ap.targetHarp != "" {
			parked[ap.targetHarp] = true
		}
	}
	c.mu.Unlock()

	out := make([]liveness.Target, 0, len(rows))
	for _, r := range rows {
		runtimeAxis := r.runtime
		if runtimeAxis == "" {
			runtimeAxis = "host"
		}
		txPath, err := paths.HarpCanonicalTranscriptPath(r.harp)
		if err != nil {
			// No resolvable transcript path is a broken observation, not an
			// observation of a broken agent: leave it empty so the monitor
			// degrades to the evidence it does have rather than reading the
			// absence as zero events.
			clidiag.Warn("ctxloom", "liveness: %s: resolve transcript path: %v", r.harp, err)
			txPath = ""
		}
		out = append(out, liveness.Target{
			Harp:             r.harp,
			Agent:            r.agent,
			Runtime:          runtimeAxis,
			StartedAt:        r.enqueued,
			LastActivity:     r.lastActivity,
			RosterState:      r.state,
			AwaitingApproval: parked[r.harp] || r.state == StateParked,
			Ended:            r.ended,
			TranscriptPath:   txPath,
		})
	}
	return out
}

// LivenessSnapshot assesses every child this coordinator holds and returns the
// verdicts. It is the ON-DEMAND surface — a caller (a CLI, a test, a future
// roster projection) asks and gets an answer computed from the folds, the
// transcripts, and the runner registry at that instant.
//
// It reports only. Nothing here stops, cancels, or reaps anything.
func (c *Coordinator) LivenessSnapshot(ctx context.Context) []liveness.Report {
	return c.livenessMonitor().AssessAll(ctx, c.livenessTargets())
}

// livenessWatchdog is the PUSH surface: a poll (not an event stream — the
// signals that matter are ABSENCES, and nothing emits an event when nothing
// happens) that warns, loudly and once per transition, whenever a child enters
// a firing state.
//
// It warns on TRANSITIONS rather than on every poll: a stall that is already
// known does not need repeating every minute, and a monitor that spams is a
// monitor that gets filtered out — which would put us back where the incident
// started, with a signal nobody reads.
func (c *Coordinator) livenessWatchdog() {
	last := map[string]liveness.State{}
	t := time.NewTicker(livenessPollInterval)
	defer t.Stop()
	for {
		select {
		case <-c.baseCtx.Done():
			return
		case <-t.C:
			seen := make(map[string]bool)
			for _, rep := range c.LivenessSnapshot(c.baseCtx) {
				seen[rep.Harp] = true
				prev := last[rep.Harp]
				last[rep.Harp] = rep.State
				if rep.Firing() && prev != rep.State {
					clidiag.Warn("ctxloom", "agent %s (%s, runtime %s) looks %s: %s",
						rep.Harp, orDash(rep.Agent), orDash(rep.Runtime), rep.State, rep.Reason)
				}
			}
			for harp := range last {
				if !seen[harp] {
					delete(last, harp)
					c.livenessMonitor().Forget(harp)
				}
			}
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
