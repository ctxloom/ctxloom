---
title: "The engine you don't control"
---

You set `runtime: container`, or `workspace: worktree`, and you stop thinking about it. Two
agents run side by side, each with its own worktree, its own config-home environment
variables, its own line in `ctxloom agent list`. You believe they are isolated.

For the state that matters most — the engine's credentials, its conversation history — they
may not be. Nothing about that setup checks.

## An isolation scheme with a counterparty

Host-mode isolation (`runtime: host`, the default) works by setting environment variables —
`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `KIRO_HOME` for a per-agent worktree — and hoping the
engine CLI routes all of its state through them. Point one at a fresh directory and the
engine is supposed to read and write its config, its credentials, and its session history
there instead of the one global location it uses by default.

That "supposed to" is doing the load-bearing work. Setting an environment variable is a
request. Honouring it is a choice made inside a vendor binary you did not write, cannot audit
line by line, and do not control the release cadence of. Isolation that depends on the
cooperation of the thing being isolated is not isolation. It is a negotiation.

## They do not honour it — measured, not theorised

This is not a hypothetical about a badly-behaved model. It is a property of these specific
young, fast-moving CLIs, checked against real, authenticated binaries rather than assumed
from their documentation.

The shape of the failure, seen more than once: an engine CLI honours its documented
home-directory variable for *configuration* — steering files, agent definitions, MCP server
declarations — and silently ignores it for the store that actually matters: the one holding
credentials and conversation history. That store answers to a different variable, or to
none at all, and lands at the same global path regardless of which config-home you pointed
the engine at. Two agents you configured as isolated end up sharing one credential file and
one conversation database, because the piece of state you most wanted kept apart was never
routed through the mechanism you used to separate everything else.

No environment variable was mis-set on ctxloom's side. The vendor simply never wired that
particular store through one. And knowing about *this* store doesn't fix the *next* one — a
future release can add a new cache, a new telemetry log, a new session index, and isolation
degrades again, the same way, silently.

That class of surprise was not the only one found. In a single day of checking real engine
CLIs against their own documentation: a session directory one level off from where the docs
said it would be; a transcript format that bore no resemblance to what was documented; a
credential path in an entirely different location than assumed; a non-interactive mode
persisting to a completely different store than its interactive counterpart; an adapter that
accepted a model-selection flag and silently ignored it. Every one of those is a documented
surface behaving differently from its documentation, on the day it was actually checked.
Treat every vendor CLI and API surface as unreliable until measured — never as reliable
because it's written down.

## What it costs, and why it costs it silently

The failure mode here is not a crash. ctxloom reports the worktree as prepared, the
config-home variables as set, the run as isolated — and it isn't lying. It isolated
everything it has a mechanism to isolate. The gap is in the one store it has no way to
*verify* got isolated, because verifying that would mean auditing the vendor binary's own
I/O — exactly what pointing it at an isolated config-home was supposed to let you not do.

Shared credentials and cross-visible conversation history between agents you believed were
separate is a silent trust failure of the worst kind: it reports success.

## Containers don't ask

A containerized process cannot write outside its filesystem. Not because it chose to route
its state correctly, not because it read an environment variable and did the right thing with
it — because the boundary does not depend on that choice at all. It doesn't matter which
environment variables the engine honours, which stores it invents that nobody documented, or
what the next release adds. The boundary holds regardless of the engine's cooperation.

That is the entire argument for `runtime: container`. It moves isolation from being a
**property of the binary's behaviour** — which you cannot observe or control — to being a
**property of the boundary** — which you built and can verify.

## What each one actually gives you

Read this before you rely on either.

**Worktree isolation (`workspace: worktree`) gives you an isolated filesystem *workspace*** —
a private checkout each agent can write into without trampling another agent's edits. That is
genuinely useful, and it is honestly not more than that. It does not isolate the engine's
*global* state: the engine's credentials, its own session store, its caches all live outside
the workspace, at a path the worktree never touches. Two agents in two worktrees still
typically share one `~/.claude`, one `~/.codex`, one `~/.kiro`, unless the config-home
variables happen to be both set *and* honoured for the specific store you care about — which,
per the above, you cannot assume.

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
