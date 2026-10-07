# ctxloom Trust Model

The canonical reference for what ctxloom trusts, and why. Everything here is
derived from the enforcement code; where behaviour and a doc-comment disagree,
this document describes the behaviour.

## The invariant

**Adding a git repository is the trust act.** Registering a remote
(`ctxloom remote create <name> <url>`) is the one deliberate step that admits a
repository's content; from then on ctxloom delivers that repository's bundles at
the commits the lockfile pins. ctxloom keeps no second, finer-grained trust
decision on top of it: no signatures, no per-item review or approval, no
rejection list, no retraction, no version floor. The decision and the reasons
for it are recorded in
[ADR 0037](adr/0037-adding-a-git-repo-is-the-trust-act.md).

The threat model this serves is OTHER users, accounts and networks — not the
local user, who already chose the repositories and runs the binary. Anything
that would only defend the local user against their own choices is out of
scope.

## What enforces it

- **No resolution by reference.** Content resolves only through a registered
  remote. A pull, a lock walk, `deps check` and the read of already-installed
  content all refuse an unregistered repository with the same error
  (`remote.NotRegisteredError`, wrapping `remote.ErrRemoteNotRegistered`), which
  names the `ctxloom remote create` that would register it. Nothing registers a
  remote automatically; `Registry.LookupURL` only looks.
- **A remote profile stays inside its own repository.** A profile shipped in a
  remote repository may name only bundles from that same repository
  (`profiles.Profile.CheckOwnRepo`, refusing with
  `profiles.ErrCrossRepoReference`), so registering a repository never admits
  whatever other repositories its profiles mention. Only a local profile
  composes several registered repositories.
- **Pins move only on `deps upgrade --yes`.** The lockfile pins every
  dependency to a commit SHA, for reproducibility. `ctxloom deps upgrade` is the
  only operation that moves an existing pin (`operations.UpgradeDependencies`):
  without `--yes` it is a preview that writes nothing, and with `--yes` it
  recomputes, applies and prints what it applied. `deps pull`, `ctxloom init`
  and startup sync create first pins and never move an existing one.
- **Every pin change is disclosed before it applies.** The disclosure
  (`operations.PinChange`, rendered by `operations.WritePinChanges` and
  `operations.WriteNewPins`) lists each item added, removed or modified; for
  hooks and MCP servers the command, args, env, URL and headers before and
  after; and a full unified diff of each changed script. A first pin lists
  everything the bundle brings in, executables included. Env and header VALUES
  appear only as fingerprints (`valueFingerprint`: the first 8 hex characters of
  their SHA-256), in text and JSON alike, because this output reaches
  terminals, CI logs and JSON consumers and those values carry tokens; a
  changed value still shows as a changed fingerprint. Args and scripts stay in
  clear: a secret hard-coded there is already published in the bundle's
  repository.

## Companions

ctxloom discovers companions on `$PATH` — its shipped first-party set plus any
binary named `ctxloom-companion-*` — and EXECUTES each one it may run, to read
the context it contributes. Being on `$PATH` makes a binary a candidate only.
Which candidates run is decided by companion admission; the
[`ctxloom companion`](../website/src/content/docs/reference/cli/ctxloom_companion.md) reference states the
current rule.

## Engine workspace-trust prompts

Everything above is about which repositories' content ctxloom delivers. This
section is about a different question — does the human trust the **working
repository** to run its own code — and it is the normative statement the
engine packages point at: **ctxloom never answers that question for you. It
reads the answer you gave the engine yourself, and every launch of the engine
obeys it.**

A repository can commit executable surfaces of its own: for claude-code, a
`.claude/settings.json` (hooks, env, an `apiKeyHelper`), a `.mcp.json`, and
skills and agents whose frontmatter declares `hooks` and `mcpServers`. Under
`-p` claude's own trust dialog does not stop the settings hooks or the
`.mcp.json` (it withholds only `permissions.allow`), so the engine's answer
alone is not a gate. Repo trust is therefore part of the engine contract
(`engine.Engine.Trust`, an `engine.RepoTrust`):

