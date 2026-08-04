# Engine delivery seam — design

**Status:** proposed, 2026-08-04. Nothing below is built except the read half's
types (`internal/shared/agent/delivery_state.go`), which land first because they
are additive and unblock testing.

Three seams, one root cause. The engine layer receives **pre-rendered payloads**
rather than content, so it cannot own the one decision that is genuinely its
own — *how does this item land in me* — and it cannot answer for what it
delivered.

---

## The root cause

Today a caller composes context, renders per-surface payloads, asks the backend
for a `Delivery` per `SurfaceKind`, and calls `Deliver(dir)`.

That means the CALLER has to know that codex folds MCP into its config surface
while claude has `.mcp.json`; that a skill-only engine renders a command as a
`SKILL.md`; that claude's context is a managed section in `CLAUDE.md` while
another engine appends to a system prompt at launch. Every one of those is a
property of the ENGINE, encoded outside it.

Consequences, all three observed:

1. **Adding an engine means touching callers**, because the caller's rendering
   has to learn the new engine's folding rules.
2. **`manage status` cannot report delivery** — there is no read half, so
   nothing can ask "is what you carry current?" (taskloom `hefty-gallery`, and
   J21's B6 hop, where the boundary table credits an inspector with a hop it
   does not watch).
3. **The mock backend materializes nothing** (`EmptySurfaceSet`), so J20's
   twelve delivery-matrix rows cannot be exercised hermetically at all.

---

## The shape: an engine takes FORMS and knows how to deliver them

`content.Form` is already the right unit, and it is the right unit for a reason
that is not a coincidence: **it is what attestation keys on.** Making it the
deliverable unit too means the thing that was signed and the thing that lands
are the same object, rather than two representations that can drift.

A `Form` carries everything delivery needs:

```go
type Form interface {
    ContentForm() signing.Form                        // raw | distilled — deliver exactly ONE
    Content(ctx) ([]byte, error)                      // the deterministic digest
    Components(ctx) ([]Component, error)              // Path + Mode + Bytes
    Signatures(ctx) (SigSet, error)
    Surface(ctx) (Surface, error)                     // the decoded typed item
}
```

### `Components()` is NOT the delivery view

`Form` is not file-backed — `Component.Path` is a store-relative LOGICAL path,
and the `TreeFS` seam exists to decouple it from any filesystem (`remotetree`
serves bytes at a pinned SHA, `MapTreeFS` synthesises directories from paths, a
companion loadout is a document-backed `Source` over an embedded file map).

But `Components()` is tied to the BUNDLE'S STORAGE LAYOUT, because that is what
it exists for: the digest and `SHA256SUMS` are bundle-relative, so a skill's
components are `skills/reviewer/scripts/run.sh`. An engine wants
`.claude/skills/reviewer/scripts/run.sh` — item-relative `scripts/run.sh` under
its own root. Handing engines `Components()` makes every one of them strip a
`skills/<name>/` prefix, which is storage layout leaking into placement: the
same class of error as leaking `Ref`, one level down.

**The typed `Surface` is the delivery-shaped view; `Components` is the
attestation-shaped one.**

| | paths | serves |
|---|---|---|
| `Components()` | bundle-relative | digest, SHA256SUMS, signatures |
| `Surface()` | item-relative, named, typed | placement |

`content.SkillFile{Path: "scripts/run.sh", Mode, Bytes}` is already exactly what
materializing a package needs, exec bit included — and it is a DECLARED mode,
not a filesystem bit, which is what keeps the digest platform-independent (see
`unfeeling-decimal` for what happens when the declaration and the filesystem
disagree).

So delivery reads `Surface()`. `Form` remains the handle for one reason: it
names WHICH form. A `Fragment` surface carries both `Body` and `Distilled`, so
the surface alone cannot say which to deliver; `ContentForm()` does.

### Proposed interface

**We pass around content. The engine does not care where it came from — it
cares what it IS, and knows how to deliver it.**

That rules out a wrapper struct. An earlier draft here had a `Deliverable{Ref,
Form, Surface}`, and `Ref` is exactly the wrong thing to hand an engine: it is
PROVENANCE (bundle, repo URL, local/builtin), which no placement decision may
depend on. An engine that can see which bundle something came from will
eventually branch on it.

`content.Form` alone is sufficient, and that is the whole interface:

- `Surface(ctx)` — **what it is**, typed and NAMED (`Skill.Name`,
  `Command.Name`, `Hook.Event`+`Name`). The name is the only identity a
  placement needs: `.claude/skills/<name>/`, `.claude/commands/<name>.md`.
- `Components(ctx)` — the bytes and modes to place, already the right shape for
  a multi-file skill package.
- `ContentForm()` — raw or distilled; the caller chose by picking this form.

Provenance is absent by construction rather than by discipline.

