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
// A delegated agent can loop indefinitely while every signal THIS type
// produces says it is fine: AgentRun returns valid ids, Roster reports state
// "executing", the worktree exists, the transcript exists and is growing.
// Nothing here could tell "working" from "looping".
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
// coordinator, so the probe closure below can see this coordinator's runner
// registry.
func (c *Coordinator) livenessMonitor() *liveness.Monitor {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.liveness == nil {
		c.liveness = liveness.New(liveness.Options{
			Now: c.now,
			// The runner heartbeat is the ONLY process evidence this
			// coordinator has. The host pid probe that used to sit beside it
			// was inert: Spawner.StartEngine returns a Kill closure, never a
			// pid, so no liveness.Target ever carried one.
			Probes: []liveness.Probe{c.runnerHeartbeatProbe()},
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
// It answers ALIVE only, never "how hard is it working": the coordinator can
// see THAT a container's runner is alive but not what it is doing, and a probe
// that invented a busy-ness number would be the silent no-op wearing a
// different hat. The "working but silent" question is answered instead by
// Target.WorkDir's mtime clock, which is real evidence rather than an
// inference.
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
// are included and answered by the monitor's endedRung rather than dropped
// here, so a death mid-turn is still reported and a clean end is never
// condemned.
//
// The list is derived from the FOLDS (the durable, authoritative state), never
// from the runtime attachment map, which is exactly the layer that was lying
// during the incident. The one exception is WorkDir, taken from the live
// attachment below: a worktree PATH is a static fact about a spawn, not a
// claim about whether anything is happening in it, so reading it from the
// attachment cannot reintroduce the lie — and its mtime is the only activity
// clock the monitor has that the engine does not write itself.
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
				harp: e.Harp, agent: r.Agent, runtime: string(r.Runtime), state: r.State,
				ended: r.Ended, enqueued: r.EnqueuedAt, lastActivity: r.LastActivity,
			})
		}
	})

	// A child waiting on a permission decision must NEVER be reported as
	// stalled. The §6a roster state carries that here; the transcript's
	// trailing permission record is a second, independent source, checked
	// inside the monitor.
	workDirs := make(map[string]string)
	c.mu.Lock()
	for harp, rt := range c.byHarp {
		if rt != nil {
			workDirs[harp] = rt.workDir
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
			AwaitingApproval: r.state == StateParked,
			Ended:            r.ended,
			TranscriptPath:   txPath,
			WorkDir:          workDirs[r.harp],
		})
	}
	return out
}

// livenessSnapshot assesses every child this coordinator holds and returns the
// verdicts. It is the ON-DEMAND surface — a caller (a CLI, a test, a future
// roster projection) asks and gets an answer computed from the folds, the
// transcripts, and the runner registry at that instant.
//
// It reports only. Nothing here stops, cancels, or reaps anything.
func (c *Coordinator) livenessSnapshot(ctx context.Context) []liveness.Report {
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
			for _, rep := range c.livenessSnapshot(c.baseCtx) {
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