- **The verdict is the engine's own record.** claude-code's
  (`claude.Claude.Trust`) is `projects[<dir>].hasTrustDialogAccepted` in your
  real `~/.claude.json`, over the directories claude itself consults for the
  run's working directory: the canonical repository root (a worktree's main
  checkout), and every directory from the working directory up to its
  repository's top level. Trusting a directory ABOVE a repository does not
  trust the repository. No answer, a false one, or an unreadable file is
  untrusted. ctxloom keeps no trust store and offers no command to trust a
  repository: trusting one is accepting claude's own prompt in your own claude.
- **It is taken once, where the cell is prepared** (`isolation.repoTrust`), and
  rides the launch to the runner (`launch.Launch.Trust`, the wire's
  `WorkspaceTrust`, which decodes anything but TRUSTED as untrusted) into the
  engine's session (`engine.Session.Trust`).
- **An untrusted repository's launch loads only user sources.** claude's one
  argv composer (`instance.execArgs`) adds `--setting-sources user
  --strict-mcp-config` (`repoSourceArgs`) to every launch whose verdict is not
  exactly trusted — interactive, `-p`, resumed and each stream-json turn — and
  refuses a presentation those flags cannot govern: a settings file named on
  `--settings`, a source `--setting-sources` does not filter
  (`errUntrustedSettingsPresented`), and the project's own `.mcp.json`, which
  strict mode ignores (`ErrUntrustedProjectMCP`). ctxloom's own hooks and
  settings live in the session home (the user source) and its MCP servers
  arrive on `--mcp-config`, so they survive.
- **A trusted repository's answer is carried, never made.** For a trusted
  repository only, the engine's `engine.InstanceConfigWriter` writes the answer
  into the session home it generates (`projectTrustKeys` in
  `internal/engines/claude/instanceconfig.go`, keyed by the directory the
  engine actually runs in — a worktree's checkout, not the project root), so
  claude does not re-ask what you already settled. An untrusted repository's
  session home carries no answer: an interactive claude asks you, and your
  acceptance there applies to that session's home alone.

| Binding | Home | Lifetime | Answer written? |
|------|------|----------|-------------------|
| `engine_home: session`, any cell | `<WorkDir>/.ctxloom/state/<harp>/home/<engine leaf>` (mounted into a container at `/ctxloom/home/<engine leaf>`) | one session | only for a trusted repository |
| undeclared / `host` / no binding, container cell | the container's own fresh `$HOME` | one run | **no** — the container receives only the credential mount, no generated config |
| undeclared / `host` / no binding, host cell | your real engine home | yours, durable | **no** — it already holds your own answers |

The launch flags follow the verdict on every row; only the written answer
depends on the home.

- **The file is never committed.** Each provisioned home is gitignored or
  ephemeral, so the machine-specific absolute path baked into an entry cannot
  reach a teammate's checkout.
- **Nothing else in the generated config is pre-accepted on your behalf.**
  `hardenedConfigKeys` pins the engine's own bypass and auto-update switches
  off; ctxloom does not pass any engine's bypass-trust or bypass-permissions
  flag to get past a prompt.

The P13 rung (`p13-untrusted-repo-hooks`) pins both halves live: claude's
behaviour run by hand, and ctxloom's own launch of an untrusted repository
(`ctxloom-launch-untrusted`, composed from the engine by `p13CtxloomLaunch`),
under which nothing the repository commits runs.

### What is generated, and what is deliberately not copied

The host's `~/.claude.json` is not a narrow trust record — it is claude's whole
top-level config, including the `mcpServers` entries for your own personal
integrations. Copying it into every agent's config home to save one dialog
would hand each agent read access to those integrations and whatever secrets
they carry. So a provisioned home gets a **generated** `.claude.json` carrying
the hardened keys (and, for a trusted repository, its trust answer), plus the
host keys `ambientConfigKeys` copies by name — among them the account identity
and, for the human's own `login` session ONLY, a Console-key login's
`primaryApiKey`; every other run's instance has it deleted, so no agent ever
holds it. No credential file is seeded into the home: every agent
authenticates with the token (`launch.RunAuth`), set in the engine's
environment by value, and only the human's own session under the top-level
`auth: login` shares their credential storage (`loginStore`), on the host; a
container refuses it. The engine auto-creates whatever else it
needs on first launch.

## Known gaps and accepted risks

1. **An agent can write to the repository it works in — and `.git` is code, not
    just data.** ctxloom runs an agent in its own git worktree, and the git
    *common* directory is exposed to that agent read-write: in a container it is
    bind-mounted through the runtime's placement policy (`gitDirMounts` in
    `internal/adapters/isolation/gitpointer.go` → `anchor(rt, common, false)`,
    the same policy the project root takes through `relocateRoot`) — the
    identical host path under the default identity policy — and on the host runtime
    nothing is in the way at all. That directory holds `hooks/` and
    the repo-local `config`, whose `core.hooksPath`, `core.fsmonitor`,
    `core.sshCommand`, `core.pager` and `[alias]` keys all name commands git
    executes. An agent that replaces `hooks/pre-commit` has planted a program
    that runs the next time a **human** commits in the primary checkout — on the
    host, outside the container, as that user. This repository carries live
    `pre-commit` and `prepare-commit-msg` hooks in that directory today, so the
    file is real, not a `.sample`. The accurate statement of the residual is
    therefore not "code and git state are corruptable": under accident it is a
    spoiled branch, and under malice it is **host code execution**. Which control
    owns which question: **choosing which repositories to add** (registering
    a remote, and reading the pin disclosure before `deps upgrade --yes`) is the
    control against a malicious *instruction* reaching an agent; the **container at runtime**
    isolates the process and the host filesystem; the **worktree and branch**
    bound the blast radius of *accident* and ordinary agent error, and are not a
    control against malice; the **read-write git mount** is not a control at all
    — it is this accepted residual. An agent handed a working repository can
    write to it; that is inherent to the job, not a defect ctxloom intends to
    close. The mitigation is upstream (point agents at repositories you would be
    willing to restore from their remote; inspect `.git/hooks` and `.git/config`
    after a run you have reason to doubt), and the risk sits with the user. Ruled
    and accepted; the in-code "Over-mount blast radius, ACCEPTED" decision is
    the same ruling.
