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

| bucket | count | IDs |
|---|---|---|
| STILL LIVE | 19 | U020-F05, F06, F08, F09, F10, F13, F14, F15, F16, F17, F18; U025-F03, F04; U049-F13, F14, F16, F18, F24, F25 |
| FIXED | 3 | U020-F07 @ `c2c4195d`; U049-F11 @ `428ce9ae`; U049-F12 @ `00396a53` |
| NEVER HELD | 2 | U049-F17, U049-F19 |
| CANNOT DETERMINE | 0 | — |
| **total** | **24** | |

**Read the "STILL LIVE" count with care.** 19 is not 19 bugs. Only **U020-F05** and
**U020-F09/F10** describe a defect with a user-visible failure and no mitigation. Six rows
(`U020-F17`, `U025-F04`, `U049-F24`, and the two complexity rows `U020-F13`/`U049-F25`)
are STILL LIVE only in the sense that *the code is unchanged* — they have **no failure
path at all**, and by this triage's own standard ("a claim with no failure path is not a
finding") they should be closed as accepted/hardening rather than carried as defects.
Severity is stated per row; the count is not a priority signal.

All three FIXED rows were fixed **incidentally, under a different ID** — none was closed
by work that set out to close it. See "Cross-cutting findings" for what that implies about
the ledger.

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

## U049 — `internal/config`

### U049-F16 — STILL LIVE — every config write destroys comments and reorders keys

`internal/config/config_save.go:145` (`yaml.Unmarshal(existingData, &existing)` into
`map[string]interface{}`) and `:89` (`yaml.Marshal(existing)`).
*Drift: cited `:126` and `:165`.*

The sort was confirmed at the library, not assumed: `gopkg.in/yaml.v3@v3.0.1/encode.go:186-189`
(`encoder.mapv`) does `keys := keyList(in.MapKeys()); sort.Sort(keys)`. A
`map[string]interface{}` carries no comment nodes at all, so comments cannot survive the
round trip.

**Failure path.** A user hand-writes `.ctxloom/config.yaml` with header comments and their
own key order, then runs **any** write command — `ctxloom mcp add`, `ctxloom agent
add/set`, `ctxloom manage statusline on`, anything routed through `Manager.Update`
(`config_manager.go:70` → `saveLocked`). Every comment is destroyed and every key at every
level is re-emitted in sort order. The user ran an unrelated command and got an unrelated
whole-file rewrite, silently and irreversibly without VCS.

**Demonstrated, not inferred.** A standalone probe reproducing exactly that round trip
(`yaml.Unmarshal` into `map[string]interface{}` → `yaml.Marshal`):

```yaml
# BEFORE                                          # AFTER
# ctxloom project config -- hand written…         agents: {}
version: 6                                        llm:
                                                      default: claude-code
llm:                                              profiles:
  # our default backend                               dev:
  default: claude-code                                    parents:
                                                              - base
profiles:                                         version: 6
  # the profile we actually use
  dev:
    parents: [base]

agents: {}
```

All four comments destroyed; key order changed from authored (`version, llm, profiles,
agents`) to alphabetical; inline flow style `[base]` rewritten to block style.

**Severity: MED-HIGH — the highest-impact row outside U020.** Large blast radius (every
write command), fully silent, and it directly contradicts the same file's own
`commitPendingUpgrade` (`config_save.go:53`), which exists specifically to write bytes
verbatim *"so the comments and key order preserved by the node rewrite survive"*. The
codebase already knows how to do this correctly, twenty lines away.

### U049-F14 — STILL LIVE (via a different mechanism than the row states)

`internal/config/config_migrate.go:24-27` (package-global `migrationWarnMu` /
`migrationWarnings`), drained at `config.go:1574` inside `loadConfigLayer` — per-*layer*.
`Load()` takes `ambientMu` only on the no-arg path; `config.go:1101-1105` returns
`loadUncached(opts...)` **before** the lock whenever any option is passed.

