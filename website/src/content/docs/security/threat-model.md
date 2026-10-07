---
title: "Threat model"
---

A security claim you cannot state precisely is a security claim you cannot keep. This page
names the adversary, states what ctxloom defends, and — at equal length and in equal detail —
states what it does not.

If you only read one section, read [What we do not
defend](#what-we-do-not-defend). A product whose limits are hidden is worse than one with
fewer features.

## The cast

- **Alice** — a developer. She runs the agent. Her machine, her shell, her credentials. Every
  defense here exists for her.
- **Bob** — her teammate. Clones the same repo, inherits the same project config. He is not
  an attacker; he is the reason a decision has to be shareable.
- **Carol** — the team lead. She decides which repositories the project draws on, registers
  them as remotes, and commits that decision so Alice and Bob inherit it.
- **Trent** — the platform or security team: the **publisher** whose repository the team
  added. Everything his repository serves reaches Alice's agent.
- **Mallory** — the active attacker. She tampers with content after it was signed; publishes
  a look-alike library; typosquats Trent's repo; writes `signer: trent` into her own bundle
  YAML and hopes.

**There is no Eve.** Eve is the passive eavesdropper of the classic cast, and she is absent
on purpose. ctxloom makes **no confidentiality claim** about your context. Inventing an Eve
scenario would imply a defense we do not have. See below.

## What we defend

**Adding a repository is the trust decision, and nothing else makes one.** Content resolves
only through a remote you registered. A reference to a repository you have not registered is
refused, naming the `ctxloom remote create` that would add it; nothing registers a remote on
your behalf. A remote profile may refer only to bundles in its own repository, so a profile
from Trent's repository cannot pull in Mallory's.

**Tampering after signing.** Mallory edits a bundle tree that Trent signed. The publisher
signature no longer covers the bytes it sits beside. This is treated as *tamper*: the tree is
refused at install, and `deps upgrade` will not move a pin onto it.

**Changes are shown before they land.** Only `ctxloom deps upgrade --yes` moves an existing
pin; `deps pull`, `init` and startup create first pins and never move one. Before it applies
anything, `deps upgrade` shows every item a move adds, removes or changes, the command and
arguments of every hook and MCP server before and after (env and header values only as a
fingerprint), and a diff of every changed script. A first pin lists everything the bundle
carries.

**A bundle naming its own publisher.** Mallory writes `signer: trent` into her bundle's YAML.
It does nothing. The field is not deserialized from content; it can only be set by a load
path that already verified a signature against Alice's trust root.

**Companion binaries run only once registered.** ctxloom reads a companion's contribution by
running it, so the decision is whether to execute it: a companion runs only when you registered
its name (`ctxloom companion add <name>`), and nothing on `PATH` is run for being there — a
dependency that drops `ctxloom-companion-*` into `node_modules/.bin` earns nothing. The
registration is a name, so a binary of a registered name placed earlier on `PATH` is the one
that runs.

## What we do not defend

**We do not encrypt your context. There is no confidentiality claim.** Bundles travel over
git in the clear. Anyone who can read the repo can read every fragment, command, hook and MCP
declaration in it. ctxloom proves **provenance and integrity** — where content came from, and
that it was not changed. It does not, anywhere, keep it secret.

**Adding a repository trusts everything it serves.** Every fragment, hook and MCP server in a
repository you added reaches your agent, and so does every update you apply. A signature says
*who*, never *whether this is good for you*: a signed malicious fragment verifies perfectly.
Add a repository only when you would run anything it publishes, and read what `deps upgrade`
shows before `--yes`.

**A writable project config is game over.** An attacker who can add a remote or edit the
lockfile decides what your agent reads. Protect `.ctxloom/` the way you protect the code it
sits beside.

**Local content is trusted by where it is.** A bundle in a local content directory reaches the
agent because someone with write access to your project or home put it there. Its signature is
still checked, and one that no longer covers the bytes earns the author a warning when they
publish, but neither result changes whether the content reaches the agent. Anyone who can
write to a local content directory decides what your agents read.

**Your own ssh-agent can sign as you.** If your signing key is a plain software key loaded
into `ssh-agent`, then any process holding `SSH_AUTH_SOCK` — *including an agent ctxloom
itself just launched* — can ask that agent to sign as you. The defenses are `ssh-add -c`
(confirm on every use), a hardware-backed key, or running the agent in a container without
the socket.

**A repository you clone can choose which of your keys signs.** ctxloom's zero-config
signing chain reads `git config user.signingkey`, and it runs `git config` inside the
working repository — so git answers out of *that repository's* `.git/config`, a file that
arrives with the clone. Cloning is therefore enough to redirect signing to a different key.
What it cannot do is produce an attestation from a key you do not hold: the signer is always
a live `ssh-agent` identity, and Mallory's key is not in your agent. A ctxloom signature is
an attestation from a controlled key or identity — and so is a git signature; `git commit
-S` resolves `user.signingkey` from the same file and claims the same kind of thing. We
accept the boundary git accepts, and we inherit git's residual with it: the signature still
comes from an identity you control, but possibly a different one than you intended, a
personal key where a work key was meant. That is an attribution problem, not a broken
attestation. Restricting the lookup to `--global` would break per-repository identities,
which are an ordinary setup; prompting would put a consent step into a flow that most often
runs unattended in CI.

**One key signs every ctxloom release surface.** A single release key signs the default
bundles and the companion loadouts, so its compromise radius is every signed surface at once.
The binaries carry no Apple or Windows code-signing signature; see
[Trusting the Binaries](/getting-started/binary-trust/) for what that costs you.

**An agent can rewrite the repository you point it at — including the parts of `.git` that
execute code on your host.** ctxloom gives an agent its own git worktree, and the
repository's git *common* directory is exposed to that agent read-write: in a container it
is bind-mounted at its identical host path (`gitDirMounts` in
`internal/adapters/isolation/gitpointer.go`, which mounts it with `ReadOnly` false), and on the
host runtime there is no boundary in the way at all. That directory is not only objects and
refs. It holds `hooks/`, and it holds the repo-local `config`, whose `core.hooksPath`,
`core.fsmonitor`, `core.sshCommand`, `core.pager` and `[alias]` keys all name commands git
will run. An agent that writes `hooks/pre-commit` there has planted a program that runs the
next time **a human commits in the primary checkout** — on the host, outside any container,
as that user. This is not a theoretical file: ctxloom's own repository has live `pre-commit`
and `prepare-commit-msg` hooks in that directory today, alongside the `.sample` files git
ships. So state the blast radius accurately. Under accident it is a spoiled branch. Under
malice it is **host code execution**, not repository corruption.

**Four things isolate four different risks, and only three of them are controls.** It is
worth being explicit about which one owns which question, because they are routinely
credited with each other's work.

- **The trust decision, at ingest.** Which repositories you add, and what `deps upgrade`
  shows before a pin moves. This is the control against a malicious *instruction* reaching an
  agent at all. Its reach is bounded — see the next entry.
- **The container, at runtime.** Isolates the agent *process* and the host filesystem: what
  the agent may execute, and what it can see and touch outside the mounts it was handed.
- **The worktree and branch, as blast radius.** Isolation against *accident* and ordinary
  agent error. A confused agent's edits land on a throwaway branch instead of your working
  tree, and are cheap to throw away. This is **not** isolation against malice; nothing about
  a worktree stops a deliberate write from reaching the shared `.git` both of you use.
- **The read-write git mount.** Not a control. It is the residual, accepted above.

**The trust decision covers the content ctxloom delivers, not everything an agent reads.** It
covers fragments, skills, bundles, hooks and MCP declarations — content ctxloom itself
resolves and hands to the agent. It does not cover a poisoned file already committed in the
repository you point the agent at, a web page the agent fetches, the contents of an upstream
dependency it installs or reads, or an injection carried in data the agent merely processes.
None of those pass through ctxloom, because ctxloom never resolved them.

**The runner-to-coordinator link is bearer-authenticated but not encrypted.**
Every runner — a host process or a container — dials the coordinator's gRPC listener and
authenticates every stream with the run credential it was minted; a guessed port buys nothing
without that token. The link is cleartext (h2c) in this release, so the bearer crosses it
unencrypted. For a container child the coordinator binds a container-reachable listener, which
can fall back to the host's primary outbound interface — visible on the LAN — so on a shared
network treat that bearer as observable in flight. The container itself opens no listener and
publishes no port. Mutual TLS on the container-reachable listener is planned and not built.

**The host runtime is not a boundary between agents.** Every agent on `runtime: host` runs as
you. A runner's coordinator credential is the agent's identity to the coordinator; it never
sits in a process environment, but in the run's owner-only secrets file
(`CTXLOOM_COORD_CRED_FILE` names it), which lives for the whole run because a relaunch or a
re-adopted run reads it again. Every process running as the same user can read that file,
just as it could read an environment variable or ptrace the runner, so one host-runtime agent
can read another's credential and speak as that agent. If agents need to be kept apart from
each other, run them in containers: a containerized process sees only its own run's secrets.

**We own the MCP servers we seed. We do not own the ones we did not write.** An MCP
declaration is not text — a server entry names an executable, and the engine spawns it. So a
server that arrives through ctxloom's own supply chain is squarely ours: config, profiles, and
**bundles**, including remote ones, since a profile may carry an `mcp` block and profiles are
bundle-deliverable. Those servers are content we deliver from repositories you added, what each
one runs is shown before a pin moves, and if one is malicious that is our failure to have shown
it to you.

Entries we did not write are a different matter, and the separation is structural rather than a
promise:

- For **Claude Code**, ctxloom by default does not write your project `.mcp.json`. It writes
  its own servers to a private file under the session's directory, passes that file with
  `--mcp-config`, and deliberately omits `--strict-mcp-config` so the engine *layers* ctxloom's
  set on top of yours instead of replacing it. The exception is the `unsafe-file` MCP approach,
  which writes the project `.mcp.json` directly; it runs only when selected by name.

Two limits follow, and neither is hypothetical. **The approval gate belongs to the engine, not
to us.** An agent running with `permissions: {claude-code: {mode: bypass}}` is launched with
Claude Code's skip-permissions flag, which disables that gate, and a bundle-delivered server
then starts without a prompt. So all this takes is a repository you added and an agent
declared at `bypass`. `bypass` covers more than file edits; it also means "run what my trusted
bundles declare." **And a server name you already use can be displaced.** When ctxloom writes the
project `.mcp.json`, a bundle server whose name matches an entry you wrote replaces that entry
for as long as ctxloom declares it. ctxloom records the bytes it replaced and restores them
when it stops declaring that server or is uninstalled. If you edit an entry ctxloom manages,
its next write refuses rather than overwrite your edit.

## The one line we hold

Everything above reduces to a single invariant: **adding a git repository is the trust act.**
Content reaches your agent only from your project, from a companion you registered, or from a
repository you added — and what a pin move would change is shown to you before it lands.

We do not claim to know whether a prompt is safe. We claim to know **where it came from** and
**that it has not changed** — and to show you what changes before it reaches the machine
holding your credentials.

Next: [Trust](/concepts/review-and-trust/).
