# ACP framing — `internal/acp/jsonrpc`

A minimal, hand-rolled, full-duplex JSON-RPC 2.0 peer over **newline-delimited JSON**.
One file, 370 lines, one type that matters (`Conn`). It exists because the pinned ACP Go
SDK's `jsonrpc2` connection hardcodes LSP `Content-Length` framing with no override
hook while ACP frames as NDJSON — so the SDK's *wire types* are reused and only the
*codec* is replaced. It serves both halves of ctxloom's ACP story: the client driver
([`internal/acp`](acp-client.md)) and the agent server (`internal/acpagent`). Its
contract is: frame, multiplex, correlate.

Two things it is not, and both matter: it is **not** a spec-complete JSON-RPC 2.0
implementation (no batch, no `-32700`/`-32600`, no `jsonrpc` version validation,
integer-only response ids), and it is **not** fault-isolating (a handler panic on the
read loop takes down the process).

```mermaid
flowchart TD
  subgraph ext["callers"]
    A["internal/acp<br/>session.go:89 · fsupstream.go:36"]
    B["internal/acpagent<br/>server.go:151 · fsupstream.go:124"]
  end
  A & B --> NC["NewConn :117"]
  NC --> C["Conn :95"]
  C --> ST["Start(ctx) :154<br/>happens-before edge, not a wrapper"]
  ST --> RL["readLoop :237<br/>decode + classify"]
  subgraph out["outbound — any goroutine"]
    CALL["Call :161"] --> GO["Go :173"] --> MP["marshalParams"] --> WF["writeFrame :317 (writeMu)"]
    NOT["Notify :216"] --> MP
  end
  subgraph in["inbound — read-loop goroutine ONLY"]
    RL --> SR["serveRequest :263 → Handler.HandleRequest(reply)"]
    RL --> HN["Handler.HandleNotification"]
    RL --> RR["routeResponse :283"]
    RL --> FP["failPending :300"]
    SR --> MR["marshalResult :346"] --> WF
  end
  GO -->|register id| PEND[("pending map[int64]chan rpcMessage<br/>pendingMu")]
  RR --> PEND
  FP -->|close(ch)| PEND
  RL -.->|defer close| DONE(("done chan"))
  GO -.->|await selects on| DONE
```

## Types

| Type | file:line | Role |
|---|---|---|
| `Error` | `jsonrpc.go:51` | the JSON-RPC error object, doubling as a Go `error`. `Error()` **includes `Data`**, because the reference ACP TypeScript SDK stuffs a thrown handler exception's real cause into `data` behind a generic `"Internal error"` message |
| `rpcMessage` | `jsonrpc.go:75` | the single wire frame covering all four message kinds; the populated fields are the discriminator. `omitempty` on `ID` is **protocol-load-bearing** — it is what makes a notification distinguishable from a request |
| `Handler` | `jsonrpc.go:88` | the inbound seam: `HandleRequest(method, params, reply)` and `HandleNotification`. `reply` must be called exactly once, inline or from another goroutine, so an engine turn need not block the read loop |
| `Conn` | `jsonrpc.go:95` | a bidirectional peer bound to one reader/writer pair; safe for concurrent `Call`/`Notify` |
| `CloserFunc` | `jsonrpc.go:362` | `func() error` → `io.Closer` adapter |

## Functions

| Function | file:line | Contract |
|---|---|---|
| `NewConn` | `jsonrpc.go:117` | allocates the conn, decoder, pending map and done channel. Does **not** start the read loop. Its `ctx` parameter is unused |
| `Conn.Start` | `jsonrpc.go:154` | `go c.readLoop(ctx)` — split from `NewConn` to create a memory-model happens-before edge, documented at `:128-153` with a reproduced `-race` failure |
| `Conn.Call` | `jsonrpc.go:161` | `Go` + `await`; the synchronous 90% API, 6 production call sites |
| `Conn.Go` | `jsonrpc.go:173` | allocates an id, registers a cap-1 pending slot, writes the request, returns an `await` closure; deregisters on write failure. The two-phase split lets a caller order `session/cancel` strictly after `session/prompt` |
| `await` (closure) | `jsonrpc.go:188` | selects response / `ctx.Done()` / `c.done`; unmarshals the result; deletes the pending slot in a defer |
| `Conn.Notify` | `jsonrpc.go:216` | writes a method + params frame with no id |
| `Conn.Close` | `jsonrpc.go:221` | `closeOnce`-guarded call to the supplied closer |
| `Conn.Done` | `jsonrpc.go:232` | the channel a caller joins the read loop on |
| `Conn.readLoop` | `jsonrpc.go:237` | decode forever; classify request / notification / response / garbage; on a decode error store it, fail all pending, return |
| `Conn.serveRequest` | `jsonrpc.go:263` | builds a `sync.Once`-guarded `reply` that echoes the id, then calls the handler **inline** |
| `Conn.routeResponse` | `jsonrpc.go:283` | parses the id as `int64`, looks up the pending slot, sends the frame |
| `Conn.failPending` | `jsonrpc.go:300` | closes and deletes every pending slot so parked callers unblock |
| `Conn.closedErr` | `jsonrpc.go:309` | the stored read error, or `ErrConnClosed` for a clean EOF |
| `Conn.writeFrame` | `jsonrpc.go:317` | stamps `"jsonrpc":"2.0"`, marshals, appends `\n`, writes under `writeMu` |
| `marshalParams` / `marshalResult` | `jsonrpc.go` | marshal outbound params (**errors** rather than sending a stripped frame, `39c3bcad`); marshal a handler result, defaulting nil to JSON `null` and **erroring** on a real marshal failure |

