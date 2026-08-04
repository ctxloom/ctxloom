# Engine delivery seam — design

**Status:** proposed, 2026-08-04. Built so far: the read half's types
(`internal/shared/agent/delivery_state.go`), which are additive.

An engine takes CONTENT and knows how to deliver it. It does not care where the
content came from; it cares what it is.

---

## The problem

The engine layer receives PRE-RENDERED PAYLOADS. A caller composes context,
renders per-surface payloads, asks the backend for a `Delivery` per
`SurfaceKind`, and calls `Deliver`.

So the CALLER knows that codex folds MCP into its config surface while claude
has `.mcp.json`; that a skill-only engine renders a command as a `SKILL.md`;
that claude's context is a managed section in `CLAUDE.md` while another engine
appends to a system prompt at launch. Every one of those is a property of the
ENGINE, encoded outside it.

Three observed consequences:

1. **Adding an engine touches callers**, because the caller's rendering has to
   learn the new engine's folding rules.
2. **`manage status` cannot report delivery.** There is no read half, so nothing
   can ask a route "is what you carry current?" (taskloom `hefty-gallery`; J21's
   B6 hop, where the boundary table credits an inspector with a hop it does not
   watch).
3. **The mock backend materializes nothing** (`EmptySurfaceSet`), so J20's
   twelve delivery-matrix rows cannot be exercised hermetically at all.

---

## The interface

```go
// internal/shared/agent

// EngineDelivery is what an engine implements: given content, place it.
//
// Forms arrive ALREADY GATED — an engine decides placement and format, never
// admissibility.
type EngineDelivery interface {
    // Accepts reports whether this engine has anywhere to put this kind at all.
    // False means undelivered-BY-DESIGN, which a report must not render as
    // "missing".
    Accepts(kind trust.ItemKind) bool

    // Deliver places every form under targetDir and returns a reversible
    // handle. The engine decides which of ITS surfaces each lands in and in what
    // format — codex folding MCP into config, a skill-only engine rendering a
    // command as SKILL.md. Those decisions live here and nowhere else.
    //
    // ORDER IS THE SLICE'S ORDER: fragments arrive selected, gated, deduped and
    // priority-sorted.
    Deliver(ctx context.Context, targetDir string, forms []content.Form) (Delivered, error)
}
```

### Why `content.Form` is the unit

It is what attestation already keys on, so the thing that was signed and the
thing that lands are the same object rather than two representations that can
drift.

It also carries no provenance. `trust.Ref` names the bundle, repo and locality —
and no placement decision may depend on any of those. An engine that can see
which bundle something came from will eventually branch on it, so provenance is
absent by construction rather than by discipline.

`Form.Surface()` supplies both halves of what a placement needs: **what it is**
(typed) and **what it is called** (`Skill.Name`, `Command.Name`,
`Hook.Event`+`Name`). `Form.ContentForm()` supplies the third: WHICH form, which
the surface alone cannot say because a `Fragment` carries both `Body` and
`Distilled`.

### `Components()` is not the delivery view

A reader will reach for it, and it is the wrong half.

`Form` is not file-backed — `Component.Path` is a store-relative LOGICAL path,
and the `TreeFS` seam decouples it from any filesystem (`remotetree` serves
bytes at a pinned SHA, `MapTreeFS` synthesises directories from paths, a
companion loadout is a document-backed `Source` over an embedded file map).

But `Components()` is tied to the BUNDLE'S STORAGE LAYOUT, because that is what
it serves: the digest and `SHA256SUMS` are bundle-relative, so a skill's
components are `skills/reviewer/scripts/run.sh`. An engine wants
`.claude/skills/reviewer/scripts/run.sh` — item-relative `scripts/run.sh` under
its own root. Handing engines `Components()` makes every one of them strip a
`skills/<name>/` prefix: storage layout leaking into placement, the same class
of error as leaking `Ref`.

| | paths | serves |
|---|---|---|
| `Components()` | bundle-relative | digest, SHA256SUMS, signatures |
| `Surface()` | item-relative, named, typed | placement |

