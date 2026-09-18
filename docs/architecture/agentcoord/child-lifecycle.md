# Child lifecycle — spawn, turns, slots, retry, terminal

`children.go` (1832 lines) is the delegated-child lifecycle engine: it turns an
`agent_run` verb into a live child session (resolve → mint harp/run-id/credential →
journal the enqueue → acquire an execution slot → spawn the engine → deliver turns from
the mailbox), drives that child's turn boundaries, bridges each turn's result back to
the parent, and funnels every death through **one exactly-once terminal**. `spawner.go`
is the only place `coord` touches `internal/operations`' launch tail; `launchgate.go`
owns the per-harp retry budget and stop flag; `owner_run.go` is the parent-less
top-level container run.

Two mutually exclusive launch drivers coexist: the **migrated** StartRun path
(`plan.ViaStartRun` — the members of `viaStartRunBackends`) and the **legacy** go-plugin chat
path, which now has no registered backend at all.

```mermaid
flowchart TD
  AR["AgentRun<br/>children.go"] --> RES["Spawner.Resolve → SpawnPlan<br/>spawner.go"]
  RES --> ASSIGN["AssignSession → harp"]
  ASSIGN --> URL["spawnReachURL<br/>children.go"]
  URL --> EQ["enqueueRun — mint run_id + token,<br/>journal factRunEnqueued<br/>children.go"]
  EQ --> RT[("childRt<br/>children.go")]
  EQ --> RC["runChild"]
  RC -->|slot| TS[("Coordinator.slots<br/>semaphore.Weighted + FIFO waiters")]
  RC -->|"ViaStartRun && url != ''"| VSR["runChildViaStartRun"] --> ISR["issueStartRun"]
  RC -->|legacy / degraded| LA["spawner.Launch → attachLaunch"] --> DC["driveChild"]

  RCH[["RunChannel recv loop<br/>runchannel.go"]] --> OTS["onTurnStarted"]
  RCH --> AFT["accumulateFinalText"]
  RCH --> CRF["captureRunFailure"]
  RCH --> OTI["onTurnIdle"]
  DC --> HCE["handleChildEvent"] --> OTB["onTurnBoundary"]
  OTI --> BTR["bridgeTurnResult"]
  OTB --> BTR
  BTR --> MAIL[("queueMail → parent mailbox")]
  BTR --> POR["publishOneshotResult"]
  OTI -->|oneShotReady| TERM
  DC --> EC["endChild"] --> TERM["terminateRun<br/>EXACTLY ONCE"]
  FC["failChild"] --> TERM
  TERM --> REAP["reapEndedRuns"]
  TERM --> RLM["relaunchForLeftoverMail<br/>launchgate.go"] --> NR["nextRelaunch"] --> RSC["resumeChild"]
  RSC --> EQ
```

## Types

| Type | file | Role |
|---|---|---|
| `SpawnPlan` | `spawner.go` | a resolved agent launch: agent name, backend, label, profiles, runtime, context, permission, ladder, MCP servers, `ViaStartRun`, `ResumeMode`, `Degraded`. `Workspace`/`DirtyTreeHandler` are stamped **after** `Resolve`, by `AgentRun` |
| `ResumeMode` | `spawner.go` | persistent vs one-shot engine lifecycle |
| `Spawner` (interface) | `spawner.go` | the launch seam tests fake: `Resolve`, `AssignSession`, `Launch`, `StartEngine`, `ResumeContext`, `MarkSessionEnded` |
| `prodSpawner` | `spawner.go` | the one production implementation; its constructor also installs the executable trust gate on the shared config |
| `EngineSpawn` | `spawner.go` | `StartEngine` result: a spawned-but-not-chatting runner plus HarnessSpec inputs and a `Kill` |
| `childRt` | `children.go` | the non-durable runtime attachment of one live run: identity, `slotHeld`, legacy channels (`in`/`close`/`wake`/`oneshot`), migrated state (`viaStartRun`/`finalMsgs`/`stderrTail`/`runFailure`), turn accumulators, `launchCancel` |
| `RunOutcome` | `children.go` | `agent_run`'s return payload, **fixed at enqueue** |
| `Coordinator.slots` | `coordinator.go` | `semaphore.Weighted` (`golang.org/x/sync`) with FIFO waiters, bounding concurrently executing child turns. `Release` panics on an over-release rather than handing back a token nobody took — a silently inflated cap admits more live engine processes than configured |
| `launchState` | `launchgate.go` | one harp's `{cancel, gen}` cancellation registry + `fails` retry counter + `stopped` flag |
| `OwnerRunSpec` / `OwnedRunStarter` | `owner_run.go` | host-resolved parameters for a parent-less container run, and the seam that lets `coord` spawn a runner without importing `lm/isolation` |

`childRt`'s legacy field set and migrated field set are mutually exclusive by
construction — no run has both — so the type is two runtime attachments wearing one
name.

## Verbs and lifecycle functions

