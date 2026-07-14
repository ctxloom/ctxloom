---
title: "The engine you don't control"
---

You set `runtime: container`, or `workspace: worktree`, and you stop thinking about it. Two
agents run side by side, each with its own worktree, its own config-home environment
variables, its own line in `ctxloom agent list`. You believe they are isolated.

Every engine CLI has a "home" environment variable. Every one of them honours it differently,
and **none of them documents where it draws the line between config and state.** We pointed
that variable at a per-agent directory and measured what three real engines actually did. One
**leaked** state out past the boundary. One **flooded** its entire runtime state into the
project tree — into a directory tracked in git. One isolated so perfectly that it **locked the
agent out**, unable to authenticate at all.

Same variable. Three engines. Three different definitions of "home". Nothing about
`ctxloom agent list` looking healthy catches any of it.

## An isolation scheme with a counterparty

Host-mode isolation (`runtime: host`, the default) works by setting each engine's config-home
environment variable to a per-agent directory, and hoping the engine CLI routes all of its
state through it. Point one at a fresh directory and the engine is supposed to read and write
its config, its credentials, and its session history there, instead of in the one global
location it uses by default. (The [environment reference](/reference/environment/) has the
actual variable names, per engine; this page is about their behaviour, which is not what the
names suggest.)

That "supposed to" is doing the load-bearing work. Setting an environment variable is a
request. Honouring it is a choice made inside a vendor binary you did not write, cannot audit
line by line, and do not control the release cadence of. Isolation that depends on the
cooperation of the thing being isolated is not isolation. It is a negotiation.

## Three engines, three failures

This is not a hypothetical about a badly-behaved model. These are specific, young, fast-moving
CLIs, checked against real authenticated binaries rather than assumed from their
documentation. The three failures are not variations on one bug. They are three different
answers to the same question, failing in three different directions.

**One: state escapes.** An engine CLI honours its home-directory variable for *configuration*
— steering files, agent definitions, MCP server declarations — and silently ignores it for the
store that actually matters: the one holding credentials and conversation history. That store
answers to a different variable, or to none at all, and lands at the same global path no
matter which config-home you pointed the engine at. Two agents you configured as isolated end
up sharing one credential file and one conversation database — because the state you most
wanted kept apart was never routed through the mechanism you used to separate everything else.

**Two: state floods in.** Another engine has the opposite problem: it honours its
home-directory variable for *everything*. One variable governs its config surface and its
entire runtime state, with no line between them. ctxloom points that variable at a
project-local directory, because that is the only way to get that engine's prompts isolated
per agent. The engine obeys — and drags its whole runtime state along: session store,
conversation memories, logs, a goals database, roughly 90MB of temp files, all deposited
beside the config we meant to isolate.

That directory sat inside the project's git root, and none of the engine-written state was
ignored. **A routine `git add -A` would have committed an engine's conversation memories and
logs into the repository.** It gets one turn worse: that same directory holds a config file
ctxloom itself generates — and the engine writes its own state *into that generated file*,
appending a per-machine trust record keyed by an absolute local path. The file is
simultaneously ours-to-generate and theirs-to-mutate, and whoever writes last wins. Regenerate
it and you destroy the engine's state; let the engine append and you have dirtied the working
tree with a path that means nothing on any other machine.

**Three: the credentials don't come along, and the agent is silently dead.** A third engine
honours its home-directory variable for everything — *including credentials*. Point it at a
fresh per-agent directory and the isolation is flawless: it writes its entire state tree into
the new location and leaves the global one byte-identical. It is also useless. The agent comes
back "not logged in" and can do nothing at all. Isolation so complete it severed the one thing
the agent needed in order to run.

That third case is worth sitting with. Even when a vendor does exactly the thing you asked
for, you can still lose — because "honour the variable for everything" and "bring the
credentials the agent needs" pull against each other, and nobody told you which side of that
tension they picked.

## The ecosystem converged — on the parts that don't matter

It would be easy, and wrong, to call this ecosystem uniformly chaotic. It isn't. It has
genuinely converged — just not where isolation lives.

**MCP is universal.** Every engine we checked supports MCP servers. But each one reads a
*different file in a different format*: a top-level JSON file; a table folded into a TOML
config; a JSON file under a settings directory; a differently-named JSON file under an agents
directory. The **protocol** converged. The **configuration** did not.