```go
// internal/shared/agent

// EngineDelivery is what an engine implements: given content, place it.
//
// Forms arrive ALREADY GATED — an engine never decides admissibility, only
// placement and format.
type EngineDelivery interface {
    // Accepts reports whether this engine has anywhere to put this kind at all.
    // False means undelivered-BY-DESIGN, which a report must not render as
    // "missing".
    Accepts(kind trust.ItemKind) bool

    // Deliver places every form under dir and returns a reversible handle. The
    // engine decides which of ITS surfaces each lands in and in what format —
    // codex folding MCP into config, a skill-only engine rendering a command as
    // SKILL.md. Those decisions live here and nowhere else.
    Deliver(ctx context.Context, dir string, forms []content.Form) (Delivered, error)

    // DeliverContext is separate because context is COMPOSED, not per-item.
    DeliverContext(ctx context.Context, dir string, composed string) (Delivered, error)
}
```

**Why context stays separate.** Ordering, dedupe and variable substitution are
ctxloom's rules and are identical for every engine; only the framing (managed
markers in a native file, or a launch-time system-prompt append) is the engine's.
Handing an engine the ordered fragment forms and asking it to compose would
duplicate those rules per engine — the exact mistake this design exists to undo,
pointed the other way.

---

## Seam 2: the read half (BUILT — types only)

```go
// StateReader is the OPTIONAL read half of a Delivery.
type StateReader interface {
    State(dir string) (DeliveryState, error)
}

// DeliveryState is what ONE route carries, and how it answers for currency.
type DeliveryState interface {
    Route() string                        // "CLAUDE.md", "system prompt (per run)"
    Currency(intended string) Currency
}

type Currency struct {
    Status DeliveryStatus                 // delivered | stale | missing | ephemeral
    Detail string
}
```

Three decisions worth defending:

**The same object that applied answers for it.** This is what makes a status
report truthful *by construction*: there is no second code path that can
disagree with the one that delivered. The bug behind J21's B6 is not a missing
accessor — it is that any separate reporting path is free to be wrong.

**Optional, not nullable.** A route that cannot be observed is structurally
absent from a report, rather than every caller remembering a "not applicable"
case. Same move `content.TrustGated` makes. Reporting an unobservable route as
"missing" is a false alarm, and false alarms train users to ignore the command.

**`ephemeral` is a distinct verdict.** A launch-time system-prompt append does
not persist, so there is nothing to diff and "stale" would be a lie — the next
run carries the current composition regardless of what the last one carried.
Its currency question is "which composition did the last run carry", answered
from the hash that is already in the append file's name.

Implementations: `FileDeliveryState` (managed-marker file; compares the MANAGED
SECTION only, so the user's own content outside the markers never reads as
drift) and `SystemPromptAppender` (per-run; reports the last delivered hash).

### The mock engine gets a read half

The mock currently has `EmptySurfaceSet` — it materializes nothing, which is
precisely why J20's delivery matrix is untestable. It gains a real context
surface (a managed-marker file) implementing BOTH halves.

That is not a test-only convenience. A mock that delivers nothing cannot prove
delivery, so every delivery assertion in the suite is either vacuous or has to
run against a live engine. Giving the mock a real route with a real read half is
what turns twelve `@wip` rows into runnable ones, and it is the hermetic vehicle
J20's own untag condition names.

---

## Seam 3: "Surface" is overloaded

Three meanings, two layers:

| name | layer | means |
|---|---|---|
| `content.Surface` | content | the decoded typed ITEM (Fragment, MCP, Hook…) |
| `agent.SurfaceKind` | delivery | the ENGINE's target (CLAUDE.md, .mcp.json, settings) |
| ~~`SurfaceState`~~ | delivery | *(rejected — would have been a third)* |

They are N:M and cannot be collapsed: a content `Hook` lands in
`SurfaceSettings`; a content `MCP` lands in `SurfaceMCP` or folds into Settings;
a content `Command` lands in `SurfaceCommands` or renders as a `SKILL.md`.

**Done here:** the read half is named `DeliveryState`, not `SurfaceState`, so
this design adds no third meaning.

**Deferred, deliberately:** renaming `content.Surface` or `agent.SurfaceKind` is
a wide mechanical change across both layers and should not land in the middle of
the tree-format migration. Filed rather than done.

---

## Ordering

1. Read-half types (`DeliveryState`, `StateReader`, `FileDeliveryState`,
   `ReadManagedContext`) — additive, breaks nothing. **Built.**
2. Mock engine gets a real context route implementing both halves — unblocks
   hermetic delivery testing.
3. `manage status` / `doctor` walk `StateReader` and report currency. Requires
   splitting compose-from-write in `operations.regenerateContext` first: a
   status command that rewrites the surface it inspects is its own bug.
4. `EngineDelivery` taking `Deliverable` — the wide change. Land per engine
   behind the existing `SurfaceSet` until every engine implements it.

Steps 1–3 are independent of the tree-format migration and can proceed beside
it. Step 4 should wait for it, because a migration verb that pre-renders
payloads would entrench exactly the shape step 4 removes.
