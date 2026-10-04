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

A coordinator reaches its children through MCP tools, all scoped to the coordinator's own
session:

- **`agent_run`** launches a configured [agent](/concepts/agents/) as a child session and
  returns immediately with its `child_agent_id` (a harp — the address you use for everything
  else) and `child_run_id`. It's an async spawn: the child does its work off in its own session
  while the coordinator moves on, and results come back as mailbox messages, not a blocking
  return value. A spawn that is refused (an agent that does not resolve, a launch the
  coordinator will not start) fails the tool call with the same `fix:` line the CLI would
  print, so the coordinator is told what to change rather than only that it failed.
- Those messages are **delivered, not fetched**: mail to a session arrives as context at the
  start of its next turn, and its arrival starts that turn when the session is idle. There is
  no receive tool and nothing to wait on.
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

Every one of these is served by the session's own runner, on the MCP endpoint a `ctxloom run`
session gets automatically. There is no standalone ctxloom MCP server to register: the endpoint
exists only while the session runs (see the [MCP Server guide](/guides/mcp-server/)). You don't
wire this up; it's there because you're running through `ctxloom run` at all. A delegated child
that sits at the bottom of the tree gets only the reporting half (`agent_send`,
`agent_report`); the tools that spawn, observe or control other children are withheld from it.

## When a credential hits its rate limit

Children launched by one coordinator from the same credential share that credential's usage
limit, so when one child's turn ends on the limit, the coordinator **holds every run that
shares the credential** instead of letting each child meet the limit on its own next turn.
Runs on a different credential carry on untouched. A hold belongs to the coordinator that
raised it and covers only its own children.

- **Nothing is retried silently.** The child whose turn hit the limit did no work; its parent
  gets an error report leading `RATE LIMITED` that says the prompt was not done and must be
  resent. Mail sent to a held run waits in its mailbox and runs once the hold lifts.
- **The hold releases itself.** It lasts until the reset time the engine reported, kept within
  a floor and a cap (and a fixed default wait when the engine named none), then every held run
  resumes together. A later reset reported by another held child extends the wait; an earlier
  one never shortens it. If the limit is still spent, the first turn to meet it holds the runs
  again.
- **It is visible while it lasts.** `roster` marks each held run with a `hold` — its `kind`
  (`rate_limited`), its `source` (the names of the variables or credential stores the
  credential comes from, never their values) and `until_unix`, when it releases. A held run's
  phase stays `idle`; the `hold` is what tells it apart. The root terminal's bar says how many
  runs are waiting and when they resume, the overlay's agents pane marks each held run and its
  feed title reads `held: rate limited until <time>`, and a held run is never reported as
  stalled.
- **Only the human can cut it short.** Resuming any held run from the overlay releases the whole
  hold early; a coordinator's own resume of a held child is refused, since it would only meet
  the limit again.
- **A held child is not relaunched.** If a held child's process dies while it waits, it is not
  restarted into the spent limit, not by its waiting mail and not by a new message: both wait,
  and the child is relaunched with them when the hold lifts. `roster` still shows it held. Stopping the child (`agent_stop`)
  takes it out of the hold, so a later message relaunches it. While its credential is still
  held, the new run starts paused and joins the hold, as does any new child launched on that
  credential: it comes up but takes no turn, not even its first, until the hold lifts.

To exercise this without spending a real limit, send a turn to an agent on the `mock` engine
whose prompt contains `mock:rate-limited` (or `mock:rate-limited=<unix seconds>` to name the
reset time): that turn ends on a rate limit exactly as a real engine's does.

## When a credential is refused

When an engine refuses a child's credential (an expired or revoked token), no turn on that
credential can succeed until you re-authenticate. The coordinator **holds every run that shares
the credential**, as it does for a rate limit, but nothing releases this hold on its own.

- **Nothing is retried silently.** The child whose turn was refused did no work; its parent gets
  an error report leading `CREDENTIAL REFUSED` that says the prompt was not done and must be
  resent. Mail sent to a held run waits and runs once the hold lifts.
- **It has no deadline.** `roster` marks each held run with a `hold` of `kind`
  `credential_rejected` and no `until_unix`, and the overlay's feed title reads
  `held: credential rejected`.
- **You are told once, loudly.** The coordinator raises one finding, the root terminal's bar
  shows a `CREDENTIAL REFUSED` notice naming the credential's variable or store and how many runs
  are parked, and the terminal bell rings once as the hold opens.
- **The remedy depends on where the credential comes from.** A credential read from a variable
  (such as `CLAUDE_CODE_OAUTH_TOKEN`) is captured when a child launches, so a running session
  never sees a new value: export a fresh one, then restart the session from that shell with
  `ctxloom run --session <harp>` (the notice names the command). A credential read in place from
  a store (a login) reaches the running session: sign in again, then resume any held run from the
  overlay.
- **A refusal outranks a rate limit.** If a refusal arrives while the credential is held on its
  rate limit, the hold becomes a refused-credential hold: its timer is cleared and only you
  release it. A rate limit arriving on a refused credential changes nothing.
- **A coordinator cannot release it.** A coordinating agent's resume of a held child is refused;
  resuming one from the overlay releases the whole hold.

On the `mock` engine, a prompt containing `mock:credential-rejected` ends its turn on a refused
credential.

## When the engine is overloaded

