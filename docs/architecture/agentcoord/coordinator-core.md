# Coordinator core — journal, folds, identity

`Coordinator` (`coord/coordinator.go:92`) is the session-owning process's whole
delegation runtime: four append-only JSONL journals with their in-memory projections,
the bearer-credential registry, live child attachments, launch/retry bookkeeping,
approval parking, transport listeners and liveness. It owns the durability contract —
**a fact becomes visible only after it is on disk and fsynced** — and the identity
contract — **a bearer token maps to exactly one `(harp, run_id, depth, project)`**.

The type is a god object: ~40 fields across eight disjoint responsibility partitions,
five of them under a single `sync.Mutex`. An approval-ladder walk, a mail push-down, a
launch-gate check and a TUI roster read all serialize on `c.mu`.

```mermaid
flowchart TD
  OPT["Options<br/>coordinator.go:40"] -->|New :251| C["Coordinator<br/>coordinator.go:92"]
  C --> SD["stateDirForProject / claimOwner<br/>statedir.go:31,62"]
  C --> RS[("Store runs.jsonl")]
  C --> MS[("Store mailbox.jsonl")]
  C --> IS[("Store items.jsonl")]
  C --> AS[("Store interactions.jsonl — audit")]
  RS --> RF["runsFold :61<br/>runs · byHarp · creds · project"]
  RS --> QF["queueFold :213<br/>order · executing · state"]
  RS --> RoF["rosterFold :322<br/>entries · current · byRun"]
  RS --> RepF["reportsFold<br/>reports.go:82"]
  MS --> MF["mailFold :426<br/>pending · seen · consumed"]
  IS --> IF["itemsFold<br/>items.go:134"]
  RF --> CR["creds.go<br/>mintToken · hashToken · verifyToken"]
  CR --> ID["Identity<br/>identity.go:76"]
  C --> API(("public verbs<br/>Roster · Identify · AgentSend<br/>AgentRecv · AgentStop · Inject"))
```

## The durability engine

| Symbol | file:line | What it is |
|---|---|---|
| `Fact` | `journal.go:18` | one durable record: `Kind`, `At`, `Data`. `At` exists so folds never call `time.Now()` and replay is deterministic |
| `fold` (interface) | `journal.go:43` | one method, `apply(Fact)`. Six implementations |
| `Store` | `journal.go:58` | one JSONL journal + its folds under single-writer/fsync-first discipline |
| `openStore` / `openStoreFromOffset` | `journal.go:81,95` | open, clamp a distrusted checkpoint offset, replay, start the writer goroutine |
| `Store.replay` | `journal.go:136` | applies every complete line; truncates a **torn tail**, fails loudly on interior corruption. Reads the whole journal with `io.ReadAll` |
| `Store.execLocked` | `journal.go:207` | `decide → append → fsync → apply` under the write lock — this ordering *is* the durability contract |
| `Store.Exec` | `journal.go:248` | serializes `decide` through the writer goroutine; 28 production call sites |
| `Store.View` | `journal.go:261` | quiescent read window; 45 call sites. Contract: do not retain references into fold state past the call |
| `Store.Offset` | `journal.go:124` | write position, for the items checkpoint |

`decide` must not call `Exec` re-entrantly — it deadlocks on the unbuffered request
channel. Nothing states this.

## Fact vocabulary and folds

The 11 fact payload structs live at `facts.go:88-198`; `factAt` (`facts.go:202`) is the
single minting helper (a pass-through to `journal.go:26`'s `newFact`, which **panics**
on a marshal failure — loud, and correct for own-struct payloads).

