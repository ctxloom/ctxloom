---
title: "The engine you don't control"
# EDITORS — read before changing this page. (Kept in frontmatter, not an HTML comment:
# frontmatter is stripped at build, so none of this ships in the published page source.)
#
# 2026-07-22: the anonymisation rule that used to live in this block was REVERSED, on purpose,
# by the maintainer (Ben). It is recorded here, not silently deleted, so a future editor finds
# the reasoning instead of a mystery.
#
# The OLD rule: name no engine, no engine-specific environment variable, no engine dotfile
# path, on the theory that tying any of it to a named vendor turns documentation into a
# disclosure.
#
# WHY IT CHANGED: every failure this page describes is an INTEGRATION LIMITATION, not a
# vulnerability. Each engine here is a single-user CLI being driven in a multi-agent way none
# of them was designed for. Storing credentials in an OS keyring is *correct* design — that is
# exactly where secrets belong. A single global credential store is *normal* for a one-human
# tool. Nothing measured on this page is a weakness any vendor should be embarrassed by, and
# the page must not write as though it is. What's missing, case by case, is a lever for
# relocating PER-SESSION state — a reasonable feature request, not an accusation. Prior notice
# to the vendors named below was considered and explicitly declined: nothing here is a secret
# about their software, and nothing here is exploitable by a third party — it is a
# caller-integration gap, visible only from where ctxloom sits.
#
# THE NEW RULE, replacing the old one: name the engine, the real environment variable, the
# real path. Every named, measured claim carries the engine's VERSION and the DATE it was
# measured — an unpinned claim about someone else's software is unfair to them and ages into a
# falsehood the moment they ship. If the exact version isn't known, say so; never invent one.
# See the point-in-time note further down this page, and the executable matrix (built
# elsewhere in this repo) that re-checks these claims against live binaries on a schedule.
#
# If a future editor wants the anonymised version back: the reasoning that justified removing
# it is right here, not deleted. Make the case against THIS reasoning, don't just revert past it.
---

You set `runtime: container`, or `workspace: worktree`, and you stop thinking about it. Two
agents run side by side, each with its own worktree, its own config-home environment
variables, its own line in `ctxloom agent list`. You believe they are isolated.

Host-mode isolation asks each engine CLI, politely, via an environment variable, to keep its
state somewhere private. **Not one of these engines documents where it draws the line between
config and state.** So we pointed that variable at a per-agent directory and measured what
four real, currently-shipping CLIs actually did. **Kiro** **leaked** credential state out past
the boundary. **Codex** **flooded** its entire runtime state into the project tree — into a
directory tracked in git. **Claude Code**, pointed at an unseeded config-home, isolated so
perfectly that it **locked the agent out**, unable to authenticate at all — a gap that turned
out to be ctxloom's to close, and is now closed. And **Antigravity**'s lever relocates its
configuration and session state cleanly — but not its authentication, which turns out to live
one layer beneath anything a process environment variable can reach.

Four engines. Four different answers to "where does your state live?" — three about a variable
honoured, ignored, or pulling in two directions, and one about a layer no per-process variable
was ever going to touch. A fifth engine, **opencode**, needed none of this hand-wringing at
all: its lever was complete the whole time, and the only bug was ctxloom's own (see below).
Nothing about `ctxloom agent list` looking healthy catches any of it.

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

## Four engines, four failures

This is not a hypothetical about a badly-behaved model. These are specific, young, fast-moving
CLIs, checked against real authenticated binaries — named and versioned below — rather than
assumed from their documentation. The four failures are not variations on one bug. They are
four different answers to the same question, and they escalate.

**One: state escapes — Kiro.** Kiro honours `KIRO_HOME` for *configuration and session
history* — steering files, agent definitions, MCP server declarations, the session-transcript
store — and ignores it for the one store that actually matters: subscription credentials, which
live in a single sqlite database under `$XDG_DATA_HOME`, unconditionally, no matter which
`KIRO_HOME` you pointed Kiro at. Two agents you configured as isolated end up authenticating
through that same global database — because the state you most wanted kept apart was never
routed through the mechanism you used to separate everything else. Measured against kiro-cli
2.12.1, 2026-07-15.

