# Isolation — `internal/adapters/isolation`

`internal/adapters/isolation` decides **where an agent's working directory lives** and
**where its engine process executes**, prepares that workspace, and hands back
a transport-free `RunnerHandle`. It
owns an **ordered degrade chain whose floor is always the host**, and the rule that
every drop of an explicitly-requested boundary is *refused* — a
`strictness.FailAlways(KindIsolation, …)` finding that aborts in **both** modes,
never a weaker cell. `--degraded` does not reach these: it means "deliver less",
not "drop the sandbox". It also owns the agent container image lifecycle and the
stored engine token.

It deliberately does **not** import `internal/engines` or `internal/adapters/operations`: it reads engine facts by name through `isolation.Facts`, installed by the composition root.
Backend names cross as bare string keys (documented connascence of name), and
`EngineStarter` and `SetBinaryVersion` exist purely to keep the dependency
direction one-way.

## Two axes, six postures

Isolation is **not** one enum. It is two independent axes:

- **`WorkspaceAxis`** — declared at the orchestration level (`--workspace`, project
  default). `WorkspaceShared = "none"`, `WorkspaceWorktree = "worktree"`.
- **`RuntimeAxis`** — declared as an *agent* trait (`runtime:` on the binding).
  `RuntimeHost = "host"`, `RuntimeContainerRootless = "container-rootless"`,
  `RuntimeContainerRootful = "container-rootful"`. There is deliberately no "any
  container" value (`IsContainerRuntimeAxis` is the "is a container requested at
  all?" predicate): rootless and rootful containers differ in UID mapping, so a
  workload can genuinely require one, and a caller that could not say which one
  used to silently get whichever the daemon offered.

`Axes` combines them, with `WantsWorktree`, `WantsContainer`, `Zero`.

Two workspace values × three runtime values is **six** requestable combinations,
not four. All three runtime values that name a container (`container-rootless`,
`container-rootful`) still realize the SAME two policy identities as before —
ownership decides which `Runtime` (`SelectRuntime`) is allowed to SERVE the
request, not which `Policy` realizes it — so the six combinations resolve to the
same four `Policy` identities in the table below, split by which container
ownership each one demands.

```mermaid
flowchart TD
    A["Axes{workspace, runtime}"] --> B{"chainFor()"}
    B -->|"{worktree, container-rootless}"| C1["Container{worktreeBase}<br/>'container-worktree'"]
    B -->|"{worktree, container-rootful}"| C1
    B -->|"{none, container-rootless}"| C2["Container{hostBase}<br/>'container'"]
    B -->|"{none, container-rootful}"| C2
    B -->|"{worktree, host}"| C3["Worktree<br/>'worktree'"]
    B -->|"{none, host}"| C4["None<br/>'none'"]

    C1 -->|"FATAL KindIsolation if the DEMANDED ownership is unreachable<br/>(never substitutes the other ownership mode)"| C3
    C2 -->|"FATAL KindIsolation if the DEMANDED ownership is unreachable<br/>(never substitutes the other ownership mode)"| C4
    C3 -->|"warn only"| C4
    C1 -.->|"NEVER skips to"| C4

    style C4 fill:#8884,stroke:#888
```

| Axes | Policy | `Name()` | Ownership demanded | Isolates | Does **not** isolate |
|---|---|---|---|---|---|
| `{none, host}` | `None` | `"none"` | — | nothing — the fault-tolerant floor | everything |
| `{worktree, host}` | `Worktree` | `"worktree"` | — | cwd (detached git worktree at `HEAD`) + **one host lever per backend**: the scoped config-home env var its descriptor's `Home` declaration names | engine *global* state where the engine ignores the var; the git common dir; the auth token (it rides the env) |
| `{none, container-rootless}` | `Container{hostBase}` | `"container"` | rootless only | process, fs view, fresh `$HOME`; project mounted at its **identical absolute path** | the project dir (mounted RW) and the whole `.git` common dir (mounted RW) |
| `{none, container-rootful}` | `Container{hostBase}` | `"container"` | rootful only | same as the rootless row | same as the rootless row |
| `{worktree, container-rootless}` | `Container{worktreeBase}` | `"container-worktree"` | rootless only | as above + a per-agent checkout as cwd | the git common dir is still whole-dir RW |
| `{worktree, container-rootful}` | `Container{worktreeBase}` | `"container-worktree"` | rootful only | same as the rootless row | same as the rootless row |

### Degrade rules

`chainFor` builds the ordered chain. **Each step drops exactly one axis**: a
container tier never degrades *into* a worktree that was not requested, and a
requested worktree is never dropped because the container failed.