## Contracts

| # | Contract | Where |
|---|---|---|
| J2 | Construct with `NewConn`, then `Start` exactly once — the split is a required happens-before edge, not ceremony | `jsonrpc.go:128-153` |
| J7 | Frames are newline-delimited, one JSON object per line | `jsonrpc.go:317` |
| J15 | `Handler.HandleRequest`'s `reply` must be called exactly once; a second call is swallowed by a `sync.Once` | `jsonrpc.go:266` |
| J8 | Handlers run on the read-loop goroutine, so a slow handler must reply asynchronously | `jsonrpc.go:87` |
| J4 | Exactly one `await` must follow a successful `Go` — it owns the pending slot's cleanup | `jsonrpc.go:172` |
| J21 | Notifications carry no id and are never replied to | `jsonrpc.go:216`, `:249` |
| J17 | `routeResponse` and `failPending` are reachable only from the read loop, so a send-on-closed-channel is impossible — load-bearing and undocumented | `jsonrpc.go:243,252` |
| J18 | Diagnostics go to stderr (`clidiag`), never stdout, so they cannot corrupt a stdio protocol stream | `jsonrpc.go:359` |

## Divergences and real behaviour

- **The package doc claims it "warns and continues on a malformed frame rather than
  tearing the session down"** (`jsonrpc.go:20-21`). It does not: **any** decode error at
  `:241-245` ends the read loop and fails every parked caller — including
  `*json.UnmarshalTypeError`, from which `json.Decoder` provably recovers (a spec-valid
  JSON-RPC batch frame is one such case). Only `*json.SyntaxError` genuinely leaves the
  stream unusable.
- **A panic in any handler kills the whole process.** `serveRequest` calls
  `HandleRequest` inline on the read-loop goroutine with no `recover`; `rg` for
  `recover()` across `internal/acp` and `internal/acpagent` returns zero hits. The
  reference ACP TypeScript SDK converts a thrown handler exception into `-32603` — the
  peer recovers, this side does not. A live trigger exists at
  `internal/acp/session.go:1247` (`sliceLines` with a negative `limit`).
- ~~**`marshalResult` turns a result-marshal failure into a *successful* response with
  `"result":null`**~~ — **RESOLVED `39c3bcad`** (U013-F02). It now returns a
  `CodeInternalError` naming the encoding failure (`jsonrpc.go:376`). The deliberate
  nil→`null` path survives untouched (`:371`) and is pinned by test, because it is
  depended on cross-package — the whole defect was that the failure was
  indistinguishable from it.
- ~~**`mustParams` drops params on a marshal failure and sends the frame anyway**~~ —
  **RESOLVED `39c3bcad`** (U013-F03). Renamed `marshalParams` and it returns an error,
  so a frame whose params cannot be marshalled is **not sent stripped**. `Notify("")` /
  `Go("")` are refused too (U013-F17): they used to emit `{"jsonrpc":"2.0"}`, a frame
  with no method and no id that the peer must drop as garbage.
- **`routeResponse` sends on the cap-1 pending channel outside `pendingMu`**
  (`jsonrpc.go:296`), so a duplicate-id response racing an `await` that exited via
  `ctx.Done()` can block the read loop permanently; `done` then never closes.
- **`"id": null` — the spec's mandated form for an error that cannot be attributed to a
  request — parses to `0` and is dropped as "unknown id 0"** (`jsonrpc.go:283-288`),
  because `json.Unmarshal("null", &int64)` returns nil error and leaves the variable
  untouched. Ids are minted from 1, so `pending[0]` never exists and the peer's parse-error
  report is swallowed while callers hang. String ids are likewise dropped
  (`:285-287`), though inbound *requests* handle them correctly by echoing raw bytes.
- **`Close` is a total no-op when `closer == nil`** (`jsonrpc.go:222-228`) — it does not
  close `done`, unblock the read loop, or fail pending callers — while its doc says it
  "tears down the transport and unblocks any parked reader/caller". `internal/acpagent`
  passes `nil`, and `internal/acp/session.go:96-102` does
  `cancel(); Close(); <-Done()`, which relies on the supplied closer unblocking the
  *reader*.
- **`readLoop` accepts a `ctx` and never selects on it** (`jsonrpc.go:237`): the
  connection's lifetime is governed solely by the reader reaching EOF. `internal/acp`
  joins `Done()` correctly; `internal/acpagent` never joins and never closes, so its
  `Serve` returning via `ctx.Done()` leaves a live read loop dispatching into a torn-down
  server.
- **Errors surfaced to a `Call` caller carry no RPC context** (`jsonrpc.go:207,309-314`)
  — no method name, no id, so a bare "context deadline exceeded" cannot be attributed.
- **The inbound `jsonrpc` version member is never validated** — a `"1.0"` frame
  classifies as a valid request — and `-32700` / `-32600` are neither emitted nor
  exported.
- **`Notify("")` succeeds and emits `{"jsonrpc":"2.0"}`**, a frame this same codec's
  `readLoop` classifies as garbage and drops. All 12 production call sites pass `api.*`
  constants.
- **`closedErr` compares with `*p != io.EOF` rather than `errors.Is`**
  (`jsonrpc.go:310`), so a wrapped EOF would surface as a hard transport failure.
- **`atomic.AddInt64(&c.nextID, 1)` operates on a non-first struct word**
  (`jsonrpc.go:104-110,174`), which needs 8-byte alignment on 32-bit platforms; the
  adjacent `readErr` already uses the modern `atomic.Pointer`.