This one has a real, vendor-supported way around it today: authenticate with `KIRO_API_KEY`
instead of the interactive subscription login, and credentials ride the environment rather
than the shared database, so per-agent isolation is genuine. ctxloom now isolates
`XDG_DATA_HOME` whenever `KIRO_API_KEY` is set, and raises a loud, non-fatal finding — never a
silent share — when it isn't (verified 2026-07-15).

**Two: state floods in — Codex.** Codex has the opposite problem: `CODEX_HOME` *is* the
`.codex` directory itself, and it governs Codex's config surface and its entire runtime state,
with no line between them — a reasonable single-home design for one human at a terminal, and a
flooding hazard the moment a caller tries to isolate just the config surface. ctxloom points
`CODEX_HOME` at a per-agent directory inside the worktree, because that is the only way to get
Codex's prompts isolated per agent. Codex obeys — and drags its whole runtime state along:
session store, conversation memories, logs, a goals database, roughly 90MB of temp files, all
deposited beside the config we meant to isolate. Measured against codex-cli 0.144.4,
2026-07-15.

That directory sat inside the project's git root, and none of the engine-written state was
ignored. **A routine `git add -A` would have committed Codex's conversation memories and logs
into the repository.** It gets one turn worse: that same directory holds `config.toml`, a file
ctxloom itself generates — and Codex writes its own state *into that generated file*, appending
a per-project `[projects."<abs-path>"] trust_level` entry keyed by an absolute local path. The
file is simultaneously ours-to-generate and Codex's-to-mutate, and whoever writes last wins.
Regenerate it and you destroy Codex's trust record; let Codex append and you have dirtied the
working tree with a path that means nothing on any other machine.

ctxloom now auto-generates a `.gitignore` entry for the whole `.codex/` tree in every project it
manages, which closes the *accidental-commit* risk. That is a mitigation on ctxloom's side, not
a fix for the underlying gap: Codex still has no configuration-only variable separate from the
full-state one, and closing that is Codex's to do.

**Three: the credentials don't come along, unless you bring them — Claude Code.**
`CLAUDE_CONFIG_DIR` honours the variable for everything, *including credentials*. Point it at a
fresh per-agent directory with nothing pre-seeded in it, and the isolation is flawless: Claude
Code writes its entire state tree into the new location and leaves the global one
byte-identical. It is also useless — the agent comes back "not logged in" and can do nothing at
all. Isolation so complete it severed the one thing the agent needed in order to run. Measured
against claude 2.1.210, 2026-07-15.

That is not a Claude Code bug. `CLAUDE_CONFIG_DIR` does exactly what its name says: it
relocates config *and* credentials, together, cleanly — the fullest lever of the four. The gap
was ours: ctxloom had to seed `.credentials.json` into the new location itself before Claude
Code had anything to authenticate with, and it now does that on every host and worktree run.
(An earlier version of that seeding copied the *whole* `~/.claude.json` in, for onboarding
convenience — which meant every isolated agent could also read the host user's own MCP server
registrations and whatever tokens they carried. That was a confidentiality bug in ctxloom, not
in Claude Code, and it was fixed 2026-07-21 by seeding only the credentials file.)

That third case is still worth sitting with, even resolved: a vendor can do exactly the thing
you asked for and you can still lose, because "honour the variable for everything" and "bring
the credentials the agent needs on day one" are two different jobs — and closing the gap
between them was ctxloom's to do, not Claude Code's to anticipate.

**Four: the state that moves isn't the state that matters — Antigravity.** Antigravity (CLI
binary `agy`) has no config-home environment variable of its own — no `ANTIGRAVITY_HOME`, no
equivalent. `HOME` is the only lever it has, and it governs everything: point it at a fresh
directory and Antigravity's entire configuration and session-state tree relocates there,
cleanly — a full `.gemini/` tree materializes at the new location, the same kind of result any
other engine's honoured variable would give you. (ctxloom sets a config-home variable for every
engine that has one dedicated to it. Antigravity does not, so ctxloom points `HOME` itself at a
per-agent directory instead.) Measured against agy 1.1.5, 2026-07-22.