- Container requested with no runtime reachable **that provides the demanded
  ownership** → `strictness.Fail(KindIsolation, …)`. `SelectRuntime` picks a
  container runtime only when it is launchable AND its probed ownership
  (`ownershipAxis`, off the daemon's actual rootless-ness) IS the demanded one; a
  rootful request never lands on a rootless daemon and vice versa. A mismatch
  and "no runtime at all" both return `Host{}` from `SelectRuntime` and take the
  same fatal path — **an ownership mismatch is never a substitution, only ever a
  fatal `KindIsolation` finding** (the exit-code-3 fatal-findings path). Neither mode
  falls back to the HOST any more, and neither ever substituted the other
  ownership mode: satisfying a rootful request with a rootless container (or the
  reverse) is the identical substitution wearing a flag, and dropping the
  container altogether was the strictly larger one.
- `prepareChain` classifies any **container → non-container** transition as
  fatal (`IsContainerPolicyName`).
- A **worktree → None** transition is `clidiag.Warn` only — deliberate.
- `warnUnknownAxes`: an unknown *workspace* value warns; an unknown *runtime*
  value (including a typo of `container-rootless`/`container-rootful`) is a
  **fatal** `KindIsolation` finding. Empty string = unset = host default, no
  diagnostic.
- `Prepare` **never returns an error** — the chain always terminates in a
  workspace, because `None.PrepareWorkspace` cannot fail.

**Zero-value polarity is correct throughout these axes.** They are strings where
`""` means unset (host default), and a non-empty unknown runtime raises a fatal
finding.

## Container mechanics

### Image build — two stages

**Stage 1 (base)** precedence, `baseForIdentity`: **user Containerfile >
devcontainer > embedded default**. The embedded default (`defaultBaseStage`) is
`FROM node:22-slim` and bakes `git`, `ripgrep`, `curl`, `ca-certificates`,
`unzip`, `jq`, `strace` plus `TERM=xterm-256color`.

**Stage 2 (agent)**, rendered by `composeAgentContainerfile(engine)` — ONE
engine per image — in order:

1. `ARG BASE_IMAGE` → `FROM ${BASE_IMAGE}`
2. `baseContractLayer` — best-effort apt tool layer
3. `overlayUserLayer` — creates uid/gid 1000 `ctxloom`, installs gosu where apt exists
4. `overlayUserGate` — **fails the build** without `id ctxloom` and without a privilege-drop path (`setpriv`, or `gosu` + `usermod` + `groupmod`)
5. `COPY ctxloom-entrypoint` + `ENTRYPOINT`
6. **the engine's one `RUN` install layer**
7. version/provenance/engine `LABEL`s
8. `COPY ctxloom` + `COPY companions/`
9. `RUN /usr/local/bin/ctxloom version`
10. `companionGate` — drops ABI-incompatible companions with a warning rather than failing

Identity is keyed on the ctxloom VERSION together with the content:
`composedContentHash` is `sha256(base content ‖ NUL ‖ engine)`, tagged
`ctxloom-agent-<engine>:<version key>-<hash>` by `composedImageTagFor`. Both the
engine and the version are in the TAG, not only the hash, so a wrong image is
visible in `docker images` rather than only by recomputing a digest — and images
built by different ctxloom versions COEXIST instead of overwriting one shared
tag. The key comes from `hostImageKeys`: ctxloom's commit (`versionCommitKey`,
not the raw stamp, which embeds a build timestamp) plus the digest of the
ADMITTED companions. The companion digest is in the tag, not only the
provenance label, because admission follows the invoking HOME's trust: two
environments at one commit stage different images, and sharing a tag made each
rebuild over the other's.

**THE ORDER ABOVE IS LOAD-BEARING.** The version `LABEL`s interpolate
`ARG CTXLOOM_VERSION`, which changes on every build, and docker invalidates
every layer after a changed one — so with the labels ABOVE the engine install,
every ctxloom rebuild re-runs the vendor's installer. That is how a cell came to
die repeatedly on a vendor installer exhausting GitHub's anonymous API quota
before the order was fixed. The engine install goes above
everything that changes per build; the ctxloom binary goes last.
Provenance (`HostProvenanceDigest`) is `versionProvenanceKey`, then a digest of
the staged companions' self-reported versions (`companionVersionKey`), then the
base config's content hash — stamped as `LABEL ctxloom.provenance` and checked
by `imageStale`. It keys on the VERSION rather than a digest of the running
binary's bytes: that digest changed on every build, so every agent image read as
stale and was rebuilt for nothing. A tracked-dirty build still rebuilds, because
`versionProvenanceKey` keeps the build timestamp for a dirty stamp.

The companion half exists because the image bakes the companions too, and
keying on ctxloom's version alone let an image holding an OLD companion read as
fresh until ctxloom's own version happened to move. A companion present on PATH
that cannot answer `<bin> version --format json` raises a `KindConfig` finding
— fatal by default, warn-and-continue under `--degraded` — rather than dropping
out of the key unnoticed.

Build orchestration: `Container.ensureImage` (single-flight per `(runtime, tag)`)
→ `runEnsureImage` → `buildFromSource` → `buildBaseImage` → `buildImage`. **No
implicit pull** — an absent image is either built from a known source or the
policy degrades (`Container.imagePresent`).

Devcontainer resolution (`resolveDevBase`) strips JSONC and handles image /
build / compose forms. Declared devcontainer **features are warned about, not
honored** (`warnDevcontainerFeatures`).

### Run mechanics

In-container conventions: image `ctxloom-agent:latest`, binary
`/usr/local/bin/ctxloom`, `HOME=/home/ctxloom`.

`Container.PrepareWorkspace` is the whole gate: **gate → host scratch → base
workspace → shared-FS probe → mounts/env**. Failure at any point returns an
error and produces a loud degrade; a panic guard removes the scratch.

**Mounts** (`Mount{Host, Container, ReadOnly}`):

- The project dir where the runtime's mapper routes it (`relocateRoot`) — its identical path on a POSIX host; see [Host path mapping](#host-path-mapping).
- `gitdirMirrorMounts` when `.git` is a pointer file; `gitDirMounts` mirrors the common dir **read-write** at its mapped path, masks its `worktrees/` registry with an empty **read-only** scratch dir and mounts this checkout's own admin dir back into it **read-write** (`gitRegistryMask`), and `gitPointerMounts` shadows the checkout's `.git` pointer and its admin dir's back-pointer with **read-only** mapped copies wherever the mapping renames paths.
- `containerConfigOverlay` — one scratch-backed bind per profile `overlayDirs`, seeded by `seedOverlay`, targeting the mapping of the project path it shadows, with the host mountpoint pre-created so it is never root-owned.
- `sessionStateMounts` — scoped RW mounts: engine transcripts (at `engineContainerSpec.transcriptStoreRel` under container `HOME`), the session persist dir, and **this project's** task log `~/.ctxloom/tasks/<project-id>.jsonl` plus its `.lock` sidecar — two single files, never the `~/.ctxloom/tasks` dir, which holds every project on the machine. `safePathSegment` validates the harp, and `paths.HomeTasksLogPath` the project id, before they become host paths.

**Env** (`renderRunSpec`): entries are emitted as `-e <entry>` and are **either**
a bare `NAME` (value read by the runtime from the launcher's own
`os.Environ()`) **or** a literal `KEY=VAL`. The bare form exists so credential
*values* never enter the world-readable argv.

**Uid remap / entrypoint**: the image `ENTRYPOINT` is
`/usr/local/bin/ctxloom-entrypoint`; `identityEnvArgs` passes `-e
PUID=<uid> -e PGID=<gid>` from `runIdentity` and nothing else — the launching
user on a POSIX host, the image's own `ctxloom` user (`imageUserID`) on
Windows, which has no POSIX uid to remap to. **The entrypoint refuses to
run the engine as root, and ctxloom passes no way to override that** — the
`CTXLOOM_ALLOW_ROOT=1` escape hatch, previously sent under
`strictness.Degraded()`, was removed: `--degraded` means "a thinner run", never
"run as root with the project mounted". Rootless podman additionally gets
`--userns=keep-id`.