An overloaded engine (claude's 529, "the server is at capacity") is not a spent limit: the
credential is fine, so the runs sharing it carry on. Only the child whose turn was turned away
backs off, after the engine's own retries gave up.

- Its parent gets an error report leading `OVERLOADED` that says the prompt was not done and
  must be resent. Mail sent to the run meanwhile waits and runs once it resumes.
- The run is held on its own for a short, fixed backoff, then resumes by itself. If the engine
  is still overloaded, its next turn backs off again.
- `roster` shows the same `hold`, with `kind` `overloaded`; the root terminal's bar and the
  overlay say the run is overloaded and when it resumes.
- As with a rate limit, only the human can release it early.

On the `mock` engine, a prompt containing `mock:overloaded` ends its turn overloaded.

## Pausing a child

`agent_pause` (or a pause from the overlay) stops a child taking new turns until it is resumed;
the reason given is recorded with the pause. `roster` shows a paused child with a `hold` whose
`kind` is `human` or `agent` (who paused it, with no `until_unix`), and the overlay and bar say
"paused by the human" or "paused by its parent". A pause the human made is the human's to
end: a coordinating agent's `agent_resume` of it is refused. A pause an agent made can be
ended by that agent or by the human. Stopping a paused child ends its pause with it.

## Holds and pauses survive a restart

A hold, and a pause you or a coordinating agent put on a child, is recorded in the
session's coordinator state as it happens. If the coordinator restarts (or you resume the
session with `--session`) while children are held or paused, the holds come back with it:
held children stay held until the same reset time, a hold whose time passed while it was
down lifts as soon as it is back, and a paused child stays paused until someone resumes it.
The `roster`'s `hold` and the root terminal's bar show the hold again, and the human is
told once more which holds are still in force.

What happens to the children themselves depends on where they run:

- A **container** child outlives its session's process for a while (see
  `CTXLOOM_RUNNER_OWNER_LOSS_WINDOW` in the environment reference). A restarted coordinator
  re-adopts it mid-task, still held or paused if it was.
- A **host** child ends with its session's process. Its harp stays held, and it is relaunched
  as a fresh run with the same identity when its hold lifts and mail is waiting for it, or when
  a message is sent to it afterwards.

A refused credential's hold is settled at the restart. The restarted coordinator reads the
credential again from the environment you restarted it from: if it changed, the hold lifts and
the held children resume; if it is the same refused credential, or none is set, the hold stays
and you are told again what to do. The comparison uses a one-way fingerprint of the credential,
never the credential itself. A relaunched host child launches with the new credential. A
re-adopted container child still carries the credential it was launched with, so its next turn
is refused again: stop it (`agent_stop`) and message it to relaunch it on the new one.

## Every session is its own tree

Each `ctxloom run` hosts a coordinator of its own. Run a second session in a
project that already has one open, in another terminal, and it isn't refused:
it starts its own coordinator beside the first. The two are independent
trees. Each has its own children, roster, inbox, shutdown drain and lifetime,
and they share no state, so one failing never affects the other. The
concurrency cap on children applies to each tree separately. The same holds
for the short-lived coordinator that `bundle distill`, `session distill` or
`init`'s probe stands up: it runs beside any open session.

A tree's state lives in a root directory named after the session that
started it (`~/.ctxloom/coord/<project>/<session>`). Its owner holds an
exclusive kernel file lock (`owner.lock`) for as long as that process lives,
so ownership ends exactly when the owner ends, however it ends. Beside the
lock, `owner.json` records who holds it (pid, session, mode, start time). That
record is for display and for spotting an abandoned owner, never for deciding
whether the owner is alive.

A session's root stays after the session exits, however it exits.
`ctxloom run --session <harp>` resumes that session and adopts its root. Its
children come with it, including the ones that already finished, so you can
still message them and fetch what they produced. If another live process
still holds the root, the resume runs without agent delegation and says so.
The one exception is an interactive owner whose terminal is gone: it is
provably abandoned, so the resume ends it and takes the tree over. A root is
removed along with its session by `ctxloom session sweep`. The short-lived
coordinator behind `bundle distill`, `session distill` or `init`'s probe is
the exception: nothing resumes it, so its root is removed as soon as it
finishes. `ctxloom doctor` lists every tree in the project and
who owns it (`DOCTOR-CHECK-PROJECT-OWNER-v4`). It removes none of them.

## Why each child gets its own grant, never a union

The MCP servers and permission mode a child runs with come from **that child's own resolved
[agent](/concepts/agents/) definition** — its own `profiles`, its own `permissions` — exactly as if you'd typed `ctxloom run --agent <name>` yourself instead
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

Config drifts. The `fixer` binding you read in `.ctxloom/config.yaml` today is not necessarily what it
said when a run from three days ago was actually spawned — someone may have widened its
profile, changed its permission mode, swapped which MCP servers it carries. An operator auditing
that three-day-old run needs to know what it *was actually granted*, not what the config
currently claims it would get if spawned again right now.

So a child's permission mode and MCP server set are written to the coordinator's run journal
once, **at the moment it's enqueued**. The journey proves this isn't just "append-only files
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
axes](/concepts/agents/#the-two-isolation-axes) for the workspace question specifically, and [Choosing an
isolation boundary](/security/environment-isolation/) for an engine's
*global* state (credentials, caches) crossing a boundary a delegated child doesn't control
either.

## See also

- [Agents & Isolation](/concepts/agents/) — how an agent's engine, profiles, runtime, and
  permission mode are defined; every child's grant traces back to this.
- [The delegation journey](/journeys/j002100-delegation/) — the real Gherkin and captured evidence
  this page is drawn from.
- [MCP Tools Reference](/reference/mcp-tools/) — full parameter schemas for every tool above.
- [MCP Server guide](/guides/mcp-server/): how the session endpoint reaches the engine.