| Function | file | Contract |
|---|---|---|
| `AgentRun` | `children.go` | validates (empty agent name and empty prompt are both refused loudly), resolves, assigns the harp, resolves reach-back, enqueues, dispatches `runChild` on a tracked goroutine, returns immediately |
| `enqueueRun` | `children.go` | mints run id + credential and journals `factRunEnqueued` inside the serialized window; publishes `childRt` |
| `spawnReachURL` | `children.go` | resolves the child-reachable coordinator URL; **fatal unless `--degraded`**, with a remediation hint |
| `childEnv` / `runnerEnv` | `children.go` | the child ENGINE env (harp + project id, deliberately no credential) vs the RUNNER env (reach-back trio + delegation-depth stamp) |
| `runChild` | `children.go` | slot acquire → launch context → migrated or legacy spawn; every failure routes to `failChild` |
| `runChildViaStartRun` / `issueStartRun` | `children.go` | build the `HarnessSpec`, join context+prompt, await dial-home, send `StartRun`, audit, drain queued mail, mark attached |
| `driveChild` / `handleChildEvent` / `onTurnBoundary` | `children.go` | the legacy event loop and its turn boundary — **FROZEN** per the spool-cutover RETIRE-FIRST ruling: never ported to the spool substrate, closed to new backends (`spawner.go`'s `checkLegacyChatFreeze`; frozen residue `legacyChatBackends` = mock alone, plus the degraded no-reach-back spawn) |
| `bridgeTurnResult` | `children.go` | swaps out the turn accumulator and queues the child's answer to the parent as kind `result` |
| `oneShotReady` / `onTurnIdle` | `children.go` | the three-condition one-shot gate, then either a `CauseOneShotBoundary` teardown or idle + slot yield + mail push |
| `terminateRun` | `children.go` | the exactly-once terminal: claim the fact, then slot release, credential revocation, poll+channel sever, parent notice, session-ended stamp, relaunch check, reap |
| `failChild` | `children.go` | warn, count the failure, terminate, mark attached |
| `resumeChild` | `children.go` | backoff, re-check the stop flag, resolve, enqueue as a **fresh run**, relaunch |
| `reapEndedRuns` | `children.go` | bounds live ended-run records by tail + age; re-asserts "not the harp's current run" **inside** the writer window |
| `driveQueued` | `children.go` | classifies how a queued message will reach a recipient, by fold state |
| `Coordinator.StartOwnedRun` / `SendOwnedRunTurn` | `owner_run.go` | mint and drive a parent-less top-level container run; `run_owned.go` is the CLI caller |

## Execution slots and the concurrency ceiling

`delegation.concurrency` (default 4) is a **throughput ceiling, not a correctness gate** —
children run concurrently. `queueFold.executing` is the exact counter the admission
logic reads.

| Function | file | Contract |
|---|---|---|
| `Coordinator.slots.TryAcquire` / `Acquire` / `Release` | `golang.org/x/sync/semaphore` | non-blocking queue-respecting acquire; FIFO blocking acquire with a correct cancel/grant race resolution; hand-off to the oldest waiter; panic on an over-release |
| `claimSlotIntent` / `releaseSlotIntent` | `children.go` | atomically claim the right to acquire, and roll it back |
| `releaseSlot` | `children.go` | clear `slotHeld` and release the semaphore if it was set |
| `onRolePark` / `onRoleUnpark` | `children.go` | yield the slot when a role parks in `agent_recv` or on an approval; re-acquire (**blocking**, bounded only by process shutdown) when it resumes |

`childRt.slotHeld` is a single boolean that means both "holds a slot" and "intends to
acquire one": `claimSlotIntent` sets it, then `onRoleUnpark` blocks in
`slots.acquire(c.baseCtx)` while it already reads true.

Coordinator state here is correctly **partitioned by child identity** — `launches`,
`polls`, `delivered` and `liveness` are harp/role-keyed, so the concurrency flip is
safe at this layer; the races that exist are within one harp's own state machine.

## Launch gate and retry

| Function | file | Contract |
|---|---|---|
| `resolveLaunchTunables` | `launchgate.go` | reads `CTXLOOM_LAUNCH_*` overrides once, at construction |
| `launchContext` | `launchgate.go` | derives and registers one attempt's cancellable context, deregistered by generation |
| `cancelLaunch` | `launchgate.go` | marks the harp stopped **and** cancels the in-flight attempt — the 2026-07-24 incident fix |
| `noteLaunchAttached` / `noteLaunchFailure` | `launchgate.go` | reset the budget on a successful attach; increment on a launch failure |
| `launchBackoff` / `nextRelaunch` | `launchgate.go` | exponential capped backoff; may-we-relaunch decision |
| `relaunchForLeftoverMail` | `launchgate.go` | `terminateRun`'s tail: re-arm a resume when mail is pending, or give up |
| `giveUpLaunching` | `launchgate.go` | warn + mail the parent that the coordinator stopped trying |

The budget bounds **launch failures only**. A child that attaches (resetting `fails` to
0) and then dies without consuming its mail relaunches with `launchBackoff(0) == 0` —
no counter, no delay. Budget exhaustion is announced only for `CauseLaunchFailed`; under
every other cause the coordinator stops re-arming silently, in a file whose stated
purpose is to give up loudly. `c.launches` entries are created on demand and never
deleted, the one per-harp map `reapEndedRuns` does not bound.

## Terminal causes

`terminateRun` branches on the cause; each drives a different subset of the eight
consequences.

| Cause | Meaning |
|---|---|
| `CauseStopped` | explicit `agent_stop`; also clears the harp's session-accept cache |
| `CauseRunnerExit` | the runner process reported `RunExited`, or the watchdog synthesized loss |
| `CauseLaunchFailed` | the launch never attached; the only cause that announces budget exhaustion |
| `CauseOneShotBoundary` | routine per-turn teardown under one-shot driving; skips the terminal-tail drain |

## One-shot driving

Under `ResumeModeOneShot` the engine is torn down at every turn boundary and resumed by
key on the next message, so **a new run_id per turn** under a stable harp.
`resolveResumeMode` (`spawner.go`) gates this on driving × backend capability and
**fails loud rather than downgrading** — the model the rest of the package should copy.

Two consequences a reader must hold:

- Between turns a healthy one-shot child's *current* run is `Ended` with cause
  `CauseOneShotBoundary`.
- Report sequence numbers restart at 1 on each run — see
  [artifacts.md](artifacts.md).

## Divergences

- **`agent_stop` on a one-shot child between turns reports a refusal.** `AgentStop`
  calls `cancelLaunch(harp)` first (`coordinator.go`) — so the stop *has* taken
  effect and no relaunch will occur — and then, because `rec.Ended` is true, returns
  `"child %s had already ended (%s); any pending relaunch is cancelled"`
  (`coordinator.go`). To a coordinating agent that reads as "your stop did
  nothing". Resume assigns a fresh run_id, so the harp's *current* run at the moment of
  the stop is the boundary terminal, not a live run.
- **Plane-2 `agent_stop` does not cancel the launch at all.** `serveStopRun`
  (`runchannel.go`) calls `terminateRun` directly; `cancelLaunch` has exactly two
  call sites repo-wide, its definition and `coordinator.go`. So a coordinator-capable
  *child* stopping a grandchild leaves an armed relaunch running behind a response that
  says "stopped". Two bodies, one verb.
- **`children.go` states the legacy chat path has no production backends**
  ("today: none in production, only test doubles"). `viaStartRunBackends` is
  the registered production backend, and no registered backend implements
  `Chat` at all, so the statement now holds.
- **`ResumeMode`'s doc contradicts the code.** `spawner.go` says one-shot is
  "not yet executed", persistent is "today's only behavior", and one-shot is
  "(v0.8, Slice 4)"; `Resolve` (`spawner.go`) returns `ResumeModeOneShot` for
  the wired backends today.
- **`RunOutcome`'s `Queued` is re-read after the driver goroutine is dispatched**
  (`children.go`), so it can report a stale answer even though the field's
  comment claims the pre-publication `tryAcquire` makes it truthful at return.
- **`agent_run`'s plane-2 disposition asserts a spawn that has not happened.**
  `serveSpawnAgent` (`runchannel.go`) answers `"spawned <harp> (engine X,
  runtime container)"` at enqueue time; every later failure surfaces only as roster or
  mailbox state the caller must go looking for.
- **A run can start with no input and be reported as a complete success.**
  `runChildViaStartRun` computes `first := operations.JoinLeadBlocks(contextText,
  prompt)`, and `issueStartRun` (`children.go`) builds `Input` only
  `if first != ""` — leaving it nil — then audits `start_run`, resets the retry budget,
  marks the child attached and returns nil. Three routes reach `first == ""`: a resume
  whose `Spawner.ResumeContext` (`spawner.go`) warns and returns `""` for an
  unreadable transcript; `StartOwnedRun`, which never validates `prompt`; and
  `takeNextMail`'s journal-error path (see [mailbox.md](mailbox.md)). The empty case is
  legitimate on resume, so the runner cannot distinguish resume from an empty composed
  context.
- **`agent_stop` cannot abort an in-flight `StartRun` round trip**: `issueStartRun`
  derives the request context from `c.baseCtx` (`children.go`) while the dial-home
  wait one block earlier correctly uses the cancellable `ctx`.
- **The launch-settlement subsystem is test-facing.** `awaitChildUp`
  (`children.go`) has zero production callers (its own doc says so), and
  `launchArmed`, `armLaunch`, `markAttached`, `childRt.attached` and `waitAnyClosed`
  exist to serve it.
- **`bridgeTurnResult` clears the accumulator before delivering it**
  (`children.go`): a `queueMail` failure warns and the turn output
  then exists nowhere. A turn that produced nothing warns to `clidiag`, a channel the
  parent — parked in `agent_recv` — structurally cannot read.
