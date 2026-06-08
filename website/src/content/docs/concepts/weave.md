---
title: "Weave (ensembles)"
---

**Weave** is ctxloom's map-reduce for agents: fan a task out to several profiles
in parallel — each a self-contained agent carrying its own context and LLM — then
pipe their outputs into one high-power **synthesis** agent that combines them into
a single result.

It is a general primitive, not tied to any one workflow. Code review is its first
consumer (an ensemble of `code-review/<domain>` profiles plus a synthesis
profile), but the same machinery serves research panels, multi-lens analysis, or
any task that benefits from several specialized passes followed by a merge.

## The pieces

Each member is an ordinary [profile](/concepts/profiles/). What makes it an agent
is its assembled context (the specialization) plus its [`llm:`](/concepts/profiles/#preferred-llm-llm)
(its model). Members can run on cheap, specialized models; the synthesizer on a
high-power one.

```
ctxloom weave -p code-review/security -p code-review/performance \
              -s code-review/synthesis  "review this diff"

   ├─ code-review/security      agent · own llm · parallel ┐
   ├─ code-review/performance   agent · own llm · parallel ┤─ labeled parts
   └─ code-review/architecture  agent · own llm · parallel ┘
                                                            │
   synthesize → code-review/synthesis (high-power llm) ─────┴─→ one report
```

## Composable: one engine, three commands

The orchestration runs **in-process** (members are spawned via the client
factory — argv, no shell), so the full map→reduce is portable across platforms.
But the component parts are exposed so you can run them piecemeal or inject
non-ctxloom data:

| Command | Role |
|---------|------|
| [`ctxloom run -p P --print`](/reference/cli/#ctxloom-run) | the atom — one agent. With no prompt it reads the task from **stdin**, so it doubles as a reducer. |
| [`ctxloom map -p A -p B`](/reference/cli/#ctxloom-map) | the **fan-out** — run profiles in parallel, emit a labeled part stream. |
| [`ctxloom weave`](/reference/cli/#ctxloom-weave) | the **composite** — map + synthesis in one portable invocation. |

So `weave` is equivalent to the hand-built pipeline:

```bash
ctxloom map -p A -p B "task" | ctxloom run -p SYNTH --print
```

Use the components directly when you want to inspect the intermediate parts
(`map --save-parts`, `map --no-synthesize`), swap the synthesizer, or pipe member
output through other tools.

## Injecting non-ctxloom outputs

`weave` can synthesize external outputs alongside (or instead of) live members —
useful for folding in another tool's report:

```bash
ctxloom weave -p a -p b -s synth --part legacy=old-report.txt "audit"
ctxloom weave -s synth --parts-from ./collected "merge these findings"
```

## Fault tolerance & cost

A failed member becomes a labeled error part and never aborts the others; if
synthesis fails, the labeled parts are emitted instead so work is never lost.
Concurrency is bounded (default 4). Each weave costs N member runs plus one
synthesis run, so prefer cheap/specialized `llm:` for members and reserve the
high-power model for the synthesizer.