| Fold | file:line | Projection | Notes |
|---|---|---|---|
| `runsFold` | `folds.go:61` | `runs` (run_id → `RunRecord`), `byHarp` (harp → latest run_id), `creds` (token hash → `Identity`), `project` | run registry and credential store folded from one journal so a terminal fact revokes atomically (`folds.go:130`) |
| `queueFold` | `folds.go:213` | spawn order + the **exact** `executing` counter the concurrency ceiling reads | correctness rests entirely on `transition` (`folds.go:263`) — the most load-bearing 20 lines in the file |
| `rosterFold` | `folds.go:322` | per-harp coordinator-visible state, latest attempt wins | `touch` (`:383`) silently no-ops for a superseded run — that guard is the point |
| `mailFold` | `folds.go:426` | role-addressed durable queues + dedupe set + consume cursor | see [mailbox.md](mailbox.md) |
| `itemsFold` | `items.go:134` | plane-1 counting projection: `counts`, `chars`, `maxSeq` keyed by **run_id** | never stores delta text, only sizes |
| `reportsFold` | `reports.go:82` | latest summary / checkpoint / per-artifact revision / per-harp seq watermark | see [artifacts.md](artifacts.md) |

`runsFold.byHarp` and `rosterFold.current` are the same harp→run_id index maintained
twice from one journal, with two reap policies (`runsFold` never prunes `byHarp`;
`rosterFold` prunes `byRun` only). The duplication buys fold independence.

Every fold arm decodes with `if fact.decode(&p) != nil { return }` — 12 occurrences.
Forward-compatible by design; a *corrupt* payload is indistinguishable from an unknown
one, and neither warns.

**Fold concurrency is sound.** `execLocked` holds the write lock across
decide→append→fsync→apply and `View` takes the read lock, so folds are single-writer
by construction and no fold field needs its own lock, including under concurrent
children.

## Read-model value types

| Type | file:line | Crosses the package boundary as |
|---|---|---|
| `RunRecord` | `folds.go:20` | the run registry's public record — harp, run_id, agent, parent, engine, runtime, permission, MCP names, ladder, ended/cause, resumable |
| `RosterEntry` | `folds.go:312` | one roster row |
| `Message` | `folds.go:405` | one mailbox message (id, from, to, kind, body, structured, in_reply_to) |
| `Identity` | `identity.go:76` | `Harp`, `RunID`, `Depth`, `Project` (persisted) + `Consumer` (`json:"-"`, in-memory only — a journaled consumer bit would outlive the process that minted it) |

`RunRecord` is copied by value at four sites (`cp := *r`) to escape the `View` window;
those copies are **shallow**, so `Ladder` and `MCPServers` share backing arrays. Safe
because both are fixed at enqueue and never mutated.

## Credentials and identity

| Function | file:line | Contract |
|---|---|---|
| `mintToken` | `creds.go:27` | 256-bit token + its persisted SHA-256; fails loudly |
| `hashToken` | `creds.go:37` | the persisted form of a bearer token |
| `verifyToken` | `creds.go:47` | constant-time-per-candidate token → `Identity`; malformed stored hashes skipped |
| `Coordinator.RegisterSessionOwner` | `coordinator.go:576` | mints and journals a **depth-0** credential (the session owner) |
| `Coordinator.Identify` | `coordinator.go:607` | consumer creds first, then constant-time run-registry verify; patches an empty `Project` |
| `Identity.IsChild` | `identity.go:98` | `Depth > 0` — the recursion and authorization predicate, 5 call sites |
| `runsFold.activeRunsForCred` | `folds.go:198` | non-ended runs for a credential, used by runner-loss synthesis |

`identity.go` is the canonical explanation of the reach-back seam (the `Env*` block);
three other packages duplicate the *values* to avoid an import cycle
(`lm/isolation/none.go:9`, `acp/container_transport.go:16`, `mcp/mcp_forward.go:32`).

**Divergences.**
- `RevokeSessionOwner` (`coordinator.go:590`) has **zero call sites anywhere,
  including tests**, so depth-0 owner credentials are never revoked — while
  `doc.go:16-18` asserts "revocation at run end severs the credential's streams and
  parked polls". That holds for *run* credentials (via `factRunEnded`, `folds.go:130`)
  and not for owner credentials; because `factSessionCred` is re-applied on every
  replay, every owner token ever minted for a project stays valid. `runsFold.identityFor`
  (`folds.go:191`) and the `factSessionCredRevoked` fold arm are dead for the same reason.
- `hashToken` — documented as the credential's persisted form — is also used to derive
  the state-directory name (`coordinator.go:282`). Changing the hash for a security
  reason would silently relocate every project's coordinator state.

## Lifecycle