**Network**: a runner's `--network` (`RunSpec.Network`) has one producer, the
route home (`hostRoute.network`, from `reachRoute`), so where a runner dials and
which network it sits on cannot disagree. It is set only where that route needs
it: rootless podman's translator option that opens a route to the host's
loopback, or — when this process itself runs in a container — the network of
its own container that a sibling can join (`pickSelfNetwork`). No network
isolation is applied or claimed. The runner spec (`Container.buildRunnerSpec`) has no
socket mount and no published port at all; "the absences are the security
contract".

### Host path mapping

The mount SOURCE is the host path as this process sees it (except under
docker-outside-of-docker, below); the runtime translates it (Docker Desktop and podman machine both take a native `C:\...`
source). The TARGET is a name ctxloom chooses in the container, through the
runtime's `pathMapper`, and the mapper varies by **host OS only** — chosen at
compile time by `hostMapper` in the `hostos_{unix,windows}.go` twins, never by
runtime name:

- POSIX host: `identityMapper`. Linux shares the kernel's paths; Docker Desktop
  and podman machine on macOS share the user's paths into their VM at the same
  names.
- Windows host: `driveLetterMapper` — `C:\Users\ben\proj` is
  `/mnt/c/Users/ben/proj` (the WSL and podman-machine convention). The drive
  letter is lowercased, the rest keeps its case, a `\\?\` prefix is stripped,
  and `..` cannot leave the drive. Share and device paths (`\\server\share`,
  `\\wsl.localhost\...`, `\\.\...`) are refused as `errUNCPath`, reported as
  `present.ErrUnreachableRoot` with the remedy to run the Linux build inside the
  WSL distro that holds the project.

Every mount is built by the runtime's path seam (`pathSeam`, from
`Runtime.paths()`), and nowhere else — `TestArch_MountsAreBuiltByThePathSeam`
refuses a mount literal outside it. The container side of a HOST-anchored path
is the seam's `targetFor(host)` (`expose` binds it there); the container side of a CONTAINER-anchored path
(under the instance home or `$HOME`) is `path.Join` over a POSIX root;
`filepath` never builds a container path. `Prepare` routes the requested
environment's roots once, with no effects, before the workspace chain, so an
unroutable project is refused as unreachable rather than read as an
unstartable container. `mountArgs` renders each `--mount` as one CSV record,
because both runtimes parse it with `encoding/csv`.

Docker-outside-of-docker is the seam's OTHER rule: there this process's paths
are not the daemon's, so the bind SOURCE is rewritten (`sourceFor`, through
this process's own container mounts) while the target is not. Targets never
read that rule; it is applied only when `mountArgs` renders the argv, which is
where a path the daemon has no name for is refused.

A Windows host's container reaches the coordinator through the runtime's own
route (`reachRoute`): Docker Desktop's `host.docker.internal`; a podman
machine on WSL (`machineVMIsWSL`) takes the host's primary address, public and
warned, because its `host.containers.internal` names the machine VM.

### Launch path

| Path | Entry | Shape |
|---|---|---|
| Attached `docker run`, transport-free | `Container.StartRunner` → `startDirectRunner` | stderr to a bounded ring; background reap (`reapRunProcess` — the fix for a measured 846-zombie PID leak); teardown `removeContainer` |

### Shared-FS verification

A bind mount does not resolve through every daemon (Docker Desktop,
remote daemons, DinD). Before launch, `mountProbeRoots` derives the real host
roots, `sharedFSProbe` memoizes but **only latches definitive outcomes**
(`definitiveProbe`), and `probeOneRoot` writes a marker inside the real root and
reads it back through a scratch container. `runSharedFSProbe` **errors on an
empty root set** rather than reporting "ok". `sharedFSGateError` distinguishes a
definitive `*sharedFSMismatch` from a transient probe failure.

Scope: the probe proves the daemon sees each root's CONTENT through a native
source — it mounts every root at the fixed `/probe` target, so it needs no
mapper and runs unchanged on Windows. It does not check the mapped TARGET
names; those follow from the mapper. A Windows share path never reaches it:
the mapper refuses it first.

## Credential delivery

How a run authenticates is its AGENT's choice, declared as `auth:` on the
binding (`agents.Agent.Auth`) in engine-neutral words (`engine.AuthMode`):
`login`, `token`, `api-key` or `cloud`. Undeclared is `token`, so the human's
own login is reached only by name; `ctxloom init` gives the default agent
`login`. It is purely per agent: top-level, delegated or one-shot, on the
session home or the real one (`engine_home: host` changes the home, never the
credential).

**One check.** `engine.CheckAuth` is the only validation of an auth
selection, and config load (`config.Validate`), `agent create/edit`
(`operations.validateAgentAuth`) and every launch (`operations.resolveRunAuth`,
through `checkAgentAuth`) all run it, so they cannot disagree. Each refusal is
typed — `ErrUnknownAuthMode`, `ErrAuthModeUnsupported`, `ErrEngineHasNoAuth` —
and carries a remedy (`report.Errorf`) naming the modes that engine's
`Auth.Modes` returns. Whether the credential is available is the engine's own
answer (`Auth.Credentials` returning `ErrNoCredential` with its remedy); write
time asks it too, and tolerates only a missing token, which is read where a
run is launched (often from a secret manager), not where the config is
edited — the run refuses it instead.

**The engine owns the meaning.** Which variables carry a mode and the
precedence between them live behind `engine.Auth` on the engine's
`HomeSpec`. `Credentials` returns an `engine.Credentials`, which names nothing
about where the run executes: `Env` is laid over the engine's environment,
every name in `Unset` is REMOVED from it, and `Stores` are the human's own
credential stores the mode shares (`engine.SharedStore`: the var that points
the engine at one, the launching env's exact value for it, its place under
`$HOME`, and whether the run only reads it). Removal is not an empty value:
some variables read `""` as a real default, and some switches count as set
whatever their value.

For claude (`claudeAuth`) the declared mode decides, against claude's
documented precedence (https://code.claude.com/docs/en/authentication,
"Authentication precedence": a cloud-provider switch, then
`ANTHROPIC_AUTH_TOKEN`, then `ANTHROPIC_API_KEY`, then `apiKeyHelper`, then
`CLAUDE_CODE_OAUTH_TOKEN`, then a named `ANTHROPIC_PROFILE`, then `/login`).
Each mode sets its own credential, read from the launching env, and unsets
whatever would outrank or replace it:

- `login` shares one read-write store: `CLAUDE_SECURESTORAGE_CONFIG_DIR` with
  exactly the string the launching env's claude resolves its storage from —
  the inherited var when set, else the human's `CLAUDE_CONFIG_DIR` byte for
  byte, else `""` (`$HOME/.claude`). That var moves only claude's credential
  storage (the file, its write lock, both refresh locks) apart from
  `CLAUDE_CONFIG_DIR`, so a session-home run holds the SAME credential and
  lock pair as the human's claude; `TestClaudeSecureStorage_FollowsTheVar`
  (`just test-conformance`) pins that the installed claude honours it on
  Linux.
- `token` sets `CLAUDE_CODE_OAUTH_TOKEN`, the long-lived token the human
  mints with `claude setup-token` and exports; claude never refreshes it or
  writes it to disk.
- `api-key` sets `ANTHROPIC_API_KEY`.
- `cloud` passes the provider's own variables through from the shell (the set
  is claude's `cloudVars`, taken from claude's Bedrock, Claude Platform on AWS,
  Google Vertex, Microsoft Foundry and gateway pages), and is refused when no
  provider switch and no gateway bearer is set. The provider's credential
  FILES are shared stores, declared only when present in the human's home
  (`providerStores`: `~/.aws`, the AWS shared config and credentials;
  `~/.config/gcloud`, gcloud's application-default credentials), read-only
  except `~/.aws/sso/cache`, which the AWS SDK rewrites on an SSO refresh and
  is therefore a read-write store nested in the read-only `~/.aws`. A
  variable that names a credential file (claude's `credentialFileVars`) is
  declared in `engine.Credentials.FileVars`: the host keeps the human's path,
  and a container binds that one file read-only where the runtime's path seam
  routes it and points the variable there (`containerRelocator.relocateFiles`),
  refusing a path the seam cannot route (`present.ErrUnreachableRoot`) or one
  naming no absolute, existing regular file.

Every mode but `login` unsets `CLAUDE_SECURESTORAGE_CONFIG_DIR`: `""` would be
`$HOME/.claude`, the human's own credential.

Why the env modes carry only long-lived credentials: an OAuth refresh token
is single-use and rotating. Native claude sessions stay in step only because
they share one credentials file AND one lock beside the config dir. A copy
has its own lock, went stale, and a refresh from one revoked the rest. A
credential nobody refreshes has no second holder to fall out of step with.

**No credential store.** ctxloom never collects, stores or mints a
credential: Anthropic does not allow a third party to "collect, store, or
intermediate Claude.ai credentials or session tokens"
(https://code.claude.com/docs/en/legal-and-compliance). The human mints a
token with the engine's own flow and exports it, or a key, where ctxloom is
launched; `engine.Auth.Credentials` takes only the mode and the launching
env. `ctxloom auth status` reports, per engine and env-carried mode, whether
the credential is exported (variable names, never values) and the engine's
remedy when it is not.

**Resolution and delivery.** `operations.Cells.Prepare` resolves the run's
`engine.Credentials` (`resolveRunAuth`) BEFORE the environment exists, with
no input about where it will run, and hands them to the environment
(`isolation.SpecBuilder.Credentials`). A preview resolves the same
credentials with every value redacted (`previewRunAuth`). The
environment makes each piece true where the engine runs:

- the HOST sets each store's var to its value in place, so the run and the
  human read the same store;
- the CONTAINER mounts each store's host directory at its place under the
  container's `$HOME` (never `$HOME` itself, never the session home;
  read-only when declared so) through `relocateRoot`, the same helper that
  produces every presented path with its mount, and sets the var to `""`,
  which points the engine there;
- BOTH refuse a declared store whose directory is missing (`stageStores`,
  wrapping `ErrNoCredential`, remedy naming the directory and `auth: token`):
  a run that would start logged out fails loudly instead.

`Env` rides the placement's env and `Unset` its `Unset`
(`launch.Placement.Unset`, the wire's `Cell.unset_env`) to the runner, which
removes those names from its own
environment before it drives the engine (`runner.Deps.Unsetenv`, refusing with
`ErrEngineEnvUnscrubbed` when it cannot). Nothing enters the ctxloom process's
env. A credential the launching env does not export is the engine's own
refusal, with its remedy (claude's token: run `claude setup-token` and export
`CLAUDE_CODE_OAUTH_TOKEN`): nothing prompts, and no run starts logged out.

**Where a credential may and may not be.** It lives in the environment
ctxloom was launched in, in the coordinator's and runner's memory, in the StartRun message between
them, and in the engine process's environment. It is never journalled (a run
fact records `cred_hash` and MCP server names, never an env), never in a
container's `run` argv (it reaches the in-container engine through the
launch's env over the wire), and never in a file: an interactive launch
(`runner.RunLaunchSpec`) hands the environment straight to the engine process
it runs on a pty, and writes no launcher script. `TestRun_TheCredentialIsNeverLoggedPersistedOrEchoed` scans a
run's output, the ctxloom home, the project and the run's temp dir for a
sentinel.

A container adds no auth question of its own: `engine.ContainerSpec` says how
the image is built, and its run authenticates exactly as a host run does.
Whether an engine may run in a container at all is whether it declares a
container story (`isolation.HasContainerStory` / `ContainerStoryEngines`); an
unmapped or empty backend reaches the default spec, which **fails closed** at
the container gate (`noContainerHint`).

**`engine_home: host`.** On the host it runs claude against the real
`~/.claude` in place, with claude's own lock, and copies nothing; the agent's
declared auth still applies. In a container it means the container's own
fresh `$HOME`; the real `~/.claude` is mounted only as a `login` agent's
shared store.

### On a macOS host

Nothing in this design has run on macOS: `just build-cross` compiles it for
darwin/arm64 and no more. Each claim below is sourced or marked.

- **`login`.** claude keeps its login in the macOS Keychain, falling back to
  `~/.claude/.credentials.json` (mode `0600`) when the Keychain refuses the
  write, and a set `CLAUDE_CONFIG_DIR` keys the Keychain entry to that
  directory (https://code.claude.com/docs/en/authentication, "Credential
  management"). That `CLAUDE_SECURESTORAGE_CONFIG_DIR` also moves the Keychain
  item name is INFERRED from the claude binary's strings, measured on Linux
  (claude's `SecureStorageEnv` doc); it is UNVERIFIED on macOS, and so is
  whether a session-home run sharing the login this way reaches the human's
  Keychain item.
- **`token`, `api-key`.** Read from the launching environment; ctxloom
  stores nothing and never uses the Keychain.
- **`cloud`.** The provider's variables from the human's shell; nothing
  stored.
- **Refused: a macOS container agent declaring `login`.** The Keychain cannot be
  mounted into a container. claude's darwin build declares its login store
  with no place under `$HOME` (`loginStoreHomeRel` is `""`), and the
  container environment refuses any store it cannot place
  (`errStoreNotADirectory`, wrapping `ErrNoCredential`), naming the Keychain,
  remedy `auth: token`. The host shares it in place. On Linux the same
  agent's store is mounted. Both refuse a missing store.
- **Where an implementation plugs in.** `engine.Auth.Credentials` decides the
  env and the shared stores per mode; `loginStoreHomeRel` is the per-OS answer
  to where claude's login lives. OPEN, not decided, for a macOS container
  with `login`: the human declares `auth: token` for container runs; or the
  Keychain item is exported into an owner-only file mounted for the run
  (which reopens the copy-and-refresh problem above).

## Engine config homes

An engine's **home** is where it keeps what is not project-specific: config,
global prompts / skills / steering / agents, session state, and credentials.
Every engine names one env var that relocates it.

**Your real engine home (`~/.claude` for claude-code) is the durable truth,
and ctxloom never writes it.** Engines natively keep per-project durable facts
there, path-keyed (claude's per-project keys). That stays the single durable
location.

What an agent run gets instead is a **per-session INSTANCE**,
`~/.ctxloom/sessions/<harp>/home/<engine-leaf>`, created at session start
and disposable. Two content classes live in it: ctxloom-generated content
(context, prompts, skills, config fragments) regenerated at each launch, and
engine-specific scaffolding the engine package writes (claude's
`.claude.json`, carrying the account identity and onboarding answers by name).

No credential is placed in it. The run shares the human's login or
authenticates from its env (see [Credential delivery](#credential-delivery)).

One accepted cost remains, and it is deliberate: trust/onboarding answers given
inside an instance die with it (re-prompted next session unless the engine's own
answer already lives in the real home).

An engine's **cwd-keyed** surfaces are a different thing entirely and are never
relocated: `CLAUDE.md` and `.claude/` live at the project root, where the
engine natively looks.

| Engine | Var | `engine_home: session`, host cells (none / worktree) | `engine_home: session`, container | undeclared / `host` / no binding, host cells | undeclared / `host` / no binding, container |
|---|---|---|---|---|---|
| claude-code | `CLAUDE_CONFIG_DIR` | `~/.ctxloom/sessions/<harp>/home/claude` | the same host directory, bind-mounted at `/ctxloom/home/claude` (the fixed instance root + the declared leaf), which is what the engine is told | **real `~/.claude`** | the container's fresh `$HOME/.claude` |

An engine whose only relocation lever is a shared var (`XDG_CONFIG_HOME` /
`XDG_DATA_HOME`) cannot be given an instance this way: relocating those moves
git's, fish's and every other XDG-aware tool's config for the child too, so its
in-tree home stays uncontrolled.

The instance root resolves through one helper, `paths.HarpSessionEngineHomes` — the
session's own `home/` member under `~/.ctxloom/sessions/<harp>/` — and each
engine appends its OWN leaf (its `HomeVar.Subdir`). The leaves are distinct by
construction, so one session root hosts every engine that session runs. The
location is deliberate: under the ctxloom home an instance sits outside every
project tree, where no `.gitignore` has to
keep it out of a commit — see [the `.ctxloom` layout page](../../layout.md).

**Instances are per SESSION, not per project.** Two concurrent sessions in one
checkout get two homes — isolation the in-tree axis did not have while the home
was project-scoped. Two runs *within* one session (a coordinator and its in-tree
delegated child, which inherits the harp on `req.Env`) deliberately share one
instance. A run with no session name gets no instance at all and keeps the
engine's real home; there is no session-less fallback, because a shared one
would be the project-scoped home this model replaced.

### The rule: `engine_home: session`, declared, on every cell

Each agent binding declares its own policy, `agents.<name>.engine_home:
host|session`:

```yaml
agents:
  coder:
    engine_home: session   # a per-session instance under ~/.ctxloom/sessions/<harp>/home/ (also the default)
    # or: host             # the engine's real host home, the unsafe selection