**Failure path.** `internal/agentcoord/coord/spawner.go:323` calls
`loadConfig(config.WithAppDir(appPaths[0]))` — `Load` *with* options, so `ambientMu` is
bypassed. That runs under `prodSpawner.resolveCfg` → `Resolve` → `Coordinator.AgentRun`
(`children.go:255`), one goroutine per concurrent child spawn. Two concurrent `agent_run`
calls against a pre-v6 config whose migration hits a lossy branch
(`config_migrate.go:233` or `:599`): goroutine A records the warning, B's drain collects
it, A's drain returns empty. A's `*Config` carries zero warnings, so
`WarnKindMigrationLossy` never reaches A's strict-mode gate and the dropped setting goes
unreported for that load.

**Severity: LOW.** No data race (the mutex holds) and the *multiset* of warnings is
unchanged — only its attribution, and only for a pre-v6 config with a lossy branch. A
genuine fail-loudly hole, narrowly reachable.

**Drift — two cited facts are stale.** *"`Manager.Update` calls `loadUncached` twice per
transaction"* is **false**: U049-F15 cut it to once (`config_manager.go:98`). The drain is
`config.go:1574`, not `:1471`. And the row's stated mechanism ("each child re-loading
config") misdescribes it — children are separate *processes* with their own globals; the
real concurrency is in-process `AgentRun` handlers.

### U049-F18 — STILL LIVE — v3→v4 deletes three user-set keys with no lossy warning

`internal/config/config_migrate.go:336-338` — `upgrade.MapDelete(entry, "trust_workspace")`
/ `"approval_mode"` / `"binary_path"`, with no `recordMigrationWarning`.
*Drift: cited `:322-324`; the sibling call is `:233`, not `:219`.*

The mechanism is wired and used twice in the same file (`:233`, `:599`) and maps to
`WarnKindMigrationLossy` → `strictness.ClassMigration`, fatal in strict mode
(`warnings.go:36,47`).

**Failure path.** A v3-or-older config with
`llm.configs.<label>: {type: gemini, binary_path: /opt/bin/gemini, approval_mode: …, trust_workspace: true}`.
On load, `geminiToAntigravityUpgrade.Apply` flips the type and deletes all three keys. The
rewrite prompt names the *upgrade*, not the losses; strict mode does not abort; nothing
tells the user their configured binary path was discarded.

**Severity: LOW-MED.** Narrow (only `type: gemini` on a pre-v4 config), and `binary_path`
genuinely could not be carried forward — it pointed at a binary the new backend cannot
run. **The defect is the silence, not the deletion.** Cheap to fix.

### U049-F13 — STILL LIVE — exponential parent resolution

`internal/config/config_resolve.go:306` —
`resolveProfileRecursive(profiles, parentName, visited.Clone(), builder, depth+1)`
inside `resolveProfileParents`'s per-parent loop. No memoization. `maxProfileDepth = 64`
at `:270`.

**Drift — hard.** The cited `:337` now lands inside `mergeProfileValues` (`:322-371`) and
has nothing to do with the claim. `resolveProfileRecursive` was split into
`guardProfileResolution` (`:279`) and `resolveProfileParents` (`:304`); it is itself now
only CCN 3. The defect survived the refactor and moved to `:306`.

**Failure path.** `guardProfileResolution` marks `visited` only on the clone, so the
visited set prevents re-entry along one *path*, never across siblings. 30 profiles where
`p<i>` declares `parents: [p<i+1>, p<i+1>]` — a diamond is not even required, the same
parent listed twice suffices, because iteration 2 clones the *pre-mutation* set and never
sees iteration 1's visit. Depth reaches ~30, well under 64, so the guard never fires and
the resolver performs ~2^30 merges. `ctxloom run -p p0` hangs burning CPU with no
diagnostic.

**Severity: LOW.** Requires a deliberately pathological hand-written config; no privilege
boundary is crossed and no ordinary typo produces it. Real, but it is a
hang-your-own-shell bug.

### U049-F24 — STILL LIVE, but not a defect

`internal/config/home.go:17` (no drift — `HomeConfigDir` is still on line 17). One
production caller, `config.go:1399` *(drift: cited `:1319`)*.

