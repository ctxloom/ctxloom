package coord

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// The owner-owned run: the owning `ctxloom run` process hosts this
// coordinator in-process (mcp.HostCoordinatorForSession), so minting its own
// run and driving/watching it is a LIBRARY call — no RPC, no change to
// coordination.proto. The run reuses the credential-based ownership model
// unchanged: it is enqueued parent-less with the owner's harp as its role,
// its runner dials home on the ordinary RunnerChannel with the per-run
// credential minted here, and the host consumes it via the in-process
// Coordinator.WatchRuns. The runner side is the same runner a delegated
// child gets (runner.Main with the run-id trio).

// OwnerRun is the owner-owned run as the owning `ctxloom run` hands it in:
// the owner's resolved launch, the same launch as StartRun carries it (the
// codec's projection, made where the launch was opened), and the composed
// MCP names the enqueue journal records. The owner-owned run REUSES the
// owner's harp (Launch.Identity.Harp) as its run role so transcript and
// session identity (state mounts, the harp carrier) stay coherent; the
// collision-audit test pins that the role-keyed maps stay coherent under
// this reuse. OneShot marks a --print single-turn run: the run tears down
// after the host collects the final answer, which changes only the host's
// wait mode.
//
// Rebind re-mints the launch's session endpoint (launch.RebindEndpoint over
// the caller's deps) when the runner reports it cannot bind the minted
// address: the mint only reserves a port until it returns, so another process
// can take it before the runner binds. The run answers that refusal ONCE, as
// a delegated child's does; a nil Rebind has no answer and the run fails.
type OwnerRun struct {
	Launch     launch.Launch
	MCPServers []agent.ChatMCPServer
	OneShot    bool
	Rebind     func(ctx context.Context, l launch.Launch) (launch.Launch, error)
}

// OwnedRunner is the runner an OwnedRunStarter stood up.
//
// Kill tears the runner down; StartOwnedRun invokes it if the post-launch
// handshake fails, and the caller owns it for normal teardown (the host
// `ctxloom run` defers isolation.RunnerHandle.Kill).
//
// Wait blocks until the runner PROCESS exits and reports why; a clean exit is
// nil. It is the death half of the dial-home race (issueStartRun): without it
// a runner that exits before dialing home is noticed only when the whole
// dial-home budget expires. It may be called more than once and concurrently,
// every call reporting the same exit — StartOwnedRun waits on it while the
// caller may too (isolation.RunnerHandle.Wait already has this shape). Nil
// only when there is no process to wait on, which degrades to that budget.
//
// ContainerName is isolation.RunnerHandle.Name (fragile-volatile) — "" for a
// host-runtime starter — and StartOwnedRun journals it onto the run record so
// it reaches the roster; coord cannot read it off isolation.RunnerHandle
// directly without importing lm/isolation, which this seam exists to avoid.
type OwnedRunner struct {
	Kill          func()
	Wait          func() error
	ContainerName string
}

// OwnedRunStarter launches the runner process for an owner-owned run with the
// per-run reach-back env stamped on. It mirrors isolation.EngineStarter but
// takes the spawn env because the run-id + credential trio it must carry is
// minted INSIDE StartOwnedRun (a pre-bound isolation.EngineStarter cannot know
// them yet).
type OwnedRunStarter func(ctx context.Context, spawnEnv map[string]string) (OwnedRunner, error)