```

**Empty (undeclared) DEFAULTS TO `session`.** Only a binding that names
`engine_home: host` keeps the real engine home, and the plan and the launch
banner name that selection unsafe.

**A declared value WINS on every invocation path that binding resolves
through** — a bare `ctxloom run` under `default_agent`, `run --agent`, a
delegated child, a oneshot fan member, alike. Invocation never matters for a
declared binding; only whether a binding is in play at all does. A run with
**no agent binding whatsoever** (no `--agent`, no `default_agent`) has no
`engine_home` to read in the first place, and always keeps the real host
home — there is no binding through which it could even opt in. Decided in
`operations.ResolveInTreeAgentHome` off the resolved binding's *effective*
`HomeMode` (`agents.ParseHomeMode`), the single place the condition
lives; bound through `operations.BindAgentHome` by every launch path.

**The home is orthogonal to the cell.** Nothing in that decision reads which
workspace or runtime the run chose. The cell decides only how the home is
*presented*: a host-executing cell (none or worktree) tells the engine the host
path itself; a container cell mounts the same host directory at
`/ctxloom/home/<leaf>` — a FIXED, well-known in-container root
(`Container.WithInstanceHome` overrides it) plus the leaf the engine DECLARES
(`agent.HomeVar.Subdir`, never re-derived from the host path) — and tells the
engine that target (`isolation.ContainerInstanceHome` supplies the root,
`isolation.MountEngineHome` records the mount; no credential file is mounted
with it). A worktree's own env (`isolation.EnvWorkspace`) carries the
scratch dir and git identity it provisioned and never a config-home var — a
second carrier there is how a run's home once came to depend on which
workspace it happened to pick.

A delegated child, a fan-out member, a `run --agent` — these ARE ctxloom's
processes, and pointing one at the human's real engine home hands it their
memory, plugins, personal MCP registrations, global agents and steering, and
lets it write session state and settings edits back into them. That is the
pollution the session home keeps every run out of unless its binding names
`engine_home: host`.

**What `host` costs, stated plainly.** ctxloom never writes the real home, so
any surface an engine reads *only* from its home — hooks, MCP servers, prompts,
skills — is undeliverable to a run that keeps it. claude-code reads those from
cwd-keyed surfaces (`.claude/`, `.mcp.json`), so it pays nothing here; an
engine with no cwd-keyed equivalent gets a degraded run, told so out loud with
the fix (`engine_home: session`) named.

### Absent, with the reason, and one fail-loud

The resolution is either present or **absent with a stated reason**
(`AgentHomeResolution.Absent`) — an empty root with no reason is not a shape
it can take. It is absent when:

1. **the effective `engine_home` is `host`** — the binding's explicit
   selection, recorded but not warned about;
2. **the run carries no session name**, or **the engine declares no
   relocatable home** (`mock`), or **the instance cannot be created** — each
   warned out loud, because a binding that asked for a home and got none
   deserves to learn why.

A user's own `--env` still wins at the launch path's merge: the resolution
fills gaps; it never overrides.

No credential follows the home. When nothing in the env authenticates claude
(no stored or exported `CLAUDE_CODE_OAUTH_TOKEN`, no API-key var),
`isolation.PrepareInstanceHome` reports it and ctxloom records a
`KindIsolation` finding and contributes **nothing** — the run aborts at the
choke gate in both modes. Handing the engine a controlled home it cannot
authenticate in would trade a working run for a mysterious 401; falling back
to the host's own home would hand the agent the user's real login, so neither
is offered.

## Per-engine container specs

`engineContainerSpecFor(backend)` (`internal/adapters/isolation/enginespec.go`; called
`containerProfileFor` in a since-removed `profile.go` until 0.7.0 — renamed
because "profile" is ctxloom's *context-composition* concept and the collision is
what let two call sites key this table on an agent label instead of an engine).
The key is the REGISTERED BACKEND NAME, resolved **per call** from the backend
the run carries — never fixed at construction time. `composableEngines()`
is the roster of engines an image can be composed for;
`TestArch_EngineIdentityRosters_MembersAreRegisteredBackends` asserts every
name it returns is a registered backend. `isolation_engines` / `--engines` names WHICH per-engine images to build (an
image carries one engine, so there is no set to compose); `container build`
loops over them.

| Backend | Image | Install fragment | Build validate gate | `overlayDirs` | `transcriptStoreRel` |
|---|---|---|---|---|---|
| `claude-code` | `ctxloom-agent:latest` | npm — no official image resolves | `claude --version` + `adapterRunGate` | `.claude`, `.ctxloom/cache` | `.claude/projects` |
| `mock` | `ctxloom-agent:latest` | asserts `cat` only — installs no vendor CLI at all | `cat --version` | `.mock`, `.ctxloom/cache` | `""` (mock keeps no transcripts) |
| **fail-closed default** (an engine with no container declaration) | `ctxloom-agent:latest` | none | none | `.ctxloom/cache` only | `""` (no store is guessed) |

Every row above is the engine's OWN declaration (`engine.Descriptor.Container`),
pushed into `internal/adapters/isolation` at registration; isolation keeps no table.

The default arm is undeclared: an engine with no container declaration has
its containerized run refused at the container gate
(`prepareContainerScratch`). `isolation.HasContainerStory(backend)` /
`ContainerStoryEngines()` read the same declarations so the refusal can happen
*earlier*, at configuration time: `operations.validateContainerStory` (run from
`validateAgentAxes`, i.e. `agent create`/`agent edit`/`SetAgent`) rejects a
binding whose resulting `{engine, runtime: container-*}` pair names an engine
with no mapping, naming the supported set in the error.
The launch-time gate stays as the last line for paths that never went through a
binding.

The build gates verify the engine is **runnable**, not merely installed: every
fragment ends in `<client> --version`.

## The trace probe — read observation

`traceprobe.go` verifies *what files the engine actually reads* inside a cell.

- `TraceProbe{HostDir, ContainerDir, OutFile, Syscalls, SeccompProfile}` is a field on `RunSpec` — the seccomp override is a **structural** decision at the render site.
- Gate: `traceProbeFromEnv` returns non-nil **iff** `CTXLOOM_ISOLATION_PROBE_TRACE_DIR` is set, and materializes the loosened seccomp JSON. A write failure leaves `SeccompProfile=""`, i.e. Docker's default profile — failing toward *more* isolation. Called from `buildRunSpec` and `Container.buildRunnerSpec`.
- Render: `renderRunSpec` emits `--security-opt seccomp=<path>`, mounts the trace dir, and wraps the engine exec via `straceWrapPrefix`.
- Parse: `ParseStraceReads` → sorted, deduplicated `[]TraceRead`. `TraceRead.Failed()` is `Result != "ok"`. **ENOENT is first-class.** Consumer: `runProbeContainer` (`tests/acceptance/isolation_probe.go`).

`strace` is baked into the default base image and is harmless without
`CAP_SYS_PTRACE`.

Two further diagnostics: `Diagnose` backs `ctxloom container check` (read-only,
never errors by design), and `ReapOrphanedWorktrees` sweeps orphaned ephemeral
worktrees at startup, leaking rather than destroying anything WIP-bearing.

Superseded agent images are reaped only on request (`ctxloom container prune`,
dry run unless `--apply`). Ownership is proved by the labels a build stamps
(`imageStamp`, via `Runtime.buildArgs`), never by an image's name: every build
also applies a per-build ownership tag (`ownershipTagFor`) that no rebuild
reuses or moves, stamps it as `ctxloom.tag`, and `ownedImage` owns an image
only while one of its refs equals that label. So a build whose primary tag a
rebuild took over stays owned and prunable, while an image built FROM a ctxloom
image inherits the label but not the tag, and stays unowned. The base is handed
to the agent stage (and named as `ctxloom.from`) by its ownership tag, so a kept
image keeps exactly the base it was built on. The keep rules live in
`classifyImages`. It is never automatic, because worktrees at
different commits share one daemon: an automatic sweep in one would remove an
image another is between building and running.

## Key exported surface

| Symbol | File | Meaning |
|---|---|---|
| `Policy` | `isolation.go` | The seam: `Name` / `PrepareWorkspace` / `SpawnClient` / `StartRunner` |
| `Workspace` / `EnvWorkspace` | `isolation.go` | dir + teardown; optional env for what the workspace provisioned (scratch dir, git identity — never a config-home var). `EnvWorkspace` is implemented **only** by `worktreeWorkspace` |
| `ContainerInstanceHome` / `MountEngineHome` | `isolation.go` | the container's half of presenting a controlled engine home: the fixed in-container root, and the mounts (home directory, real credential file over it) that make it true |
| `Axes` / `WorkspaceAxis` / `RuntimeAxis` | `isolation.go` | The isolation request |
| `WorkspaceNames` / `RuntimeNames` | `isolation.go` | Single source for validation, completion, schema |
| `IsContainerRuntimeAxis` | `isolation.go` | "Is a container requested at all?", independent of which ownership |
| `Prepare` | `isolation.go` | Public entry; never returns an error |
| `IsContainerPolicyName` | `isolation.go` | The security predicate: "did we keep the boundary?" |
| `Isolated` | `isolation.go` | `p.Name() != "none"`; gates per-member config writes |
| `StarterForWorkspace` / `FactoryForWorkspace` / `WorkspaceEnv` | `isolation.go` | Binding adapters for `internal/adapters/operations` |
| `EngineStarter` / `RunnerHandle` | `isolation.go` | Launch closure; `{Name, Kill func(), Wait, StderrTail}` |
| `ImageConfig` | `isolation.go` | `Image`, `BaseContainerfile`, `AppRoot`, `NoDevcontainerBase`, `DevcontainerService`, `Engines` |
| `None` / `Container` / `Worktree` | `none.go` / `container.go` / `worktree.go` | The three policy types (four policy identities, six requestable postures) |
| `PrepareClaudeHome` | `auth.go` | The exported one-way copy-in seam, for per-session instance homes outside a `Policy` |
| `Runtime` / `Docker` / `Podman` / `Host` | `runtime.go` | Pluggable launcher substrate |
| `SelectRuntime` | `runtime.go` | Ownership-demanding selection; `Host{}` when no runtime serves the demanded ownership; never errors |
| `ProbeRuntime` | `runtime.go` | The unconstrained "what's reachable?" question — diagnostics/build only, never a run |
| `InContainer` | `runtime.go` | Self-detection (sentinel files + env + cgroup v1) |
| `RunSpec` / `LaunchSpec` / `Mount` | `runtime.go` | Run description / spawn params / bind mount |
| `SessionState` / `SessionStateFromEnv` | `statemounts.go` | Harp + project id threaded into the seam |
| `TraceProbe` / `TraceRead` / `ParseStraceReads` | `traceprobe.go` | Read-observation vocabulary |
| `Diagnosis` / `Diagnose` | `diagnose.go` | `container check` report |
| `BuildAgentImage` / `ImageBuildOptions` / `HostProvenanceDigest` | `imagebuild.go` | `container build` / `container provenance` |
| `ReapOrphanedWorktrees` / `WorktreeReapResult` | `worktree_reap.go` | Startup orphan sweep |
| `PlanImagePrune` / `ApplyImagePrune` / `LiveImageRef` | `image_prune.go` | `container prune` and the doctor superseded-images check |

## Invariants

1. **Degrade drops one axis at a time** (`chainFor`).
2. **Every lost container boundary is a refusal** — `strictness.FailAlways(KindIsolation)` in `chainFor`, and a non-degradable container→non-container transition in `prepareChain`. Aborts in both modes.
3. **An ownership mismatch is fatal, never a substitution** — `SelectRuntime` returns `Host{}` (not the other ownership's runtime) when the demanded ownership is unreachable, and the run then refuses rather than landing on the HOST.
4. **The chain always terminates in a workspace**; `Prepare` never errors.
5. **Unknown runtime axis is fail-closed; unknown workspace axis warns** (`warnUnknownAxes`).
6. **Auth env values never enter argv** — `envPassthrough` carries names only, because `/proc/<pid>/cmdline` is world-readable.
7. **The default container spec fails closed** (undeclared: the container gate refuses it).
8. **No implicit pull** — an absent image is built from a known source or the policy degrades.
9. **An unverifiable image *identity* fails loud; an unverifiable *label* reads as stale and triggers a rebuild** — opposite directions, both deliberate (`imageIdentityConfig` errors; `imageLabels` returns nil).
10. **A user-owned (run-as-is) image must satisfy the identity contract** — a ctxloom-governed entrypoint or a non-root user, else `KindIsolation` (`Container.checkRunAsIsIdentity`).
11. **The engine never runs as root in a governed image** — there is no override, in either mode; the build itself fails without a privilege-drop path (`overlayUserGate`).
12. **The build gates that the engine is runnable**, not merely installed.
13. **An agent image is content-keyed** — base content and the ONE engine are both in the tag.
14. **Bind-mount roots are verified, not assumed** (the shared-FS probe); an empty root set is an error, not an "ok".
15. **Commits from an agent never impersonate the human** — `gitIdentity` yields `"ctxloom agent <id>" <sanitized>@agents.ctxloom.local`.
16. **Worktree teardown leaks rather than destroys** — `force=false`, unknown-dirty treated as dirty, and a WIP-bearing orphan is SPARED (`teardownWorktree`, `worktree_reap.go`).
17. **`WorktreeVerdict`'s unhandled/unclassified case funnels to `VerdictSkipped` (never touch)** — the `default:` case in `ReapOrphanedWorktrees`'s tally.
18. **Session state names are path-validated** before becoming host paths (`safePathSegment`).

## Divergences from documented or implied behavior

*Stated factually; triage lives in `FINDINGS.md`.*

**Credential and coverage gaps**

- ~~**The default (unprofiled) container profile authenticates with claude credentials**~~ — **RESOLVED.** The default arm used to return `resolveClaudeContainerAuth`, passing `ANTHROPIC_*` and copy-mounting `~/.claude` into *any* unrecognized engine's container (reachable at the time: a generic `acp` backend was registered, and the ACP container transport passed an unrecognized or empty engine name through unchanged). It now fails **closed** (`noContainerHint`) and the launch aborts; `operations.validateContainerStory` refuses such a binding at write time so the abort is not the first the user hears of it.
- ~~**A backend in neither `credentialSeedSpecs` nor a curated-home registry gets a worktree with zero engine-global isolation and no finding at all**~~ — **PARTIALLY RESOLVED.** `Worktree.PrepareWorkspace` now records a `strictness.Fail(KindIsolation)` for any backend that is neither in `credentialSeedSpecs` nor named in `backendsWithNoGlobalState` — closing the gap for every unmapped engine. `backendsWithNoGlobalState` carries exactly one, independently-verified exemption (`mock`, which provably touches no engine-global state), not a silent carve-out; an empty backend (no agent context at all) stays silent by design.
- **The curated-HOME allowlist** that used to symlink `~/.gitconfig`/`~/.ssh` into a worktree's per-agent home **has been removed along with the whole curated-home mechanism** — `Worktree` now relies solely on `credentialSeedSpecs`' scoped env vars (`Worktree.prepareHomeVarDirs`), which is why `.gitconfig`/`.ssh` identity is left on the *shared* worktree checkout instead of being copied or symlinked per agent. Whether that removal fully retired the class of bug the old allowlist was tracking (over-broad `.ssh` exposure) was not re-verified here.
- **The worktree reaper's scope is `~/.ctxloom/sessions/*/ephemeral/` only** (`ReapOrphanedWorktrees`); worktrees on the `os.TempDir()` fallback are permanently unreapable, and nothing sweeps the sibling `ctxloom-tmp-*` dirs.
- **`worktreeWorkspace.Cleanup`'s idempotence guard is `dir` alone**, short-circuiting removal of `configHome` / `scratchDir` if a caller ever reaches it with `dir == ""` but either of those still set.

**Green build, nothing delivered**

- ~~**`composeAgentContainerfile(nil)` renders a complete, buildable, gate-passing image with zero engine layers.**~~ CLOSED 2026-08-25 by the one-image-per-engine split: `composableBuildSources` raises a fatal `KindIsolation` finding when the engine has no known install recipe, rather than building a green, empty image.
- **The staleness gate fails open**: `combineProvenance` returns `""` on unresolvable provenance and `imageStale("")` returns `false`, so any present image runs as-is with no diagnostic.
- **A stale image that cannot rebuild because `resolveSelfExe` failed launches with no warning and no finding**, while the parallel "rebuild failed" path raises a fatal `KindIsolation` for the identical outcome. `selfLinuxExe` errors unconditionally off Linux, so this is the **default path on macOS and Windows** dev hosts.
- **`overlayContainerfile` emits its client-validation `RUN` only when `validate != ""`**, and the default profile's `validate` is `""` — so `container build <unprofiled> --base-image X` tags an image never checked to contain any engine.
- **`sessionStateMounts` skips the transcript mount silently when `transcriptStoreRel == ""`**; a missing harp or project id degrades behind `clidiag.WarnOnce` — *once per process*, so in a fan-out only the first member's data loss is announced.

**Claims that overstate the boundary**

- **Host runtime is not a security boundary between agents.** Two agents
  launched with `runtime: host` run as the same OS user, and the coordinator
  credential (`CTXLOOM_COORD_CRED`) that `Coordinator.Identify`
  (`agentcoord/coord/coordinator.go:777`) accepts as sole proof of caller
  identity is exec-time environment: `/proc/<pid>/environ` exposes it to any
  other same-uid process for that process's entire lifetime, unsetting it
  after read does not scrub the kernel's snapshot, and where
  `ptrace_scope` permits same-uid ptrace a determined process can lift the
  same bytes out of memory even past that. `internal/shared/procsec` raises
  the cost of the file-read path but says so itself: "THIS IS BAR-RAISING,
  NOT A BOUNDARY … The isolation boundary is a container" (`procsec.go:12-17`).
- **`gitDirMounts` mounts the git common dir read-write** (only the `worktrees/` registry is masked). A member can therefore rewrite main's refs/objects/index, hooks and config.
- **`TraceProbe`'s doc claims the loosened seccomp profile is structurally unreachable from a normal run**, but the gate is a plain `os.Getenv` (`traceProbeFromEnv`) — any parent exporting `CTXLOOM_ISOLATION_PROBE_TRACE_DIR` makes every container run in that process ptrace-permitted and strace-wrapped.
- **`worktreeWorkspace.Env()` advertises `HomeVar` target directories that nothing creates** if `prepareHomeVarDirs` failed; isolation then depends on each engine choosing to `mkdir -p` rather than falling back to its global home.
- **`ImageConfig`'s doc claims "zero value = devcontainer auto-detect ON"** but `resolveDevBase` turns detection *off* when `AppRoot == ""`.

**Signal quality**

- **`ParseStraceReads` records `Result:"ok"` whenever the errno group is empty, ignoring the captured return value** — a failed syscall with no named errno reports success, inverting the probe's signal.
- **REAPED vs SPARED is decided by an `os.Stat` check** in `ClassifyOrphanedWorktrees`, so any stat error reports REAPED — and that number is printed to the user.
- **`SelectRuntime` silently substitutes on an unrecognized preference**: an explicit `podman` preference that is unknown or unavailable falls through to auto-detection with only a comment (`selectRuntimeWhere`), and the function never errors, so no caller can detect it. (This is orthogonal to the fatal ownership-mismatch path above — an *unrecognized runtime name* degrades quietly, a *recognized runtime with the wrong ownership* does not.)
- **`IsContainerPolicyName` matches duplicated string literals** rather than the constants the policies return, so a rename silently downgrades `prepareChain`'s fatal finding to a warn.

**Dead structure worth knowing before reading the package**

- `Resolve` and the `Chroot` runtime, previously noted here as test-only/unreachable dead code, have both been **removed outright** — there is nothing left to find under either name.
- The `Approvals` axis on `Policy` (four implementations with zero production consumers) has likewise been **removed outright**; only a comment in `runner.go` still names it, in passing, as "the policy-level Approvals axis that used to name the other boundary."
- `pidalive_unix.go` / `pidalive_windows.go` are no longer duplicated inside this package — they were extracted to `internal/shared/pidalive`, which now has multiple call sites across `cli`, `isolation`, `agentcoord/coord`, and `mcp`.

## See also

- [Capability matrix](capability-matrix.md) — the per-engine isolation summary table
- [Transport](../agentcoord/transport.md) — how a launch reaches the runner
- [The `Backend` abstraction and registry](backend-abstraction.md)