Refutation attempted and failed, independently on both sides: `internal/paths` does **not**
import `internal/config`, and `config.AppDirName` is merely an alias of
`paths.AppDirName` (`config.go:38`), so the move is unblocked by any cycle. `internal/paths`
still holds `HomeSessionsDir:170`, `HomeApprovalsPath:399`, `HomeAllowedSignersPath:419`,
`HomeDistrustedSignersPath:444` in the identical shape.

**No failure path — no input produces a wrong outcome.** A file-placement observation.
**Severity: none.**

### U049-F25 — STILL LIVE (measured), gate framing needs rewording

Measured with the exact CI command at `d4c7da2c`. Six functions over gate, not five —
the row missed one, and two figures drifted up:

| function | CCN today | claimed | line today | cited |
|---|---|---|---|---|
| `migrateLLMv3` | **31** | 31 | `:166` | `:152` |
| `Apply` | **19** | 18 | `:316` | `:302` |
| `Apply` | **16** | 15 | `:86` | `:72` |
| `migrateDefaultAgentV6` | **14** | 14 | `:559` | `:545` |
| `migrateProfilesV3` | **13** | 13 | `:256` | `:242` |
| `Apply` | **11** | — *(missed)* | `:397` | — |

**Severity: LOW as a per-function item.** See "Cross-cutting findings" — the gate is
violated at 278 sites repo-wide and never actually runs. These six are in no way
distinguished among them, and fixing `config_migrate.go` alone would not turn it green.

### U049-F11 — FIXED @ `428ce9ae` (+ `442e7aae`)

The three byte-identical builtin loops are gone: `config_bundles.go:777` is now the single
`eachBuiltinBundle(fn)`, and the three former sites (`:214`, `:508`, `:571`) are one-line
callbacks. The row's sharpest evidence — *"the three builtin loops disagree on the
`ListBuiltinBundles` error path, two `return out` and one falls through"* — is resolved
into one canonical `return` at `:781`. The four profile-scope loops now share
`resolveProfileScope` (`:366`) and `resolveProfileOrReport`.

`428ce9ae` — *"fix(U047-F12, U047-F11): one canonical parse for builtin bundles"*, plus
`442e7aae` for the profile-loop extraction. Both in `0f59fbae..HEAD`. Neither names U049-F11.