// StartOwnedRun mints a PARENT-LESS, owner-owned run and drives it onto
// Transport 2: journal a runEnqueued with ParentHarp = owner.Harp,
// ParentRunID = "" (Depth 1), spawn the runner via start (with the per-run
// reach-back trio), await its RunnerChannel dial-home, and issue StartRun over
// that channel — the identical wire crossing runChildViaStartRun makes for a
// delegated child, via the shared issueStartRun tail. On return the run is
// live; the caller renders/collects it via WatchRuns and enqueues follow-up
// turns via SendOwnedRunTurn. On any launch/handshake failure the run is failed
// (terminateRun, exactly-once) and the error returned; the caller's deferred
// runner teardown still runs.
func (c *Coordinator) StartOwnedRun(ctx context.Context, owner Identity, spec OwnerRun, start OwnedRunStarter, prompt string) (*RunOutcome, error) {
	if c.Draining() {
		return nil, fmt.Errorf("owner run: %w", ErrDraining)
	}
	if spec.Launch.Identity.Harp == "" {
		return nil, errors.New("owner run: a resolved launch with its harp is required (the run reuses the owning session's harp as its role)")
	}
	if spec.Launch.Engine == "" {
		return nil, errors.New("owner run: the launch names no engine")
	}
	if start == nil {
		return nil, errors.New("owner run: a runner starter is required")
	}
	// A ONE-SHOT run gets exactly one turn and this prompt is it, so
	// an empty one can only be a delivery failure upstream — and it used to
	// sail through: issueStartRun builds Input only `if first != ""`, so the
	// StartRun went out with a nil Input, round-tripped, and this returned a
	// populated RunOutcome and nil. A top-level container run with zero
	// payload and every signal green. A STRUCTURED run is different: it
	// legitimately opens with no lead and takes its turns via
	// SendOwnedRunTurn, so it is not refused here.
	if spec.OneShot && prompt == "" {
		return nil, errors.New("owner run: a one-shot run needs a prompt — it gets exactly one turn, " +
			"and an empty first turn delivers nothing at all (check context assembly and the --print/stdin prompt source)")
	}
	// The dial-home IS the run's transport — a runner that could never dial
	// home has no transport at all, so an unresolvable endpoint is fatal here
	// (never a silent degrade, unlike a delegated child which can fall back
	// to a message-less local orchestrator). The runtime axis is the
	// launch's; its cell re-minted the reach for its runtime and names any
	// listener beyond loopback that re-mint needs.
	runtime := spec.Launch.Axes.Runtime
	url, err := c.ReachURL(runtime)
	if err != nil {
		return nil, reachRefusal("owner run", runtime, err, "")
	}
	if err := c.honourListen(spec.Launch.Cell.Listen); err != nil {
		return nil, fmt.Errorf("owner run: the runner has no listener to dial home to: %w", err)
	}

	// A synthetic plan carrying exactly what enqueueRun journals and
	// issueStartRun reads: the owner's harp AS the run role (AgentName), the
	// resolved launch, and the composed MCP set. No binding is selected —
	// the host already resolved the launch.
	l := spec.Launch
	plan := &SpawnPlan{
		AgentName:  l.Identity.Harp,
		Backend:    string(l.Engine),
		Label:      l.Label.Label,
		Runtime:    runtime,
		Permission: l.Permission.String(),
		MCPServers: spec.MCPServers,
		Launch:     l,
	}

	// The owned run REUSES the owner's own identity rather than spawning a
	// child of it — it IS the session owner, running over a different
	// transport (a container run instead of the plugin-hosted path).
	// So its stamped depth is the owner's OWN depth (owner.Depth, normally
	// 0), not owner.Depth+1: enqueueRun's depth parameter is explicit for
	// exactly this reason (a genuine child, by contrast, always gets
	// caller.Depth+1 — see AgentRun). This is what keeps a top-level
	// container session able to delegate on the same terms as the
	// plugin-hosted top-level session: both present depth 0 to the
	// recursion guard and to the runner-side leaf computation.
	//
	// This also flips Identity.IsChild() (Depth > 0) to FALSE for the owned
	// run's own credential, where it used to be true (depth was 1 before
	// this depth parameter existed). That corrects three call sites that
	// gate on IsChild() — peerSend's childSend/ownerSend split, AgentStop,
	// and serveRoster — each of which was refusing or
	// misrouting a call the owned run's OWN engine made about ITSELF
	// (childSend's ParentHarp resolution hit the self-loop below; AgentStop
	// and roster both explicitly refuse an IsChild() caller).
	rt, token, err := c.enqueueRun(owner, plan, l.Identity.Harp, prompt, false, make(chan struct{}), owner.Depth)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	rt.ownerRun = true
	rt.oneshot = spec.OneShot
	c.mu.Unlock()
	c.setState(rt, StateExecuting)
	c.audit("owner_run", owner.Harp, map[string]string{"harp": l.Identity.Harp, "run_id": rt.runID, "backend": string(l.Engine)})

	runner, err := start(ctx, runnerEnv(rt.runID, token, url))
	if err != nil {
		// ONE error, both destinations: the run's terminal record and the
		// caller get the same text. Returning the bare cause here left the
		// operator-facing path (this error reaches `ctxloom run`'s stderr) less
		// informative than the journal.
		err = fmt.Errorf("owner run: runner launch failed: %w", err)
		c.failChild(rt, err)
		return nil, err
	}
	c.mu.Lock()
	rt.close = runner.Kill
	rt.runnerWait = runner.Wait
	c.mu.Unlock()
	c.recordContainerName(rt.runID, runner.ContainerName)

	// The runner leads the first turn with the package's context ahead of
	// Launch.Prompt; prompt is what the host asked for this run.
	//
	// This bare return LOOKS like it skips cleanup (the two
	// failure paths above both call c.failChild explicitly), but it does
	// not — issueStartRun calls c.failChild itself on every one of its
	// error-return paths (awaitRunner timeout, payload guard, StartRun
	// wire/refusal) before returning a non-nil error. Adding a second
	// c.failChild call here would double-count c.noteLaunchFailure and
	// corrupt the launch-retry budget for a failure that was already fully
	// handled. Pinned by TestStartOwnedRun_CleansUpOnIssueStartRunFailure
	// (owner_run_cleanup_test.go): rt.close fires and the run leaves the
	// live roster without any cleanup call at this call site.
	l.Prompt = prompt
	err = c.issueStartRun(ctx, rt, hashToken(token), l, prompt, l.Label.Model, "", spec.Rebind != nil)
	if errors.Is(err, errEndpointUnavailable) {
		// THE REBIND (see OwnerRun.Rebind): the runner is up but could not
		// bind the minted address. Re-mint and re-issue StartRun to the SAME
		// runner, once — issueStartRun did not fail the run for this refusal,
		// so a failed re-mint must.
		rebound, rerr := spec.Rebind(ctx, l)
		if rerr != nil {
			rerr = fmt.Errorf("owner run: rebind the session endpoint: %w", rerr)
			c.failChild(rt, rerr)
			return nil, rerr
		}
		c.audit("endpoint_rebind", rt.harp, map[string]string{"run_id": rt.runID})
		err = c.issueStartRun(ctx, rt, hashToken(token), rebound, prompt, rebound.Label.Model, "", false)
	}
	if err != nil {
		return nil, err
	}

	return &RunOutcome{
		Harp:    l.Identity.Harp,
		RunID:   rt.runID,
		Engine:  l.Label.Label,
		Runtime: runtime,
	}, nil
}