`content.SkillFile{Path: "scripts/run.sh", Mode, Bytes}` is already exactly what
materializing a package needs, exec bit included — and `Mode` is a DECLARED
mode, not a filesystem bit, which is what keeps the digest platform-independent
(`unfeeling-decimal` records what happens when the declaration and the
filesystem disagree).

### One method, not two

Context is composed rather than per-item, but that does not earn a second
method: a `DeliverContext(composed string)` would be a pre-rendered payload,
which is the thing this seam removes.

Fragments arrive as an ordered slice of forms. Dedupe, priority ordering and
variable substitution stay upstream — they are ctxloom's rules and identical for
every engine. Joining stays a shared helper (`assembleDedupedContext` is already
uniform), so it is a function an engine calls, not a method it implements. The
engine keeps only the engine-shaped decision: which surface, what framing. It
also keeps sight of the individual fragments, which a joined string destroys.

### Three stages: read → process → deliver

Substitution and every other derivation happen in the MIDDLE — after content is
read from whatever format it was stored in, before anything reaches an engine.

```
READ                      PROCESS                        DELIVER
content.Store             gate · dedupe · order          EngineDelivery
tree | remote | archive   substitute variables           claude | codex | kiro …
| builtin | document
        └──── content.Form ────┴──── content.Form ────────┘
```

`content.Form` is the currency at BOTH boundaries, and that is what makes the
engine signature stable: `Form` is an interface, so the middle stage emits
RESOLVED forms — same contract, derived values — and the engine never learns
whether a form was read off disk or produced by substitution. It asks
`Surface()` and gets a typed, named item with its body already final.

This is why substitution belongs neither in the content layer nor in the engine:

- **Not content.** A store's job is to yield exactly the authored, attested
  bytes. A store that substituted would make `Content()` disagree with the
  digest that was signed.
- **Not the engine.** Substitution rules are ctxloom's and identical for every
  engine; putting them at delivery duplicates them per engine, which is the
  problem this seam exists to remove.

### ALL processing lives in the middle

The rule is exhaustive, not illustrative. Every derivation — anything where what
comes out is not byte-identical to what was authored, and anything that decides
what is included — belongs to the process stage:

| processing | today | belongs |
|---|---|---|
| trust gating | `bundles.Loader` via `WithTrustGate` | **process** |
| form selection (raw vs distilled) | `SeededBundleLoader(cfg.ShouldUseDistilled())` | **process** |
| profile fragment collection | `collectProfileFragments` (operations) | process ✓ |
| dedupe | `dedupeFragmentRefs` (operations) | process ✓ |
| priority ordering | `sortFragmentsByPriority` (operations) | process ✓ |
| variable substitution | `substituteVariables` (operations) | process ✓ |
| builtin fragment injection | `ResolveBuiltinBundleFragments` (config) | process ✓ |
| joining / framing | `assembleDedupedContext` | deliver (framing is engine-shaped) |

**Two are on the wrong side today, and both sit in the READ stage — the one that
must stay pure.**

*Form selection.* The loader is constructed with `ShouldUseDistilled()`, so
whether you get raw or distilled is decided while reading. A store should yield
every form it holds and let the process stage pick; deciding at read time means
the read stage carries a policy input, and the same store cannot serve two
consumers who want different forms.

*Trust gating.* `WithTrustGate` wires the gate into the loader, so withholding
happens during read. Gating is a decision about what is INCLUDED, which is
processing by this rule. Leaving it in the loader means every reader is either
gated or not by construction, which is why management and listing loaders have
to be built separately from exposure loaders today.

Moving both is what makes the read stage a pure `Store` — bytes exactly as
authored and attested, no policy — which in turn is what lets verification be
meaningful there.

Naming the stage and giving it one boundary — profiles + stores in, resolved
ordered forms out — is what turns the engine interface from a refactor into a
seam.

