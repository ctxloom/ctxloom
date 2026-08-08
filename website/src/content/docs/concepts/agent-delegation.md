---
title: "Agent Delegation"
---

You ask a coordinator to fix three unrelated bugs, and it spawns a child to look at each one.
That's the pitch — fan work out, get more done per wall-clock minute. It's also the failure
mode waiting to happen: if the child fixing bug two can quietly reach the MCP server you gave
the child fixing bug three, or the credential the coordinator itself holds, then you haven't
delegated three separate jobs. You've spun up one job with three names and one shared blast
radius. A prompt that goes sideways in any one child now reaches everything all of them can
touch.

ctxloom's answer is that a child's privileges are never inherited, never unioned with a
sibling's, and never assumed — they're resolved fresh from that child's own configured
[agent](/concepts/agents/), the same way they would be if you'd launched it yourself. And
because "trust me, it's scoped correctly" isn't something an operator can act on after the
fact, that grant is written to a durable journal the moment the child is enqueued — not
reconstructed from whatever the config happens to say today. A [real acceptance
journey](/journeys/j002100-delegation/) proves both halves against a live coordinator and its
journal, not just against the spawning code in isolation.

## Spawn, message, stop — the shape of a delegated run

A coordinator reaches its children through four MCP tools, all scoped to the coordinator's own
session:

- **`agent_run`** launches a configured [agent](/concepts/agents/) as a child session and
  returns immediately with its `child_agent_id` (a harp — the address you use for everything
  else) and `child_run_id`. It's an async spawn: the child does its work off in its own session
  while the coordinator moves on, and results come back as mailbox messages, not a blocking
  return value.
- **`agent_recv`** waits (up to a bounded timeout) for those messages. A child parked here
  yields its execution slot rather than burning a turn polling.
- **`agent_send`** delivers a message — coordinator to child by harp, or child to coordinator via
  the reserved `to_role: "parent"` address. Delivery is durable: a message to a child that's
  gone quiet is still there when it resumes, even across a coordinator restart.
- **`agent_stop`** ends a child's run without discarding it — the harp stays resumable, so a
  later `agent_send` relaunches it primed with its recorded history rather than starting cold.

Two more tools carry structured evidence rather than conversation: **`agent_report`** files a
PROGRESS/CHECKPOINT/FINAL update as a durable, journaled fact (not just a chat message that
scrolls away), and **`agent_fetch_artifact`** retrieves the bytes of something a child produced,
content-hash-verified before it's ever written to disk. **`roster`** lists a coordinator's
children — harp, run state, latest report, last activity — the live-status view an operator or
a coordinator itself reads instead of holding it all in the conversation.

Every one of these is part of the **runner-terminated** MCP surface a normal `ctxloom run` /
`ctxloom acp` session gets automatically — richer than, and schema-different from, what a
standalone `ctxloom mcp serve` registration exposes (see the [MCP Server
guide](/guides/mcp-server/)). You don't wire this up; it's there because you're running through
`ctxloom run` at all.

## Why each child gets its own grant, never a union

The MCP servers and permission mode a child runs with come from **that child's own resolved
[agent](/concepts/agents/) definition** — its own `profiles`, its own `permissions` (or
`escalation` ladder) — exactly as if you'd typed `ctxloom run --agent <name>` yourself instead
of a coordinator spawning it. Nothing about being spawned rather than launched directly widens
what a child can reach.

That sounds obvious stated plainly, and it's exactly the kind of claim that's easy to get wrong
in the spawning code without anyone noticing for a while: a coordinator that composed a child's
MCP set by unioning *all* the profiles resolved anywhere in the run — instead of scoping strictly
to that one child's own profiles — would still look correct in the common case (each child
reaching its own tools) and only reveal the bug when a child reaches for a sibling's. The
[delegation journey](/journeys/j002100-delegation/)'s sharpest scenario exists because of exactly
that shape of bug: two children, "reviewer" (a read-only `plan`-mode agent with its own
`docs-lookup` MCP server) and "fixer" (a `bypass`-mode agent with its own `deploy-tool` server),
spawned from the same coordinator. Verified: each child's journaled grant carries *only* its own
server — reviewer never gets `deploy-tool`, fixer never gets `docs-lookup` — and each carries its
own real permission mode, not one hard-coded value reused for both.

## The grant is journaled, not reconstructed after the fact

Config drifts. A project's `.ctxloom/agents/fixer.yaml` you read today is not necessarily what it
said when a run from three days ago was actually spawned — someone may have widened its
profile, changed its permission mode, swapped which MCP servers it carries. An operator auditing
that three-day-old run needs to know what it *was actually granted*, not what the config
currently claims it would get if spawned again right now.

So a child's permission mode and MCP server set are written to the coordinator's run journal
once, **at the moment it's enqueued** — the same discipline already applied to the escalation
ladder, and for the identical reason. The journey proves this isn't just "append-only files
can't be un-appended" by actually editing the config mid-run: it spawns "fixer" once, records
its grant, edits `fixer`'s definition to take on `reviewer`'s profile and permission mode, then
spawns "fixer" again. The **second** spawn's journaled grant genuinely reflects the edit — so the
edit isn't being silently ignored — while the **first** run's journaled grant, re-read after the
edit, hasn't moved at all. A week from now, after the config has changed twice more, that first
run's record still shows exactly what it was actually given.

## What the journal deliberately never carries

Naming a capability and carrying the means to abuse it are different things, and only the first
belongs in a file an auditor reads. Both fixture MCP servers in the delegation journey ship a
command line with a plausible secret-shaped argument — the kind of thing a real server's launch
command sometimes needs, an API token or a deploy credential. The journal records that a server
*named* `docs-lookup` or `deploy-tool` was granted. It does not, and structurally cannot, record
the command, arguments, or environment that launch it — the same boundary the journal already
draws for a session's own bearer credential, which it records as a SHA-256 hash, never the token
itself.

## What this doesn't (yet) prove

Three things a broader claim about delegation safety might tempt you to assert are deliberately
left out of the journey this page draws on, and are worth naming rather than leaving a reader to
assume they're covered: a child's **assembled context contents** (what the child actually
received isn't captured by this harness), **workspace isolation** (whether a child's file writes
actually land in its own worktree rather than the parent's live checkout), and **artifact
publish/fetch tamper-refusal**. All three are real, verifiable claims — they just need a
different harness than the one this page's evidence comes from, and asserting them without that
evidence would repeat a mistake this project has already been caught making once. See [Isolation
axes](/concepts/agents/#the-two-isolation-axes) for the workspace question specifically, and [the
isolation matrix](/security/isolation/) for what's actually been measured about an engine's
*global* state (credentials, caches) crossing a boundary a delegated child doesn't control
either.

## See also

- [Agents & Isolation](/concepts/agents/) — how an agent's engine, profiles, runtime, and
  permission mode are defined; every child's grant traces back to this.
- [The delegation journey](/journeys/j002100-delegation/) — the real Gherkin and captured evidence
  this page is drawn from.
- [MCP Tools Reference](/reference/mcp-tools/) — full parameter schemas for every tool above.
- [MCP Server guide](/guides/mcp-server/) — why the standalone `ctxloom mcp serve` surface is
  smaller than the one described here.