Isolation looks complete. It is not. With that fresh `HOME`, no API-key environment variable
set, and the usual way of disabling the session keyring applied, **it still authenticated and
answered a real prompt.** Last time this page went looking for that channel, we came up empty
and said so. This time we found it: an OS session keyring, reached through a D-Bus Secret
Service socket at a fixed path derived from the process's UID — `/run/user/<uid>/bus` — not
from `$HOME`, and not from `DBUS_SESSION_BUS_ADDRESS`, the environment variable that normally
advertises the socket's location. Unsetting that variable removes the label a client uses to
find the socket; the underlying client library falls back to the same well-known, UID-addressed
path regardless. We reproduced it directly: `agy models` run with `HOME` pointed at an empty
scratch directory and *no other environment variable set at all* — no
`DBUS_SESSION_BUS_ADDRESS`, no `XDG_RUNTIME_DIR`, no `DISPLAY` — still authenticated through the
keyring.

That is the reductio of this whole page, and it lands differently than the last version of this
section did. The other three engines' failures were all failures of a variable — honoured,
ignored, or pulling in two directions. This one is not a variable problem. **The identity that
authenticates Antigravity is addressed by the operating system's own login session, by UID —
and no per-process environment variable was ever going to reach that**, whether or not we could
name the exact socket.

**You cannot redirect an address that was never yours to redirect.**

Given that, host-mode worktree isolation for Antigravity now proceeds instead of refusing: a
curated `HOME` genuinely isolates its configuration and session state, which is real and worth
having. But ctxloom raises a loud, non-fatal finding on every such run, naming exactly what did
and did not happen — configuration and session state isolated, authentication not — because
there is no lever this side of a container boundary that reaches the keyring, and pretending
the curated `HOME` closes that gap would be the same kind of silent overclaim this page exists
to rule out.