`New` (`coordinator.go:251`) → `Serve` (`httpserver.go:65`) → `Close`
(`coordinator.go:512`), in that order.

| Step | file:line | Behaviour |
|---|---|---|
| defaults | `coordinator.go:257-269` | `<= 0 means default`, four times; `TurnCap: -1` becomes 4 rather than erroring |
| state dir | `coordinator.go:282`, `statedir.go:31` | `~/.ctxloom/coord/<base>-<hash12>`, 0700 |
| owner lock | `statedir.go:62` | exclusive `owner.pid` with stale-owner reclaim; `errStateOwned` drives an **ephemeral** state dir fallback |
| journals | `coordinator.go:344-373` | four `openStore` calls; items may open from a checkpoint offset |
| adopt | `coordinator.go:458` | terminates orphaned host runs, grace-times container runs |
| watchdogs | `coordinator.go:393` | runner heartbeat watchdog + liveness watchdog |
| `goTracked` / `waitTracked` | `coordinator.go:414,438` | `wg.Add` under `mu`, refused after `closing`; join with a 5s bounded escape |
| `Close` | `coordinator.go:512` | closing → cancel → kill attachments → `srv.close` → join → close journals → remove an ephemeral dir |
| `audit` | `coordinator.go:565` | appends one interaction fact; **warns, never gates** (I8) |

`New`'s five post-`WithCancel` failure paths call `closePartial` (`coordinator.go:544`)
but never `c.cancel()`, and on the ephemeral path never remove the temp dir they just
created; `closePartial` discards all four journal `Close()` errors.

## Public verbs on `Coordinator`

| Verb | file:line | Contract |
|---|---|---|
| `Roster` | `coordinator.go:626` | sorted roster snapshot under `View` |
| `Identify` | `coordinator.go:607` | token → `Identity`; the auth root for every transport |
| `AgentSend` | `coordinator.go:637` → `peerSend` `:655` | approval-reply interception → routing policy (I1) → durable queue → delivery-by-state |
| `AgentRecv` | `coordinator.go:720` | audit + `recvMail` long poll |
| `AgentStop` | `coordinator.go:727` | children refused (I2); `cancelLaunch` on **both** paths, then `terminateRun` |
| `Inject` | `coordinator.go:763` | user-typed text as a turn, plus a `KindUserInjected` mirror notice to the target's parent |
| `PublishEvents` | `publish.go:36` | validate + journal an event batch, deduped on `(run_id, seq)` |
| `WatchRuns` / `ListRuns` | `consumer.go:239,253` | see [observation.md](observation.md) |
| `Serve` / `ReachURL` | `httpserver.go:65,160` | see [transport.md](transport.md) |
| `StartOwnedRun` / `SendOwnedRunTurn` | `owner_run.go:70,165` | see [child-lifecycle.md](child-lifecycle.md) |

`Inject` accepts an empty `text` and durably journals an empty-body mail, returning a
success delivery mode; the only emptiness guard is three layers away in the TUI
(`cli/tui/model.go:399`). The sibling verb `AgentSend` is guarded at a different layer
(`mcp/mcp_tools_agents.go:238`). Neither guard is in the verb.

The delivery-by-state classification is written twice over the same `driveQueued`
return, in two vocabularies: `peerSend` (`coordinator.go:704-713`) returns English
prose, `Inject` (`:785-792`) returns the typed `Delivery*` constants.

## State directory

| Symbol | file:line | Notes |
|---|---|---|
| `stateDirForProject` | `statedir.go:31` | `~/.ctxloom/coord/<key>` at 0700 |
| `sanitizeKey` | `statedir.go:44` | replaces `/ \ : ..`; does **not** neutralize a bare `"."`, so a caller-supplied `ProjectKey` of `"."` resolves to `~/.ctxloom/coord` itself |
| `claimOwner` | `statedir.go:62` | writes `owner.pid`; the write and close errors are unchecked, so a zero-byte lock can exist for a live owner and the next claimant reads it as stale |
| `PidAlive` | `pidalive_unix.go:9`, `pidalive_windows.go:11` | two build-tagged one-line wrappers around `internal/shared/pidalive.Alive` |