**Consequence for attestation, worth stating:** a resolved form's bytes are NOT
the attested bytes. Verification happens on the read side, before processing;
what reaches the engine has already been gated. A caller must never re-verify a
resolved form against the manifest and conclude tampering.

### `targetDir`, never `ProjectDir`

It is the root THE AGENT WILL ACTUALLY READ FROM, which is not always the
project:

| runtime / workspace | targetDir |
|---|---|
| host + none | the project dir |
| workspace: worktree | a detached checkout under the session's ephemeral dir |
| container | the bind-mounted path inside the container |

`ContextWriteRequest.ProjectDir` is misnamed for this reason: under worktree
isolation it is not the project dir, and a field named for one case invites a
caller to reason about the others wrongly. The isolation axes are independent
and compose, so the one thing a delivery must never assume is that its root is
the project. Renaming it, `Delivery.Deliver(dir)` and `DeliverAll(dir, …)` to
`targetDir` is mechanical and rides with this change.

---

## The read half

```go
// StateReader is the OPTIONAL read half of a Delivery.
type StateReader interface {
    State(targetDir string) (DeliveryState, error)
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

Implementations: `FileDeliveryState` (a managed-marker file; compares the
MANAGED SECTION only, so a user's own content outside the markers never reads as
drift) and `SystemPromptAppender` (per-run; reports the hash the last launch
carried, which is already in the append file's name).

Three properties this shape buys:

**The object that applied answers for it.** A separate reporting path is free to
disagree with the one that delivered; walking the same objects makes a status
report truthful by construction. That — not a missing accessor — is the bug
behind J21's B6.

**Optional, not nullable.** A route that cannot be observed is structurally
absent from a report rather than forcing every caller to remember a
"not applicable" case, matching `content.TrustGated`. Rendering an unobservable
route as "missing" is a false alarm, and false alarms train users to ignore the
command.

**`ephemeral` is a distinct verdict.** A launch-time append does not persist, so
there is nothing to diff and "stale" would be a lie: the next run carries the
current composition whatever the last one carried. Reporting it stale sends a
user to fix what is already correct.

### The mock engine implements both halves

The mock has `EmptySurfaceSet` and materializes nothing, which is why J20's
delivery matrix is untestable. It gains a real context route implementing
`EngineDelivery` and `StateReader`.

This is not a test convenience. A mock that delivers nothing cannot prove
delivery, so every delivery assertion either runs against a live engine or is
vacuous — and a vacuous green is indistinguishable from a real one. The mock's
route is the hermetic vehicle J20's own untag condition names.

---

## Naming: "Surface" is overloaded

| name | layer | means |
|---|---|---|
| `content.Surface` | content | the decoded typed ITEM (Fragment, MCP, Hook…) |
| `agent.SurfaceKind` | delivery | the ENGINE's target (CLAUDE.md, .mcp.json, settings) |

They are N:M and cannot be collapsed: a content `Hook` lands in
`SurfaceSettings`; a content `MCP` lands in `SurfaceMCP` or folds into Settings;
a content `Command` lands in `SurfaceCommands` or renders as a `SKILL.md`.

The read half is therefore named `DeliveryState`, adding no third meaning.
Renaming either existing term is a wide mechanical change across both layers and
should not land during the tree-format migration.

---

## Ordering

1. Read-half types — additive. **Built.**
2. Mock engine gains a real context route implementing both halves; unblocks
   hermetic delivery testing.
3. `manage status` / `doctor` walk `StateReader`. Requires splitting
   compose-from-write in `operations.regenerateContext` first: a status command
   that rewrites the surface it inspects is its own bug.
4. Name the PROCESS stage and give it one boundary: profiles + stores in,
   resolved ordered forms out. Today it is spread across `internal/config` and
   `internal/operations`.
5. `EngineDelivery` — the wide change, landed per engine behind the existing
   `SurfaceSet`, once the process stage emits resolved forms.

Steps 1–3 are independent of the tree-format migration. Step 5 waits for it: a
migration verb that pre-renders payloads would entrench the shape step 5
removes.