2. **`runtime: host` is not a security boundary between agents.** Two agents
    launched with the host runtime run as the same OS user. The coordinator
    bearer credential that `Coordinator.Identify`
    (`internal/core/coord/coordinator.go`) accepts as sole proof of caller
    identity is never in a process environment: every runner reads it from its
    run's owner-only secrets file, named by `CTXLOOM_COORD_CRED_FILE`
    (`sessions.EnvCoordCredFile`), which lives for the whole run because a
    relaunch or a re-adopted run reads it again. Any process running as the
    same user can read that file, just as it could ptrace the runner, so a
    host-runtime agent can read another host-runtime agent's credential and
    speak to the coordinator *as* that agent. ctxloom does not try to hide it
    from same-user processes: the isolation boundary is a container, where a
    process sees only its own run's secrets. It is a property of the runtime
    axis.
3. **An untrusted repository's committed skills and agents.** A repository can
    commit `.claude/skills/*/SKILL.md` and `.claude/agents/*.md` whose
    frontmatter declares `hooks` and `mcpServers`. The capability ladder's P13
    rung (`p13-untrusted-repo-hooks` in
    `tests/acceptance/capability_probe_registry.go`) pins claude's behaviour:
    without flags claude loads both into an untrusted `-p` run
    (`untrusted-fires`: the init frame lists them); a trusted folder's agent
    frontmatter hook and inline MCP server execute (`trusted-frontmatter-fires`);
    and `--setting-sources user --strict-mcp-config` keeps both out entirely —
    neither is loaded, so no frontmatter can run (`setting-sources-suppresses`).
    ctxloom's side is the engine contract's repo trust (see "Engine
    workspace-trust prompts"): the trust answer is written only for a
    repository the human trusted in their own claude, and every claude launch
    into any other carries both flags (`repoSourceArgs`, from
    `instance.execArgs`). The rung's `ctxloom-launch-untrusted` cell launches an
    untrusted repository exactly as ctxloom does and shows nothing it commits
    runs. A repository the human HAS trusted runs its surfaces under ctxloom as
    it would in their own claude — that is what trusting it means.
4. **Claude's subprocesses inherit the run's hook token.** A run that serves
    the approval route puts the session endpoint's bearer into the engine's
    environment as `CTXLOOM_HOOK_TOKEN` (`sessions.EnvHookToken`, written by
    `sessions.EncodeHookReach` in the runner's `deliverAndDrive`), so every
    process claude spawns — third-party stdio MCP servers and hooks included —
    inherits it. The token authorizes only that run's endpoint. On the host
    runtime a malicious MCP server can therefore act as the run's hooks, for
    example by answering the run's own approvals. Same-uid processes are not
    isolated from each other on the host runtime (gap 2); the container
    runtime is the boundary. Ruled and accepted: the token stays in the
    environment.
5. **The `unsafe-file` MCP approach writes ctxloom's session entry into the
    project's `.mcp.json`.** Selecting the project root for the MCP surface
    (`mcpUnsafeFile`, `internal/engines/claude/surfaces.go`) writes ctxloom's
    session-endpoint entry into the project's own `.mcp.json`, a file teams
    commit. The bearer is never in it: `bearerByReference` writes
    `${CTXLOOM_CLAUDE_RELAY_BEARER}` (`relayBearerRef`) and the value rides
    claude's process environment on the presentation's env channel, which
    claude expands into the relay it spawns. That puts the bearer in claude's
    environment, where every process claude spawns inherits it — the same
    exposure as gap 4, accepted on the same terms. The entry itself (the
    relay command, the loopback URL, the reference) is reversed through the
    ownership record (`fsstatic.Records`, writer `delivery.SessionWriter`):
    at the runner's end (`runner.Host.Teardown`, called from `runner.Main`),
    or by the run itself when driving fails (`deliverAndDrive`). A runner
    killed before its teardown leaves the entry until a later run's
    `sweepDeparted` reverses it, which happens only once the originating
    session's liveness lock proves it gone (`sessions.Locks`); a held,
    missing or untrusted lock leaves it in place. Until then a plain `claude`
    started in that project spawns the relay with the reference unexpanded,
    which cannot authenticate, and runs on without that server. A commit
    made while a run is live, or before the sweep, still captures the entry —
    without a secret. The default MCP approach (`mcpConfig`, the private
    `--mcp-config` file) writes nothing into the project.
6. **Artifact uploads have a per-upload cap and nothing else.** Each upload is
    bounded by `coord.ArtifactUploadSizeCap`, but there is no count cap, no
    per-run total and no garbage collection, so a child can fill the
    coordinator's disk by uploading repeatedly. Accepted under the host-runtime
    trust model: a child that can upload is already a same-user process on
    that host (gap 2).
7. **The coordinator's listener can be reachable beyond this host.** It never
    binds `0.0.0.0`, and a plain host session binds loopback only
    (`TestServe_BindsLoopbackOnly`, `internal/adapters/coordgrpc`). When a
    container run's runtime has no private route home — no loopback
    translator and no bridge gateway, as with rootless slirp/pasta — the
    coordinator also listens on the host's primary outbound address
    (`publicRoute` in `internal/adapters/isolation/runtime.go`), which is
    reachable from the LAN, and reports that once per address. Every stream
    and request on any listener needs a per-run bearer (`Coordinator.Identify`),
    carried in cleartext h2c (see `docs/architecture/agentcoord/transport.md`).
    The outbound address is kept because without it a rootless container
    cannot reach the coordinator at all. Ruled and accepted. What crosses it
    is that per-run bearer and the launch, never the human's engine
    credential: a container cell names its credential by file
    (`launch.Placement.SecretFiles`), written on the originator's side to an
    owner-only dir on `$XDG_RUNTIME_DIR` and mounted read-only
    (`isolation.containerPlacement`, `isolation.materializeSecrets`). A host
    cell still carries its credential in the launch's env, over the
    loopback-only listener.
8. **Any same-user process can write into a session's spool.** Delegated
    agents exchange mail through a per-session spool directory
    (`internal/core/spool`). Its root lies under `paths.HomeSessionsDir`, which
    every ctxloom process establishes owner-only at startup
    (`paths.EnsureHomeRoots`), and `spool.EnsureDirs` creates every directory
    beneath it owner-only. That bounds the spool to the user account and
    nothing finer: an agent can delete its own unread `in/` mail, and any
    process running as the same user can forge parent mail into a child's
    `in/`. No per-message credential stands in the way. The same reasoning as
    gap 2 applies: the boundary between agents is a container, not the host
    runtime.
