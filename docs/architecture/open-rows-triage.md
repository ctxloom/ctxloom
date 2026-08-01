# Open-rows triage — are the 24 `open` findings actually live?

**Evaluation, not remediation.** `docs/architecture/findings-index.md` derives `open`
mechanically: *no commit message names this ID*. That is **not** *verified still broken*.
This document answers the question the ledger never asked — **is the defect live in the
code at `d4c7da2c`?** — for all 24 `open` rows.

Verdicts are per-row, adversarial (the working question was "what would have to be true
for this NOT to be a bug?"), and located by **what the code does**, not by the cited line
number. Line drift is flagged per row.

No code was changed. No ledger row was edited. A human applies these.

## Verdict summary

| bucket | count |
|---|---|
| STILL LIVE | 22 |
| FIXED | 1 |
| NEVER HELD | 0 |
| CANNOT DETERMINE | 1 |
| **total** | **24** |

---

## U020 — `internal/agentcoord/coord` (`children.go`, `consumer.go`)

`children.go` grew from ~1,800 to 2,086 lines since the census, so **every** U020 line
citation has drifted. All twelve were re-located by behaviour.

### U020-F05 — STILL LIVE — mailbox message journaled consumed before delivery

`internal/agentcoord/coord/children.go:1282-1294` (`wakeChild`),
`:1316-1327` (`sendTurn`), `:1230-1237` (`onTurnBoundary`);
`internal/agentcoord/coord/mailbox.go:205-232` (`takeNextMail`).
*Drift: cited `1135-1141, 1152-1161, 1088-1090`.*

`takeNextMail` appends `factMailConsumed` (mailbox.go:217-219) and unreserves
(`:229`) **before** returning the message. Once that fact is journaled the fold's
`undeliveredLocked` filters the id out permanently — there is no redelivery, ever, on
this process or the next.

Three post-consume early returns still drop it with no requeue and no warning:

1. `wakeChild:1292-1294` — `if err := c.slots.acquire(c.baseCtx); err != nil { return }`.
   Bare `return`. The message was consumed at `:1282`.
2. `sendTurn:1320-1322` — `if in == nil { return }`.
3. `sendTurn:1323-1326` — `select { case in <- …: case <-c.baseCtx.Done(): }`. The
   `Done` arm falls through and returns; `text` is discarded.

The error-handling *around* this path was hardened since the census (`U023-F03` added
`failChild` on a take failure at `:1231-1234` and `:1283-1288`) but the
**consumed-then-dropped** window is untouched.

**Failure path.** `Coordinator.Close()` (`coordinator.go:542-545`) seals the tracked
group and cancels `baseCtx` **immediately**, with no drain of in-flight mailbox
deliveries. Any `agent_send` whose delivery is between `takeNextMail` and the channel
send at that instant hits `<-c.baseCtx.Done()` (or a failing `slots.acquire`, which only
fails on that same cancellation). The journal says the message was consumed; the engine
never saw it; the parent gets no error, no warning, and no redelivery on restart.

**Severity: HIGH (highest of the 24).** Silent, *durable* message loss on a mailbox whose
contract is at-least-once, triggered by ordinary coordinator shutdown rather than an
exotic race. It is the project's characteristic silent-no-op shape: success reported,
zero payload delivered. `takeNextMail`'s own comment (mailbox.go:196-198) describes the
consume→deliver window as a *crash* window; these are ordinary in-process returns.

### U020-F06 — STILL LIVE — "the parent ALWAYS learns of a child death" is warn-only

`internal/agentcoord/coord/children.go:1646-1648`. *Drift: cited `1424-1426`.*

```go
if _, _, err := c.queueMail(rec.Harp, rec.ParentHarp, kind, body); err != nil {
    clidiag.Warn("ctxloom", "agent %s: queue terminal notice: %v", rec.Harp, err)
}
```

The comment 26 lines above (`:1620-1621`) still asserts the absolute form: *"The
synthesized terminal notice: the parent ALWAYS learns of a child death (blue-paper)."*

**Failure path.** A journal write failure (ENOSPC/EIO on the mail store) at the moment a
child terminates → the notice is never queued, the warning goes to the coordinator's
stderr, and the parent blocks in `agent_recv` until its own timeout with no indication
its child died. The terminal fact is already claimed, so nothing retries.

**Severity: MED.** Low probability (requires a journal write failure) but it silently
voids a documented absolute invariant, and the parent's failure mode is a hang.

### U020-F07 — FIXED @ `c2c4195d`

`internal/agentcoord/coord/children.go:1840-1845` now reads:

```go
// U020-F07: read under c.mu — rt is already published (enqueueRun) at
// this point, so onRolePark/onRoleUnpark/onTurnStarted/claimSlotIntent
// can all touch rt.slot concurrently; matches runChild's own read.
c.mu.Lock()
held := rt.slot == slotHeld
c.mu.Unlock()
```

Commit `c2c4195d` — *"fix(U020-F01): replace childRt.slotHeld bool with a tri-state slot
machine"*. Its body says explicitly: *"Updated every call site sharing the field:
enqueueRun, runChild, resumeChild (**also fixed U020-F07's unguarded lock-free read in
the same edit**), wakeChild, onTurnStarted, onRoleUnpark, onRolePark, releaseSlot,
releaseSlotIntent."*

**Ledger note.** `c2c4195d` **is** in `0f59fbae..HEAD` and its body **does** contain the
literal string `U020-F07` — `git log --grep='U020-F07'` finds it. The census extraction
missed it, presumably by matching subject lines or a `fix(<ID>)`-shaped pattern only.
This row should be repointed to `c2c4195d`. It is worth checking whether other `open`
rows across the ledger were missed the same way (body-only ID mentions).

The same commit also renamed the field: `slotHeld bool` → `slot slotState`
(`slotFree`/`slotClaimed`/`slotHeld`), citing U020-F01. Every U020 row that named
`rt.slotHeld` reads `rt.slot` today.

### U020-F08 — STILL LIVE — comment claims no production backend takes the legacy path

`internal/agentcoord/coord/children.go:596-597`. *Drift: cited `551-554`.*

> `the dial's other reachable case is a StructuredChat backend outside the allowlist
> (today: none in production, only test doubles).`

Refutation attempted and failed. The allowlist
(`internal/agentcoord/coord/spawner.go:189-194`) is exactly
`{claude-code, codex, kiro, acp}`. Two production backends implement `Chat` and are
absent from it:

- `internal/antigravity/chat.go:57` — `func (b *Antigravity) Chat(...)`
- `internal/opencode/chat.go:56` — `func (b *Opencode) Chat(...)`
  (registered: `internal/opencode/backend.go:49,83`)

`children.go:608` gates on `rt.plan.ViaStartRun` and otherwise falls through to the
legacy `spawner.Launch` at `:616`, so both backends genuinely take that path.

`spawner.go:184-186` has since been corrected in part — it now names *"antigravity, mock,
and any future non-ACP StructuredChat backend"* — but it still omits **opencode**, and
`children.go:596-597` is unchanged.

**Failure path.** `driveChild`'s doc (`:1143-1147`) calls the resulting ConsumerService
observability gap *"an accepted, documented gap on an already-degraded path."* For
antigravity and opencode delegated children this is not a degraded corner — it is a
**permanent** live-observability gap for two fully-supported backends. An operator
watching a delegated antigravity/opencode child sees nothing live and is told by the
source that this cannot happen in production.

**Severity: MED.** No crash, but a load-bearing comment that is false, and it conceals a
real capability gap. Needs a product decision (should antigravity/opencode delegation be
live-observable?), not just a comment edit.

### U020-F09 — STILL LIVE — `sendTerminal` eviction is payload-blind

`internal/agentcoord/coord/consumer.go:203-206`. *Drift: cited `188-191`.*

```go
select {
case <-ch: // evict the oldest queued event to make room
default:
}
```

No inspection of what was evicted. The evicted event may be **another run's**
`RunCompleted` — the exact class of event the function exists to protect.

Reachability survives the adversarial check. A `narrow(runID)` re-scoping affordance was
added since the census (U041-F06, `consumer.go:117-131`), and
`internal/cli/run_owned.go:110` does use it. But `internal/cli/acp_children.go:32` still
calls `c.WatchRuns(nil)` and **discards** `narrow` (`_`) — deliberately, per
`consumer.go:115-116` ("a caller that never needs it … which legitimately wants every run
in the project"). So one production subscriber's single ring still carries every run's
terminals.

**Failure path.** A lagging `acp_children` consumer fills its ring. Run A's terminal is
queued but undrained. Run B terminates; `sendTerminal` evicts the head, which is A's
terminal. `operations.adaptConsumerFeed` ends a feed only on `RunCompleted`, so A's
watcher waits forever. Nothing is logged.

**Severity: MED-HIGH.** A hang with no diagnostic, in the one path built specifically to
prevent that hang. Requires a lagging consumer, which is the normal condition under load.

### U020-F10 — STILL LIVE — exhausted terminal retry drops silently

`internal/agentcoord/coord/consumer.go:195-202`. *Drift: cited `180-187`.*

```go
if attempt == terminalEvictAttempts {
    // …acceptable-but-unlikely…: fall through and drop…
    return
}
```

Bare `return`. Nothing emitted. The file header (`:150-152`) acknowledges that losing a
terminal *"hangs a consumer that waits on it forever."*

**Failure path.** Same hang as F09, reached by the retry-exhaustion route instead of the
eviction route. A hung viewer is undiagnosable from the coordinator's logs because there
is no log line at all.

**Severity: MED.** Same consequence as F09, lower probability (four consecutive races),
but this one is a two-line fix (`clidiag.Warn` with the run id) with no design question
attached.

### U020-F13 — STILL LIVE — `terminateRun` is CCN 16 against a CI gate of 10

`internal/agentcoord/coord/children.go:1517-1662`. *Drift: cited `1299-1440`.*

**Measured**, not asserted. `lizard -x "*.pb.go" -x "*/website/*" -C 10` at `d4c7da2c`:

```
NLOC  CCN  token  PARAM  length  location
  61   16    413      1     146  @1517-1662@internal/agentcoord/coord/children.go
```

CCN 16 — exactly the claimed figure. The gate is real and enforcing:
`.github/workflows/ci.yml:208-209` runs `just complexity-check` →
`justfile.container:507-508` → `lizard … -C 10 .`, with no leading `-` and no
per-file exclusions beyond `*.pb.go` and `*/website/*`. Verified that lizard exits `1`
when any warning is emitted.

**But read this row in its systemic context, not as a per-function defect.** A repo-wide
run of the exact CI command reports **278 functions over CCN 10**. The gate cannot be
passing today. Either the CI lint job is red, or it is not running, or its failure is
being ignored. Fixing `terminateRun` alone changes nothing about that.

**Severity: LOW as a bug, HIGH as a signal.** `terminateRun` is long but its eight jobs
are individually correct — F06 above is the only correctness defect found in it. The real
finding is that a declared CI invariant is violated 278 times and nobody is being told.
**Recommend raising the CI gate question as its own item.**

### U020-F14 — STILL LIVE — `rt.oneshot` read outside `c.mu`

`internal/agentcoord/coord/children.go:1193`. *Drift: cited `1055`.*

```go
if ev.Entry.Content != "" && (rt.oneshot || ev.Entry.Type == agent.EntryTypeAssistant) {
    c.mu.Lock()                       // :1194 — the very next line
```

The field is written under `c.mu` by `attachLaunch` (`:1500-1506`). `childRt`'s own
contract (`:101`) says *"Guarded by `Coordinator.mu` except where noted"*; this read is
not noted.

**Failure path.** Safe *today* only because `attachLaunch` and `handleChildEvent` both
run on the driver goroutine (`runChild` → `driveChild`) — an accident of the current call
graph, not an enforced property. It is a data race by the Go memory model and `-race`
would flag it if the call graph ever changed.

**Severity: LOW.** Not currently exploitable; the fix is free (move the read one line
down, inside the lock that is already taken).

### U020-F15 — STILL LIVE — assigned harp leaks into session accounting on early failure

`internal/agentcoord/coord/children.go:262-277`. *Drift: cited `222-232`; the ordering is
unchanged.*

```go
harp, err := c.spawner.AssignSession(c.projectDir, plan.Backend)   // :262
…
url, err := c.spawnReachURL(harp, plan.Runtime)                    // :269
if err != nil { return nil, err }                                  // :270-272  ← leak
rt, token, err := c.enqueueRun(…)                                  // :274
if err != nil { return nil, err }                                  // :275-277  ← leak
```

Neither path calls `MarkSessionEnded`. Confirmed by exhaustive search: the only
`MarkSessionEnded` call in the coord package is `children.go:1650`, inside
`terminateRun` — and no run was ever enqueued on these paths, so no terminal will ever
fire.

**Failure path.** An unresolvable reach-back endpoint (a container child with no bridge
network — the error at `:567` is a real, user-facing condition) or a journal failure in
`enqueueRun`. `operations.AssignSession` → `mgr.AssignHarp` has already written a session
index entry. It never gets an `EndedAt` stamp, so it reads as a live session forever.
Every retry of a persistently-failing spawn mints another one.

**Severity: LOW-MED.** No correctness impact on running agents; it pollutes the session
index monotonically, so `resume`/session listings accumulate phantom live sessions. The
fix the finding proposes (a `defer`red conditional `MarkSessionEnded` disarmed on
`enqueueRun` success) is small and safe.

### U020-F16 — STILL LIVE — `RunOutcome.Queued` is read after the driver is dispatched

`internal/agentcoord/coord/children.go:280` then `:286-288`. *Drift: cited `246-248`.*

```go
c.goTracked(func() { c.runChild(rt, prompt, token, url) })   // :280
…
c.mu.Lock()
queued := rt.slot != slotHeld                                 // :287
c.mu.Unlock()
```

`runChild:577-584` does a **blocking** `c.slots.acquire` and sets `rt.slot = slotHeld` on
success. `RunOutcome`'s own doc (`:208`) says *"agent_run's return payload, **fixed at
enqueue**"*, and `enqueueRun:361-362` says the pre-publication `tryAcquire` is what makes
`queued` *"truthful at return"* — reasoning that does not survive the later re-read.

**Failure path.** `enqueueRun`'s `tryAcquire` fails (cap full) → `slot` stays `slotFree`
→ `runChild` starts and blocks in `acquire` → another run releases a slot inside the
microsecond window before `:287` → `runChild` sets `slotHeld` → `agent_run` returns
`Queued: false` for a run that genuinely waited on the D4 cap.

**Severity: LOW.** Narrow window, and the field is informational (it does not gate any
behaviour). But it is wrong in the one direction that matters — it under-reports
queueing, so an operator diagnosing cap pressure is told there is none.

### U020-F17 — STILL LIVE (marginal) — `_ = c.issueStartRun(...)` discards an error

`internal/agentcoord/coord/children.go:691`. *Drift: cited `646`.*

Unchanged: `_ = c.issueStartRun(ctx, rt, hashToken(token), spec, first, engine.Model, resumeSessionID)`
with no adjacent comment. The sibling call site `owner_run.go:147` still checks it.

The finding conceded the discard is *defensible* — `issueStartRun` routes every failure
through `failChild` itself (verified: `:746`, `:750`, `:756`, `:773`, `:778`). Since the
census, `issueStartRun`'s **own doc** (`:699-700`) gained *"On any failure it routes
through failChild (exactly-once terminal) and returns the error"* — but that landed in
`6d13f608`, which **predates** the census base `0f59fbae`, so it is not a fix; the
reviewer saw it and still wrote the row.

**Severity: NIT.** Reduces to "the discard site should say why". No failure path exists —
by the task's own standard ("a claim with no failure path is not a finding") this is the
weakest of the 24 and could reasonably be closed as WONTFIX rather than fixed.

### U020-F18 — STILL LIVE — `agent_stop` cannot abort an in-flight `StartRun`

`internal/agentcoord/coord/children.go:760`. *Drift: cited `677-678`.*

```go
actx, acancel := context.WithTimeout(ctx, c.runnerAwaitTimeout)          // :741  ← cancellable
…
rctx, rcancel := context.WithTimeout(c.baseCtx, defaultRequestTimeout)   // :760  ← NOT
```

Unchanged since `1397ff0d` (which predates the census). The dial-home wait honours the
cancellable launch context; the `StartRun` round trip does not.

**Failure path.** `agent_stop` during the `StartRun` round trip: the launch context is
cancelled, but `requestRunner` keeps running against `baseCtx` until
`defaultRequestTimeout` elapses. The stop verb returns success while the child's StartRun
is still in flight.

The doc (`:703-705`) has been narrowed since the census to say specifically *"cancelling
it (agent_stop) aborts the dial-home wait instead of holding the harp for the full
`c.runnerAwaitTimeout`"* — closer to the finding's alternative remedy ("state the
exception in the doc"), but it still does not say the round trip is *excluded*, and the
coupling itself is unchanged.

**Severity: LOW.** Bounded by `defaultRequestTimeout`; the run does terminate. The fix
(derive `rctx` from `ctx`) is one token, but it changes stop semantics and deserves a
deliberate call rather than a drive-by edit.

---

## U025 — `internal/agentcoord/discover`

### U025-F03 — STILL LIVE — `os.Stat` inside the sort comparator

`internal/agentcoord/discover/discover.go:99`, with `mtime` at `:127-132`.
*Drift: cited `:62` (comparator) and `:81-86` (`mtime`).*

```go
sort.Slice(matches, func(i, j int) bool { return mtime(matches[i]).After(mtime(matches[j])) })
```

`mtime` does a fresh `os.Stat` on every call, so the comparator is impure and
`sort.Slice` is unstable with no tiebreak. Unchanged since the census; note the file *was*
edited in that window (U025-F02 added the `skipped []error` return, `:90`, `:100-118`) —
the reviewer's neighbouring finding was fixed and this one was not.

**Failure path.** A live coordinator rewrites its `endpoint.json` on `Serve()` while
`List()` is sorting → the comparator's answers become mutually inconsistent → Go's
`sort.Slice` yields an arbitrary order (it does not panic or corrupt; the consequence is
purely ordering). `watchLiveFeed` (`operations/sessionfeed.go:154-162`) then tries
candidates in the wrong recency order. Separately, equal mtimes — routine at 1-second
filesystem granularity, and most likely for coordinators started together, which is the
case the ordering exists to serve — give a non-deterministic order every run.

**Severity: LOW.** Adversarially, this is much weaker than the row's `CORRECTNESS/MED`
label suggests. `n` is the number of *projects with a coordinator state dir* — a handful
— so the "O(n log n) syscalls" amplification is negligible, and the caller
(`watchLiveFeed`) **tries every endpoint in turn until one holds the harp**, so a wrong
order costs a few extra failed dials, never a wrong answer. Worth the six-line fix
(stat once into a slice, add a path tiebreak); not worth prioritising.

### U025-F04 — STILL LIVE (latent only — no failure path today)

`internal/agentcoord/discover/discover.go:68-71`, populated at `:119-122`.
*Drift: cited `38-41`.*

```go
type Endpoint struct {
	URL  string
	Cred string   // :70 — bearer credential, plain exported string
}
```

Confirmed absent: `rg 'func \(e Endpoint\)|func \(e \*Endpoint\)'` returns nothing — there
is still no `String()` and no `LogValue()`.

**No failure path exists today.** Exhaustive search of every consumer: `discover.List()`
has exactly one production caller, `operations/sessionfeed.go:140`, and the only thing
that ever formats an endpoint is `ep.URL` alone (`sessionfeed.go:190`, `:193`, `:197`).
`ep.Cred` is used once (`:196`), passed to `bearerToken`, never printed. The `%v` at
`sessionfeed.go:151` formats `skipped[0]` (an `error`), not an `Endpoint`.

**Severity: NIT (hardening, not a defect).** The finding said so itself — *"nothing leaks
yet"*. By the standard "a claim with no failure path is not a finding", this row is a
design-hardening suggestion on a type whose purpose is to be passed around. Cheap
insurance; not a live bug. Recommend recording it as accepted-hardening rather than
carrying it as an open defect.

---

## Cross-cutting findings

### The ledger's `open` derivation missed exactly one row — by one commit body

`U020-F07` is named in the **body** of `c2c4195d` (in `0f59fbae..HEAD`) and
`git log --grep='U020-F07'` finds it. The census did not.

Scope of the miss, measured: re-running `git log 0f59fbae..HEAD --grep=<ID>` for **all 24**
IDs returns a hit for `U020-F07` only. A parallel search of the source tree
(`rg <ID> --glob '*.go' --glob '*.yml'`) for the other 23 returns nothing — no fix cites
them in a comment either. **So the mechanical derivation was correct for 23 of 24.** The
extraction is sound; it just matched subject lines rather than whole messages.

### The CCN 10 gate has never actually run — it is skipped behind a red lint step

Bears directly on `U020-F13` and `U049-F25`, and is bigger than either.

- The gate is real: `.github/workflows/ci.yml:208-209` → `just complexity-check` →
  `lizard -x "*.pb.go" -x "*/website/*" -C 10 .` (`justfile.container:507-508`), no
  leading `-`, no per-file exclusions. Verified to exit `1` on any warning.
- It is comprehensively violated: a repo-wide run of that exact command at `d4c7da2c`
  reports **278 functions over CCN 10**.
- It never fires. On the most recent `ci.yml` run (`29883211433`, `release/0.7`,
  2026-07-22) the Lint job fails at *"Run golangci-lint"* and the next step, *"Enforce
  cyclomatic complexity (CCN ≤ 10)"*, is **skipped**. Every `ci.yml` run back to at least
  2026-07-19 failed.

Fixing `terminateRun` and the five `config_migrate.go` functions would not change any of
this. **The actionable item is the unreached gate, not the six functions.** Recommend
filing it separately.
