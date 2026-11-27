---
tags:
  - domain
  - architecture
---
# ctxloom: architecture

ctxloom is hexagonal architecture around CQRS and event sourcing, built on
**angzarr**: the embedded kernel and its coordinator, the store contract
(`angzarr-store-sqlite`, ONE store per user at
`<ctxloom home>/.ctxloom/ctxloom.db`), `angzarr-proto` wire types and
`angzarr-build` code generation from `proto/`. CLAUDE.md holds the details;
this is the shape every change must keep.

## Where code lives

Every crate is at `crates/<ring>/<role>/<crate>`; the path is the architecture.
- `core/wire`: generated types.
- `core/domain`: **aggregates** (decide, apply), the write side.
- `core/read-model`: **projectors**, the query side.
- `core/ports`: traits the core needs.
- `core/application`: command and query handlers.
- `adapters/driven`: port implementations.
- `adapters/driving`: the CLI, MCP and gRPC, translation only.
- `root`: composition.

## How the parts may talk: the message-flow rules

These are the baselines. Each is enforced by a mechanical check in the gate,
never by review alone.

1. **Aggregates and projectors communicate only through angzarr messages.**
   - An aggregate emits events.
   - Commands and facts enter an aggregate through the coordinator.
   - Nothing else connects them: no shared mutable state, no direct call, no
     table, channel, condition variable or callback passing work from one
     handler to another in memory.
   - An in-memory hand-off is a second, unrecorded event stream. It is lost
     on a crash, invisible to every other process, and it bypasses the
     aggregate's decision.
2. **Projectors never communicate with each other.**
   - A projector does not read another projector's read model, call it, or
     wait on it.
   - A projector that needs some data folds the events that carry it, or that
     data is first made an aggregate's event.
3. **Projectors may feed back into aggregates.**
   - A projector, policy or runner that drives work sends commands and
     records facts, through the coordinator, keyed (a fact by its external
     id, a command by a minted id), so a resend is a DUPLICATE.
   - It never mutates an aggregate's state or another stream's store
     directly.
4. **One writer.**
   - Only the daemon (the host) opens the store or the kernel. Every other
     process is the daemon's client.
   - A CLI command, a hook or a frontend that needs a write sends it. One that
     needs a read asks a projection the daemon serves.
5. **The write side never reads a projection.**
   - An aggregate decides on its own state and on the evidence in the
     command.
   - The caller gathers that evidence before it sends the command.

## Events carry the change; the state is the fold

State is `reduce(apply, events)`. What matters is the state that reduction
produces over the life of the stream, not the content of any one message.
- An event carries what changed and the facts a reader cannot derive. It
  does not repeat the whole state, re-send what an earlier event said, or
  carry what the fold already holds.
- A reader that needs the whole picture folds the stream.
- Large or sensitive payloads are recorded by size and digest, not as text.
  The tool-call input is the precedent: it was two thirds of a store's event
  bytes.

## Enforcement: hard fail

Every rule above is a gate check, and ANY violation fails the gate. There is
no ceiling, no allowance for legacy code, and no review-only rule. If a rule
has no check yet, building that check is the first unit of work on any change
that touches that path.
- **One writer:** clippy `disallowed-methods` on the kernel and store openers,
  allowed only at the daemon's own sites. arch-rules lets only the daemon's
  role depend on the store adapter and the kernel host.
- **No in-memory hand-offs:** ast-grep rules over `core/application` and
  `root` that refuse a shared mutable table, channel or condition variable
  between handlers. A runner's own private state is allowed by name in the
  allow file, with the reason beside it.
- **Dependency direction:** `arch-rules.toml` (`just lint-arch`), as for
  every role.
- **No projector reading another's read model:** a projector's state and
  handler hold no other projector's handle (`Held<..>`, a read-model type
  used as state) and wait on none.
  - The rule is about what happens at runtime, not about crates. Reusing
    another read model's pure fold functions or value types is code reuse,
    and it is allowed: for example `held-projection`, or `session-timeline`
    folding the transcript entries as `session-index` does.
  - A projection read inside a decision is refused, because a domain depends
    on no read model (`arch-rules.toml`).
- **Self-test:** every check is tested on a fixture that holds a violation,
  so a check that passes everything turns red.

There is ONE transitional exception, and it ends at P5 (owner ruling
2026-11-26). The hand-off check's allow file lists the in-memory hand-offs
the messages-only phases still have to remove:
- P3: the Orders and the one-shot and terminal tables;
- P4: the watches' condition variable;
- P5: the pumps' channel.

Each entry names its phase. Every phase ends with its own entries at zero,
and nothing may be added. After P5 the file holds only a runner's own
private state.

Every error carries a stable crate-prefixed code, args and an en-US Fluent
message.