Containers close that channel, and not because Antigravity cooperates. A container gets its own
mount, IPC, and PID namespaces, so the keyring's socket does not exist inside it — there is
nothing at that path to fall back to. We measured this directly too: Antigravity, run in a
fresh container with no session bus reachable, logged its own fallback ("Using file-based token
storage because no D-Bus session bus detected") to a file-based credential store
(`oauth_creds.json` under `HOME`), and then failed outright — not logged in, because nothing had
been seeded into that fallback path for this run. That failure is the correct, advertisable
property: a containerized run of Antigravity cannot silently authenticate as the host user. It
can only refuse, loudly. Two edges stay open and unverified rather than ruled out either way:
whether a deliberately *seeded* file-based credential would let a container authenticate
end-to-end (not attempted here — it needs a real interactive OAuth login with no keyring
reachable, which needs a browser and a human, not a script), and whether Antigravity's binary
also carries a separate, cloud-metadata-shaped credential path, reachable only from a genuine
cloud host, or if a future container profile ever mounted additional cloud credentials into the
box. Neither experiment run here was on such a host or with such a mount, so both stay named
unknowns, not confirmed second channels.

## A fifth case: ours to fix, and already fixed

Not every gap on this page belongs to a vendor. opencode is the counter-example, and it belongs
here precisely because it complicates the pattern rather than confirming it: opencode has
always had a complete lever. `XDG_CONFIG_HOME` relocates its configuration; `XDG_DATA_HOME`
relocates its credential file (`auth.json`) and its session database, both, cleanly — the same
shape of result `CLAUDE_CONFIG_DIR` gives you for Claude Code, just spread across two standard
XDG variables instead of one engine-specific one. Measured against opencode 1.18.1, 2026-07-22.

ctxloom simply never wired it. Before 2026-07-22, opencode had no entry in ctxloom's
credential-seeding registry at all: worktree isolation set `XDG_CONFIG_HOME` for prompt
isolation and silently left `XDG_DATA_HOME` untouched, so every isolated opencode agent shared
one global credential file and one global session store — the same *shape* of failure as
Kiro's, for a completely different reason. Kiro's gap is the vendor's; opencode's was ctxloom's
own, sitting unaddressed in plain sight. It's fixed now: both `XDG_CONFIG_HOME` and
`XDG_DATA_HOME` are seeded and isolated per agent, credentials included.

It's included here not to inflate the count to five for drama. It's that this page exists to
report what was measured, honestly, including the times the finding was "we hadn't done the
work yet." A page that only ever names vendor gaps would itself be a quiet kind of overclaim.

## What would close each gap

These five are not the same kind of problem, and treating them as if they were would be unfair
to the vendors who happen to have picked the harder design:

- **Kiro — vendor-side.** `KIRO_HOME` relocates session history but not subscription
  credentials, which live in one global sqlite database under `$XDG_DATA_HOME` regardless. The
  concrete ask: a configuration-home variable whose scope includes where credentials live, not
  only where session transcripts live — or widen `KIRO_HOME` itself to cover the credential
  store the way it already covers session state. (The `KIRO_API_KEY` path already sidesteps
  this today by authenticating outside the shared store entirely; ctxloom isolates on that path
  now.)

- **Codex — vendor-side.** `CODEX_HOME` is a single, all-or-nothing home for config and every
  byte of runtime state — a reasonable design for one human at a terminal, and a flooding
  hazard for a caller trying to isolate just the config surface. The concrete ask: a
  configuration-only variable, distinct from `CODEX_HOME`'s full-state scope, for callers who
  want to isolate prompts and MCP declarations without also relocating (and having to
  gitignore) session logs, a goals database, and tens of megabytes of temp files.

- **Claude Code — ctxloom-side, already fixed.** `CLAUDE_CONFIG_DIR` relocates config and
  credentials together, cleanly — there was never a vendor gap here. The historical "locked
  out" failure was ctxloom not seeding a fresh config-home with credentials before asking
  Claude Code to authenticate from it. Fixed, by seeding `.credentials.json` on every host and
  worktree run.

- **opencode — ctxloom-side, already fixed.** Same shape as Claude Code: the lever
  (`XDG_CONFIG_HOME` + `XDG_DATA_HOME`) was always complete. ctxloom just hadn't wired the
  credential half of it. Fixed 2026-07-22.

- **Antigravity — structural.** The credential channel is an OS session keyring addressed by
  UID, not by any environment variable a caller controls — there is no lever a config-home
  variable could offer here, because the address was never the process environment's to
  redirect. This one closes only by changing the boundary: `runtime: container` severs the
  keyring socket at the namespace level, and Antigravity fails closed rather than silently
  authenticating as the host user, which is the correct outcome. A vendor-side addition would
  still help outside a container — an explicit, caller-selectable file-based credential mode
  that Antigravity would consult before ever touching the keyring — but that closes a
  convenience gap, not the structural one; the boundary is still what does the real work here.

None of this is a report of negligence. Four different teams made four different, defensible
design calls for a single-user tool, and none of those calls anticipated a caller running many
copies of the same identity side by side. That's the actual ask underneath all of it: a lever
for relocating per-session state, on tools that were never asked to have one before.

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
re-done every time they ship. And measurement has its own blind spots: on Antigravity, rounds
of env-var-shaped probing missed an OS-level channel entirely, and it only turned up once we
went looking outside that shape. Knowing today's stores does not protect you from tomorrow's,
either — a future release can add a cache, a log, a session index, and isolation degrades again,
silently, the same way. **You cannot build an isolation guarantee on top of that by
negotiation.**

**A standing note on the dates above.** Every named claim on this page is a measurement against
one specific engine version, on one specific day — stated inline, next to the claim. An
unversioned claim about someone else's software is unfair to them, because it ages into a
falsehood the moment they ship a fix. Vendor behaviour drifts, in both directions, and nothing
here is a permanent verdict on Kiro, Codex, Claude Code, opencode, or Antigravity — it is what
we measured, when we measured it. ctxloom now carries an executable acceptance matrix, built
and maintained alongside this page, that re-runs these same probes against live, authenticated
engine binaries on a recurring basis, so a claim here going stale gets caught by a failing
check, not by a reader finding out the hard way.

## Why it fails silently

None of these four failures is a crash. ctxloom reports the worktree as prepared, the
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

It is also the only answer that survives Antigravity — for a sharper reason than "we could not
find the channel." We now know exactly where its credential lives: an OS session keyring,
addressed by UID. Locating it changed nothing about host-mode's reach, because `HOME` was never
that address to begin with — no amount of finding turns a UID-scoped OS socket into something a
per-process environment variable can redirect. A boundary does not need an address to redirect
anything. Whatever the engine reads, if it is outside the boundary, it is not there. **You stop
needing to know.**

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
