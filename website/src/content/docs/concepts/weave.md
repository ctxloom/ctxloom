---
title: "Weave (ensembles)"
---

:::caution[Experimental]
Experimental — interfaces and behavior may change. This covers both `ctxloom weave` and `ctxloom map`.
:::

One reviewer reads a diff for security and misses the N+1 query. Another reads for performance and misses the auth bypass. Running the same model twice with the same prompt doesn't fix this — it just misses the same things twice.

**Weave** fans a task out to several specialized members in parallel, each a
self-contained agent carrying its own context and LLM, then pipes their outputs
into one high-power **synthesis** agent that merges them into a single verdict.
A security pass, a performance pass, and an architecture pass run at once, and
you read one report instead of three.

It is a general primitive, not tied to any one workflow. Code review is its first
consumer (an ensemble of `code-review/<domain>` profiles plus a synthesis
profile), but the same machinery serves research panels, multi-lens analysis, or
any task that benefits from several specialized passes followed by a merge.

## The pieces

A member is either a named local [agent](/concepts/agents/) (`--agents a,b` —
each on its own engine binding) or a bare [profile](/concepts/profiles/)
(`-p` — sugar for a default-engine agent, running with the profile's own
[`llm:`](/concepts/profiles/#preferred-llm-llm)). Members from both flags run
together. What specializes a member is its assembled context plus its
engine/`llm:` binding; members can run on cheap, specialized models while the
synthesizer runs on a high-power one. `-l`/`--llm` overrides the LLM for every
member (the synthesizer keeps its own), and `--workspace worktree` gives each
member an isolated git worktree.

```
ctxloom weave -p code-review/security -p code-review/performance \
              -s code-review/synthesis  "review this diff"

   ├─ code-review/security      agent · own llm · parallel ┐
   └─ code-review/performance   agent · own llm · parallel ┤─ labeled parts
                                                            │
   synthesize → code-review/synthesis (high-power llm) ─────┴─→ one report
```

The same fan-out with named agents as members:

```
ctxloom weave --agents go-cr-security,go-cr-correctness \
              -s synthesis  "review this change"
```

## Composable: one engine, three commands

The orchestration runs **in-process** (members are spawned via the client
factory — argv, no shell), so the full map→reduce is portable across platforms.
But the component parts are exposed so you can run them piecemeal or inject
non-ctxloom data:

| Command | Role |
|---------|------|
| [`ctxloom run -p P --print`](/reference/cli/ctxloom_run/) | the atom — one agent. With no prompt it reads the task from **stdin**, so it doubles as a reducer. |
| [`ctxloom map -p A -p B`](/reference/cli/ctxloom_map/) | the **fan-out** — run profiles in parallel, emit a labeled part stream. |
| [`ctxloom weave`](/reference/cli/ctxloom_weave/) | the **composite** — map + synthesis in one portable invocation. |

So `weave` is approximately the hand-built pipeline:

```bash
ctxloom map -p A -p B "task" | ctxloom run -p SYNTH --print
```

The difference: `weave` frames the parts for the synthesizer with a generated
preamble (the original task, followed by a "specialist outputs to synthesize"
section and combine instructions) ahead of the labeled part stream. Piping
`map`'s stdout straight into `run` skips that framing — the synthesis profile
sees only the raw parts.

Use the components directly when you want to inspect the intermediate parts
(`map --save-parts`), swap the synthesizer, or pipe member output through other
tools. `map` itself never synthesizes; to fan out with `weave` but skip
synthesis and get the labeled parts only, pass `weave --no-synthesize`.

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
Concurrency is bounded (`--concurrency`, default 4). Each weave costs N member runs plus one
synthesis run, so prefer cheap/specialized `llm:` for members and reserve the
high-power model for the synthesizer.