// recordContainerName journals a container-runtime run's resolved container
// name (fragile-volatile: the roster's only handle on a live agent when tmux
// is unavailable — `docker logs -f`/`docker attach`). No-op for "" (a
// host-runtime starter's OwnedRunStarter return); idempotent on the same
// value.
func (c *Coordinator) recordContainerName(runID, name string) {
	if name == "" {
		return
	}
	if err := c.runs.Exec(func() ([]Fact, error) {
		r := c.runsF.run(runID)
		if r == nil || r.ContainerName == name {
			return nil, nil
		}
		return []Fact{factAt(factRunContainer, c.now(), runContainer{RunID: runID, ContainerName: name})}, nil
	}); err != nil {
		c.rep.Warnf("coordinator: record container name: %v", err)
	}
}

// SendOwnedRunTurn enqueues a follow-up user turn for an owner-owned run: the
// same spool write + doorbell a runner's EngineHost already consumes as a new
// turn (delivery-by-state). The run's harp is both sender and recipient (it
// is the session's own run); its runner files no automatic turn report
// (HomeConfig.Depth 0), so this input queue never sees the run's own output
// re-queued into it.
func (c *Coordinator) SendOwnedRunTurn(runID, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("owner run %q: a turn needs text — an empty turn wakes the engine with nothing to "+
			"act on (check context assembly and the prompt source)", runID)
	}
	c.mu.Lock()
	rt := c.attach[runID]
	ownerRun := rt != nil && rt.ownerRun
	c.mu.Unlock()
	if rt == nil {
		return fmt.Errorf("owner run %q: no live run to send a turn to", runID)
	}
	// A DELEGATED child's run id must not be drivable here. This verb enqueues
	// SELF-addressed mail (from == to == the run's harp), which is only correct
	// for a session's own run: for a child it bypasses the parent→child routing,
	// its audit trail and the result bridge, and delivers a turn nobody is
	// recorded as having sent.
	if !ownerRun {
		return fmt.Errorf("owner run %q: not an owner-owned run — send to a delegated child with agent_send, "+
			"which routes and audits it as its parent's message", runID)
	}
	// The write rings the run's doorbell; its runner delivers the turn.
	if _, err := c.queueMail(rt.harp, rt.harp, "message", text); err != nil {
		return fmt.Errorf("owner run %q: enqueue turn: %w", runID, err)
	}
	return nil
}