**Residual (not the row's subject):** the guard *spelling* still varies three ways —
`config_bundles.go:132`, `:330`, `:404`/`:450`. Cosmetic; the duplication is gone.

### U049-F12 — FIXED @ `00396a53`

`config_resolve.go:239-241` now reads:

```go
ExcludeFragments: collections.SortedKeys(b.ExcludeFragments),
ExcludeMCP:       collections.SortedKeys(b.ExcludeMCP),
DenyTools:        collections.SortedKeys(b.DenyTools),
```

— exactly the three fields the row named. `collections.Set.Items()` is still unordered
(`internal/shared/collections/set.go:50-58`); it is simply no longer on this path.

`00396a53` — *"fix(U106-F06): give a resolved Profile's set-backed fields a stable order"*.
Incidental: closed under a different unit's ID. *Drift: `toProfile` is at `:197`, not
`:270-272`.*

**Residual worth a look (nobody's row).** Five sibling fields in the same struct literal
still use unordered `.Items()` — `Tags:222`, `SelectTags:223`, `Bundles:224`,
`BundleItems:225`, `Commands:227`. The fix was applied to the three fields the finding
named, not to the class. Whether any of those five reach a hashed or written artifact was
**not** determined here and should be checked.

### U049-F17 — NEVER HELD

`config_bundles.go:699` and `:745` *(drift: cited `:671`, `:717`)* withhold on a bare
`continue` when `ContentPayload()` errors — but that error branch is unreachable.
`BundleHook.ContentPayload` (`internal/bundles/bundles.go:783`) and `BundleMCP.ContentPayload`
(`:722`) are `json.Marshal` over structs holding only `string`, `[]string`,
`map[string]string` and `bool` — no chan, func, cyclic reference or NaN, none of which
`json.Marshal` can fail on.

The codebase asserts this itself: both `ComputeContentHash` wrappers annotate the error
branch *"Unreachable: the struct holds only strings/[]string/map[string]string, none of
which json.Marshal can fail on."*

So **no user's hook or MCP server can disappear through this path.** The row's premise —
"a user's configured executable silently disappears from the launched engine" — has no
input that produces it. Adding a warn to unreachable defensive code is not a fix.

### U049-F19 — NEVER HELD

Half the row is right and the load-bearing half is wrong.

**Right:** `config_types.go:96` does construct `FragmentRef{Name: ""}` from an empty
scalar. Verified empirically against `yaml.v3` — a bare `- ` yields
`Kind=ScalarNode, Tag="!!null", Value=""`, which takes the scalar branch. (`- ~` and
`- null` are *worse* than the row claimed: they yield `Name: "~"` and `Name: "null"`.)

**Wrong: "with no error".** `resources/schema/input/config-schema.json:160-186` requires
each `fragments` item to be either a string with `minLength: 1` or an object with a
`minLength: 1` name. `validator.ValidateBytes(data)` runs on **every** layer load
(`config.go:1588`, inside `loadConfigLayer`) and appends the classified result to
`cfg.warnings` (`:1589`); `classifyValidationError`'s terminal branch
(`internal/config/unknown_keys.go:124`) unconditionally appends a `WarnKindValidate`,
which is fatal-class in strict mode. The YAML null converts to a JSON null and fails the
`oneOf`.

That constraint landed in `07a365f7` (2026-06-11), **six weeks before** the census base
`0f59fbae` (2026-07-24) — so the claim was wrong when written, not fixed since.

**Found while refuting it — a real gap no row covers.** `internal/profiles/profiles.go:40-47`
carries the byte-identical `UnmarshalYAML` with no emptiness check, and directory profiles
have **no schema file at all**: `resources/schema/input/` holds only `config-schema.json`,
`fragment-schema.json` and `taskloom-config-schema.json`. **That** path is genuinely
silent. If F19's defect is worth fixing anywhere it is there — and no open row cites it.

---

## Cross-cutting findings

### All three FIXED rows were fixed incidentally, under a different ID

None of `U020-F07`, `U049-F11`, `U049-F12` was closed by work aimed at it:

| row | fixed by | that commit's stated subject |
|---|---|---|
| U020-F07 | `c2c4195d` | `fix(U020-F01)` — a slot-state refactor |
| U049-F11 | `428ce9ae` | `fix(U047-F12, U047-F11)` — a different unit |
| U049-F12 | `00396a53` | `fix(U106-F06)` — a different unit |

This is the ledger's blind spot working exactly as its header warns. It also means the
converse is worth auditing across the whole index: rows marked RESOLVED by ID may be no
better verified than these were.

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

### Line-number drift is near-universal — treat every cited line as advisory

Of the 24 rows, **22 cite a `file:line` that no longer matches the claim.** Only
`U049-F24` (`home.go:17`, exact) and `U020-F15` (region shifted, ordering unchanged)
survived. `children.go` alone moved ~280 lines. Two rows drifted so far that the cited
line now describes unrelated code:

- `U049-F13` cited `config_resolve.go:337`, which now sits inside `mergeProfileValues`.
  The defect survived a refactor and moved to `:306`.
- `U020-F13` cited `children.go:1299-1440`; `terminateRun` is now `:1517-1662`.

Every row above was re-located by **behaviour**, not by line number.

### Two rows were overstated in kind, not merely in degree

`U049-F17` (ERRHANDLING over a branch that is structurally unreachable — and which the
codebase itself annotates as unreachable) and `U049-F19` (SILENTNOOP over a path that
emits a strict-mode-fatal schema warning). Both look entirely plausible from the *shape*
of the code and only fall apart when you read the callee or the schema. If the bulk review
was shape-driven, that is the failure mode to expect elsewhere in the index.

### Scope note — one uncited defect found while refuting a row

`internal/profiles/profiles.go:40-47` carries `U049-F19`'s exact claim on the
directory-profile path, where — unlike `config.yaml` — **no schema validator exists**.
That path is genuinely silent. It is not covered by any row in the index. Flagged, not
filed; filing is the human's call.