**The agent-markdown convention is nearly universal.** Most engines read a project markdown
file for context — each with its own name for it, in its own location. *Most.* At least one
has no native project context file at all; its only delivery route is a hook that reads a
cache file. Even the convergence has a hole in it, and the hole is invisible until you look
for it.

So this is the actual shape of the thing: **the ecosystem converges on what is cheap and
visible — the protocol, the markdown file — and diverges wildly on precisely what isolation
depends on**: where state lives, which variable governs it, what gets written where, and
whether your credentials come with you. Convergence where it's cheap; chaos where it matters.

There is no principle you can reason from here. Only measurement — per engine, per version,
re-done every time they ship. And knowing today's stores does not protect you from tomorrow's:
a future release can add a cache, a log, a session index, and isolation degrades again,
silently, the same way. **You cannot build an isolation guarantee on top of that by
negotiation.**

## Why it fails silently

None of these three failures is a crash. ctxloom reports the worktree as prepared, the
config-home variables as set, the run as isolated — and it isn't lying about what it controls.
It isolated everything it has a *mechanism* to isolate. The gap is in the state it has no way
to **verify** landed where it should, because verifying that would mean auditing the vendor
binary's own I/O — which is exactly what pointing it at an isolated config-home was supposed
to save you from.

Shared credentials and cross-visible history between agents you believed were separate is a
trust failure that reports success. A project tree quietly filling with another program's
memories and logs is the same failure pointed the other way — nothing alarms until someone
runs `git status`, or stages everything and looks a beat too late.

## Containers don't ask

A containerized process cannot write outside its filesystem. Not because it chose to route its
state correctly. Not because it read an environment variable and did the right thing with it.
Because the boundary does not depend on that choice at all.

Inside a container, the question dissolves. You stop caring where the engine puts its state,
because everywhere it can reach is inside the box. It no longer matters which variables it
honours, which stores it invents that nobody documented, where it draws its private line
between config and state, or what the next release changes. You are no longer trying to
predict which of a vendor's stores respects which variable — **you have stopped asking the
vendor for anything.**

That is the entire argument for `runtime: container`. It moves isolation from a **property of
the binary's behaviour**, which you can neither observe nor control, to a **property of the
boundary**, which you built and can verify.

## What each one actually gives you

Read this before you rely on either.

**Worktree isolation (`workspace: worktree`) gives you an isolated filesystem *workspace*** —
a private checkout each agent can write into without trampling another agent's edits. That is
genuinely useful, and it is honestly not more than that. It does not isolate the engine's
*global* state: the engine's credentials, its own session store, its caches all live outside
the workspace, at a path the worktree never touches. Two agents in two worktrees still
typically share the engine's one global config directory, unless its config-home variable
happens to be both set *and* honoured for the specific store you care about — which, per the
above, you cannot assume.

**Container isolation (`runtime: container`) bounds the filesystem the process can write
to** — a fresh `$HOME`, and only the paths ctxloom deliberately mounts in — plus the
per-image network and mount surface you configured. That is a real, enforced boundary, and it
is also not more than that. It is **not a security sandbox**: ctxloom passes no network
isolation flag by default, and engine credentials still cross the boundary, as scoped
environment passthrough or a read-only credential mount, because the in-container engine has
to authenticate somehow. A container stops a destructive write from reaching your home
directory. It does not stop a compromised prompt from misusing a tool the agent was
legitimately granted — that is a different problem, and it's the one the [trust
gate](/security/trust-states/) and [review flow](/concepts/review-and-trust/) exist to
address, upstream of the engine ever running.

## Mechanism

- The runtime axis (`host` | `container`) and the workspace axis (`none` | `worktree`) are
  independent, chosen at different times, by different owners — see [Agents &
  Isolation](/concepts/agents/#the-two-isolation-axes).
- `ctxloom container build` builds the per-backend agent image; `ctxloom container check`
  diagnoses whether this host can even run one — see the [`ctxloom
  container`](/reference/cli/ctxloom_container/) reference.
- You control the image's base with `ctxloom container scaffold` or `--base-containerfile`, or
  supply a fully external image via `isolation_images` (subject to the identity-remap
  contract) — see [`ctxloom container build`](/reference/cli/ctxloom_container_build/).

Back to: [A prompt is executable code](/security/prompts-are-code/) · [Threat
model](/security/threat-model/)
