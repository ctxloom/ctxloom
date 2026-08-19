---
title: "Choosing an isolation boundary"
---

You've picked `workspace: worktree`, or `runtime: container-rootless`, or both, and now you want one
question answered: isolated from *what*? Both options are called "isolation" on this site, and
neither is fake, but they hold back different things, and one of them holds back less than
people assume. Picking wrong doesn't fail loudly — the agent runs, the session looks normal,
and the boundary you thought you had just wasn't there for the one action that mattered.

This page is not going to tell you which one to use. It's going to tell you what each one
actually stops, so the choice — and it is a choice, yours to make per agent, per run — is
informed instead of assumed.

## Worktree isolation is a workspace boundary, not a security one

`workspace: worktree` gives an agent its own git checkout to write into. That's genuinely
useful: two agents working the same repo write into separate checkouts instead of racing to
stomp each other's edits, and a long unattended run has a blast radius smaller than your whole
working tree. It is a *workspace* control — which copy of the repo gets mutated — and it is
honest about being exactly that much. It is a boundary *between agents*, not a guarantee about
what happens to your own uncommitted work when you hand it to one — a worktree checkout only
ever contains committed state, so something has to happen to whatever isn't committed yet. See
[Delegating into a dirty tree](#delegating-into-a-dirty-tree) below.

It is **best effort** against anything that isn't "which directory did the write land in,"
because it was never built to hold back the three things below. None of these is a bug. Each
one is the isolation doing precisely what it was designed to do, in a situation it was never
designed to cover.

**1. The agent can just ask, and you can just say yes.** A worktree boundary doesn't remove
the agent's ability to *request* something outside it — it removes nothing about requesting at
all. Every engine ctxloom drives has an approval flow for exactly this: a permission prompt, an
escalation ladder, a `plan` posture that asks before every mutation. When an agent asks to
touch a path outside its worktree and the answer is yes, nothing was bypassed. The boundary was
opened from the inside, by the person who has standing to open it. That is the approval flow
working as intended — and it is also, categorically, not something a workspace choice can stop,
because stopping it would mean refusing your own "yes." [Agent permissions and the escalation
ladder](/concepts/agents/#what-an-agent-is) cover the actual knobs — `default`, `acceptEdits`,
`plan`, `bypass`, and a per-request-kind ladder that can auto-accept, auto-decline, or relay
upward. Read `bypass` for what it says: skip every prompt. An agent launched that way was never
going to be stopped by which directory it started in.

**2. Privilege doesn't care about directories.** An agent running under elevated privilege, or
one that calls out to a tool that itself runs privileged, isn't contained by a worktree at all.
`git worktree add` chooses which checkout of *your repository* an agent sees; it says nothing
about what the agent's process, or a process it spawns, is permitted to do on the machine. A
directory boundary and a privilege boundary are different axes, and worktree isolation only
ever spoke to the first one.

**3. The engine vendor decides where its own state lives — not ctxloom, and not you.** The
engine CLI ctxloom drives is a third-party program with its own opinions about where its
config, its credentials, and its session history belong, and those opinions are set by the
vendor's own release cadence, not by a flag ctxloom passes. ctxloom can redirect an engine's
state only where the engine exposes a way to be redirected. Where it doesn't, there is nothing
to point anywhere. And even a redirect that appears to work can quietly not cover everything:
credentials in particular sometimes live in an OS-level facility scoped to your login session
rather than to any path at all, in which case relocating a config directory changes where the
engine's *settings* live and changes nothing about where its *authentication* lives. [The
engine you don't control](/security/isolation/) is the measured account of this — real engines,
real behavior, checked rather than assumed. It's the deep dive on this one vector; this page
only needed to name it.

None of these three is worktree isolation *failing*. A workspace boundary was never a request
boundary, a privilege boundary, or a vendor-state boundary, and calling it best-effort isn't a
knock on it — it's the accurate description of the one thing it does.

## Containers close the loop, by not asking

Container isolation (`runtime: container-rootless` or `runtime: container-rootful`) answers a different question than `workspace: worktree` does, and it
answers it a different way. A containerized engine isn't held back because it read an
environment variable correctly, or because it chose to route its state through the right file.
It's held back because the boundary isn't a request at all — it's a property of the kernel.
What the process can't see, it can't act on, correctly-behaved or not. That's the whole reason
it closes gaps host-mode isolation structurally cannot: it doesn't need to locate what an
engine reads before it can be sure the engine can't reach it. See [Containers don't
ask](/security/isolation/#containers-dont-ask) for the full argument; the short version is that
containment stops being something you negotiate with a vendor binary and becomes something you
built and can verify yourself.

That's the honest upside. Here's the honest cost, and it is not small: this is what "somewhat
expensive in time and complexity" means in practice.

- **Build and pull time.** A per-engine agent image has to exist before a container can start
  from it — a build the first time, a pull if you're fetching a prebuilt one, and a rebuild
  whenever the base or the engine version moves.
- **Startup latency on every run.** A host-mode session starts a process. A container-mode
  session starts a process *inside a fresh container*, every time, whether the run takes thirty
  seconds or thirty minutes.
- **A credential story you have to solve on purpose.** An isolated engine still has to
  authenticate somehow, and "isolated from the host" and "has the host's credentials" pull in
  opposite directions. Get this wrong and the failure isn't a security hole — it's an agent
  that reports "not logged in" and can't do anything at all, which is *correct* behavior for
  the boundary you built and still something you have to plan for, not discover mid-run.
- **Debugging across a boundary.** When something goes wrong inside a container, "what does
  the filesystem actually look like right now" is no longer a question your host shell answers
  for free.

None of that is a reason to avoid containers. It's what you're taking on if you pick them, the
same way "a directory boundary only ever covered directories" is what you're taking on if you
pick worktrees. Neither list is the tiebreaker; the tiebreaker is what you're actually
defending against on this particular run.

## The two axes are independent — check both, not one

Isolation on ctxloom is two separate choices, made at different times, by different people, and
choosing one says nothing about the other:

| Axis | Values | Set where |
|---|---|---|
| **Workspace** | `none` \| `worktree` | Per invocation (`run`/`acp --workspace`, or an `agent_run` spawn's workspace field), or the project's `workspace:` default |
| **Runtime** | `host` \| `container-rootless` \| `container-rootful` | On the agent (`agent set --runtime`), or the project's `runtime:` default |

`workspace: worktree` on `runtime: host` gets you an isolated checkout and a shared, negotiated
engine state. `workspace: none` on `runtime: container-rootless` gets you a contained engine process
working directly in your live checkout — the container boundary with none of the worktree one.
All four combinations are real and each answers a different question; see [Agents & Isolation:
the two isolation axes](/concepts/agents/#the-two-isolation-axes) for the full mechanism.

One default is worth calling out because it's easy to over-read: a delegated child spawned by
`agent_run` now defaults to its own worktree when neither the call nor the project config says
otherwise — a narrowing of *that child's* blast radius on the project checkout, decided once,
at spawn. It says nothing about the runtime axis, and nothing about the engine's own credential
or session state, which is exactly the boundary the three vectors above describe. A worktree
default for delegated children is not a claim that delegated children are sandboxed from your
engine's global state. It never was, on any path.

## Delegating into a dirty tree

A worktree checkout only ever contains committed state — that's a property of `git worktree
add`, not a choice ctxloom made. HEAD and everything reachable from it, nothing you haven't
committed yet. So when a delegated `agent_run` child spawns into its own worktree (the default
above) and your own live checkout has uncommitted changes, something has to give: either the
child doesn't see those changes, or ctxloom does something to make it see them. Which one
happens is `dirty_tree_handler`, and it's a choice, not a fixed behavior — set per project, or
overridden per call.

Four options:

- **`commit`** (the default). ctxloom commits your uncommitted changes onto your current branch
  first, so the child sees everything. The change becomes real git history — at the cost of a
  commit landing on your branch that you didn't type `git commit` for.
- **`copy`**. The worktree is carved at HEAD as usual, then your uncommitted changes — tracked
  and untracked both — are reproduced *inside it* as uncommitted WIP. The child sees the same
  content `commit` would have shown it; your branch is never touched, and nothing durable is
  created beyond that one worktree. Not available when the agent has no structured-chat backend
  and runs the per-turn oneshot fallback, which tears its worktree down every turn anyway.
- **`stale`**. The child spawns against committed state only. ctxloom warns you which files it
  won't see. Cheap and honest, but the child works from whatever you last committed, not what's
  actually on disk right now.
- **`fail`**. ctxloom refuses the spawn outright and names the uncommitted paths. No automatic
  action at all; you decide what happens next.

`commit` being the default is why it's the one gated behind more than a config value. A commit
landing on your branch that you didn't ask for in the moment is the one outcome here worth a
deliberate yes, so it doesn't fire the first time you hit it: it requires a separate, one-time
project acknowledgement — `dirty_tree_commit_ack: true` in `.ctxloom/config.yaml` — and until
that's set, the spawn is refused, naming the exact key to add. That acknowledgement is
deliberately config-only. It can't be supplied as an `agent_run` call parameter, and it's never
inferred from anything an agent does — a coordinator agent calling `agent_run` typically has no
TTY and is often running while you're away, so an agent consenting on your behalf wouldn't be
your consent. Only a human, editing the file, grants it. Once granted, every individual
auto-commit still prints a warning naming the branch and the files being touched — the
acknowledgement authorizes the behavior once, not any single commit silently.

The handler choice itself is lighter-weight than that acknowledgement: a project default you
can set in `.ctxloom/config.yaml`, and any `agent_run` call can override it for itself. It's
only the *permission to commit on your behalf* that's locked to a human editing config —
picking among the three alternatives that never touch your branch is not.

See the [config reference](/reference/config/#top-level-fields) and [`agent_run`'s
parameters](/reference/mcp-tools/#agent_run) for the exact keys, values, and precedence.

## How to choose

There's no single right answer here, and this page isn't going to manufacture one. What
changes the trade is what's actually true of the run in front of you:

- **You're at the keyboard, approving as you go.** Every approval prompt worktree isolation
  can't stop is a prompt *you* are answering, in real time. The gap in vector 1 above is
  smaller when a human is the one being asked.
- **The run is unattended, or the permission posture is `bypass` or a wide `auto_accept`
  ladder.** Now nobody is at the prompt to catch a request that shouldn't have been granted.
  That's exactly when a directory-only boundary is doing the least work relative to what people
  assume it's doing.
- **The content in play is untrusted, or came from outside your review.** [A prompt is
  executable code](/security/prompts-are-code/) — if what the agent is reading could be trying
  to talk it into something, the boundary you want is the one that doesn't depend on the agent
  declining to try.
- **You need conflict-safety, not a security boundary.** Parallel agents trampling each other's
  edits is a real, common problem, and giving each one its own checkout is exactly what worktree
  isolation is for. Don't reach for a container to solve a merge-conflict-shaped problem. That's
  a different question from what happens to *your own* uncommitted edits when you delegate work
  into a worktree — see [Delegating into a dirty tree](#delegating-into-a-dirty-tree).
- **Time and complexity are real constraints too.** A container that takes longer to iterate in
  than the task takes to finish isn't a safer choice on this run — it's a slower one that also
  happens to be safer, and whether that trade is worth it is yours to weigh, not a default
  someone else set for you.

Worktree isolation being best-effort doesn't mean it's wrong to use. Containers being the
stronger boundary doesn't mean they're the default answer. The choice is the user's — this page
exists so it's made with the actual shape of the boundary in view, not the shape "isolation"
implies.

## Mechanism and further reading

- [Agents & Isolation: the two isolation axes](/concepts/agents/#the-two-isolation-axes) —
  `runtime` and `workspace`, where each is set, and what `runtime: container-rootless` mounts.
- [The engine you don't control](/security/isolation/) — the measured account of vector 3:
  real engines, real config-home behavior, checked rather than assumed.
- [A prompt is executable code](/security/prompts-are-code/) — why what an agent reads matters
  as much as where it can write.
- [`ctxloom container`](/reference/cli/ctxloom_container/) — building and checking agent
  images; [`ctxloom container build`](/reference/cli/ctxloom_container_build/) for base-image
  and override options.
